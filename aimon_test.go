package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 每個測試用自己的設定資料夾，不碰真的 %AppData%
func isolate(t *testing.T) string {
	d := t.TempDir()
	t.Setenv("APPDATA", d)
	t.Setenv("XDG_CONFIG_HOME", d)
	t.Setenv("HOME", d)
	t.Setenv("AIMON_NO_PANEL", "1")
	return d
}

func assistant(id string, read, create1h, input, output int64, ts string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"timestamp":%q,"message":{"id":%q,"model":"claude-opus-5-5","content":[],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":%d}}}}`,
		ts, id, input, output, read, create1h, create1h)
}

const (
	userPrompt = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"幫我讀這張單"}}`
	toolResult = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`
	title      = `{"type":"custom-title","customTitle":"KLOOK 讀單"}`
)

func writeLines(t *testing.T, path string, lines ...string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParserDedupesAndSplitsTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// 第一句：一則回覆寫成兩行（同 id）→ 只算一次；工具結果後又一則回覆，仍算同一句
	writeLines(t, path, title, userPrompt,
		assistant("m1", 0, 10_000, 100, 50, "2026-10-14T01:00:00Z"),
		assistant("m1", 0, 10_000, 100, 50, "2026-10-14T01:00:00Z"),
		toolResult,
		assistant("m2", 10_000, 0, 10, 20, "2026-10-14T01:00:05Z"),
	)
	p := &ClaudeParser{}
	if err := p.Update(path); err != nil {
		t.Fatal(err)
	}
	m1 := (0.0001*4 + 0.01*4*2 + 0.00005*20)  // 0.0814
	m2 := (0.00001*4 + 0.01*0.2 + 0.00002*20) // 0.00244
	st := p.Stats()
	if st.Title != "KLOOK 讀單" || st.TTL != 3600 || st.Context != 10_010 || st.Window != 1_000_000 {
		t.Fatalf("stats = %+v", st)
	}
	if !near(st.Total, m1+m2) || !near(st.TurnCost, m1+m2) {
		t.Fatalf("total=%v turn=%v want %v", st.Total, st.TurnCost, m1+m2)
	}

	// 第二句：增量讀，這句重新算、累計往上加
	writeLines(t, path, userPrompt, assistant("m3", 10_010, 0, 5, 10, "2026-10-14T01:10:00Z"))
	if err := p.Update(path); err != nil {
		t.Fatal(err)
	}
	m3 := 0.000005*4 + 0.01001*0.2 + 0.00001*20
	st = p.Stats()
	if !near(st.TurnCost, m3) || !near(st.Total, m1+m2+m3) {
		t.Fatalf("turn=%v total=%v", st.TurnCost, st.Total)
	}
	if st.CacheReads != 10_000+10_010 {
		t.Fatalf("reads=%d", st.CacheReads)
	}
}

func TestParserTitleFallsBackToFirstPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, userPrompt, assistant("m1", 0, 1000, 1, 1, "2026-10-14T01:00:00Z"))
	p := &ClaudeParser{}
	_ = p.Update(path)
	if got := p.Stats().Title; got != "幫我讀這張單" {
		t.Fatalf("title=%q", got)
	}
}

func TestClaudeParserReadsLatestMainEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	low := strings.Replace(assistant("m1", 0, 1000, 1, 1, "2026-10-14T01:00:00Z"),
		`"timestamp"`, `"effort":"low","timestamp"`, 1)
	sidechain := strings.Replace(assistant("side", 0, 1000, 1, 1, "2026-10-14T01:00:01Z"),
		`"isSidechain":false,"timestamp"`, `"isSidechain":true,"effort":"max","timestamp"`, 1)
	high := strings.Replace(assistant("m2", 1000, 0, 1, 1, "2026-10-14T01:00:02Z"),
		`"timestamp"`, `"effort":"high","timestamp"`, 1)
	writeLines(t, path, userPrompt, low, sidechain, high)
	p := &ClaudeParser{}
	if err := p.Update(path); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats().Effort; got != "high" {
		t.Fatalf("effort=%q want high", got)
	}
}

func makeCodexLine(kind string, payload any, ts string) string {
	b, _ := json.Marshal(map[string]any{"timestamp": ts, "type": kind, "payload": payload})
	return string(b)
}

func codexUsage(response string, input, cached, written, output int64, ts string) string {
	return makeCodexLine("token_usage_record", map[string]any{
		"response_id": response,
		"usage": map[string]int64{
			"input_tokens": input, "cached_input_tokens": cached,
			"cache_write_input_tokens": written, "output_tokens": output,
		},
	}, ts)
}

func codexTask(ts string) string {
	return makeCodexLine("event_msg", map[string]any{"type": "task_started"}, ts)
}

func codexPrompt(text, ts string) string {
	return makeCodexLine("response_item", map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}, ts)
}

func TestCodexParserDedupesUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	u := map[string]int64{"input_tokens": 10_000, "cached_input_tokens": 8_000, "output_tokens": 100}
	writeLines(t, path,
		makeCodexLine("turn_context", map[string]any{"model": "gpt-5.6-sol", "effort": "xhigh"}, "2026-10-14T01:00:00Z"),
		codexTask("2026-10-14T01:00:01Z"), codexPrompt("幫我查這個錯誤", "2026-10-14T01:00:01Z"),
		codexUsage("resp-1", 10_000, 8_000, 0, 100, "2026-10-14T01:00:02Z"),
		codexUsage("resp-1", 10_000, 8_000, 0, 100, "2026-10-14T01:00:02Z"),
		// 同一次請求還會寫累計 token_count，只拿它的視窗上限，不重複計價。
		makeCodexLine("event_msg", map[string]any{"type": "token_count", "info": map[string]any{
			"last_token_usage": u, "model_context_window": 258_400,
		}}, "2026-10-14T01:00:02Z"),
	)
	p := &CodexParser{}
	if err := p.Update(path); err != nil {
		t.Fatal(err)
	}
	st := p.Stats()
	want := (2_000*4.0 + 8_000*0.4 + 100*20.0) / 1e6
	if !near(st.Total, want) || !near(st.TurnCost, want) {
		t.Fatalf("total=%v turn=%v want=%v", st.Total, st.TurnCost, want)
	}
	if st.Context != 10_000 || st.Cache != 8_000 || st.CacheReads != 8_000 || st.Window != 258_400 || st.TTL != 1800 {
		t.Fatalf("stats=%+v", st)
	}
	if st.Effort != "xhigh" {
		t.Fatalf("effort=%q want xhigh", st.Effort)
	}
}

func TestNotesAPI(t *testing.T) {
	isolate(t)
	h := panelHandler(&panel{parsers: map[string]transcriptParser{}})

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/notes", nil))
	if get.Code != http.StatusOK || get.Body.String() != "" {
		t.Fatalf("empty GET: code=%d body=%q", get.Code, get.Body.String())
	}

	want := "待回覆\n- 旅客護照\n- 飯店確認"
	put := httptest.NewRecorder()
	h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/notes", strings.NewReader(want)))
	if put.Code != http.StatusNoContent {
		t.Fatalf("PUT: code=%d body=%q", put.Code, put.Body.String())
	}
	get = httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/notes", nil))
	if get.Code != http.StatusOK || get.Body.String() != want {
		t.Fatalf("saved GET: code=%d body=%q", get.Code, get.Body.String())
	}
	second := "已回覆\n- 旅客確認"
	put = httptest.NewRecorder()
	h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/notes", strings.NewReader(second)))
	if put.Code != http.StatusNoContent {
		t.Fatalf("second PUT: code=%d body=%q", put.Code, put.Body.String())
	}
	want = second

	tooLarge := httptest.NewRecorder()
	h.ServeHTTP(tooLarge, httptest.NewRequest(http.MethodPut, "/api/notes", strings.NewReader(strings.Repeat("x", maxNotesBytes+1))))
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large PUT: code=%d", tooLarge.Code)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir(), "notes.md")); err != nil || string(b) != want {
		t.Fatalf("oversize request changed note: body=%q err=%v", b, err)
	}
}

func TestCodexParserTurnBoundaryIgnoresToolResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, path,
		makeCodexLine("turn_context", map[string]any{"model": "gpt-5.6-sol"}, "2026-10-14T01:00:00Z"),
		codexTask("2026-10-14T01:00:01Z"), codexPrompt("第一句", "2026-10-14T01:00:01Z"),
		codexUsage("resp-1", 2_000, 1_000, 0, 50, "2026-10-14T01:00:02Z"),
		makeCodexLine("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "ok"}, "2026-10-14T01:00:03Z"),
		codexUsage("resp-2", 3_000, 2_000, 0, 50, "2026-10-14T01:00:04Z"),
		codexTask("2026-10-14T01:01:00Z"), codexPrompt("第二句", "2026-10-14T01:01:00Z"),
		codexUsage("resp-3", 4_000, 3_000, 0, 50, "2026-10-14T01:01:01Z"),
	)
	p := &CodexParser{}
	_ = p.Update(path)
	one := (1_000*4.0 + 1_000*0.4 + 50*20.0) / 1e6
	two := (1_000*4.0 + 2_000*0.4 + 50*20.0) / 1e6
	three := (1_000*4.0 + 3_000*0.4 + 50*20.0) / 1e6
	st := p.Stats()
	if st.Title != "第一句" || !near(st.Total, one+two+three) || !near(st.TurnCost, three) {
		t.Fatalf("stats=%+v", st)
	}
}

func TestCodexParserIncrementalRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, path,
		makeCodexLine("turn_context", map[string]any{"model": "gpt-5.6-sol"}, "2026-10-14T01:00:00Z"),
		codexTask("2026-10-14T01:00:01Z"), codexPrompt("增量讀", "2026-10-14T01:00:01Z"),
		codexUsage("resp-1", 2_000, 1_000, 0, 50, "2026-10-14T01:00:02Z"),
	)
	p := &CodexParser{}
	if err := p.Update(path); err != nil {
		t.Fatal(err)
	}
	before, offset := p.Stats().Total, p.Offset
	partial := codexUsage("resp-2", 3_000, 2_000, 0, 50, "2026-10-14T01:00:03Z")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(partial)
	_ = f.Close()
	_ = p.Update(path)
	if p.Offset != offset || !near(p.Stats().Total, before) {
		t.Fatalf("未完整一行不應被讀取：offset=%d total=%v", p.Offset, p.Stats().Total)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\n")
	_ = f.Close()
	_ = p.Update(path)
	if p.Offset <= offset || !(p.Stats().Total > before) {
		t.Fatalf("增量讀失敗：offset=%d total=%v", p.Offset, p.Stats().Total)
	}
}

func TestTokensK(t *testing.T) {
	for n, want := range map[int64]string{212_300: "212.3K", 1_000_000: "1M", 1_250_000: "1.2M", 999: "999"} {
		if got := tokensK(n); got != want {
			t.Errorf("tokensK(%d)=%q want %q", n, got, want)
		}
	}
}

func TestEditHooksKeepsOrderAndOthers(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "env": {"A": "1"},
  "model": "opus[1m]",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "play-sound <done>", "async": true}]}]
  },
  "zeta": true
}`
	_ = os.WriteFile(path, []byte(orig), 0o644)
	add := func(h map[string]any) {
		for _, ev := range claudeEvents {
			h[ev] = append(stripOurs(h[ev]), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `"C:/x/ai-session-monitor/bin/aimon.exe" hook claude`}}})
		}
	}
	// 裝兩次也只會有一份
	if err := editHooks(path, add); err != nil {
		t.Fatal(err)
	}
	if err := editHooks(path, add); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if i, j, k := strings.Index(s, `"env"`), strings.Index(s, `"model"`), strings.Index(s, `"zeta"`); !(i < j && j < k) {
		t.Fatalf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, "play-sound <done>") {
		t.Fatalf("lost existing hook or escaped it:\n%s", s)
	}
	if n := strings.Count(s, "aimon.exe"); n != len(claudeEvents) {
		t.Fatalf("aimon hooks = %d, want %d:\n%s", n, len(claudeEvents), s)
	}
	// 移除後只剩原本的
	_ = editHooks(path, func(h map[string]any) {
		for _, ev := range claudeEvents {
			if g := stripOurs(h[ev]); len(g) > 0 {
				h[ev] = g
			} else {
				delete(h, ev)
			}
		}
	})
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "aimon") || !strings.Contains(string(b), "play-sound") {
		t.Fatalf("uninstall wrong:\n%s", b)
	}
}

func TestEditCodexHooksBacksUpAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	orig := `{"description":"keep","hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	add := func(h map[string]any) {
		for _, ev := range claudeEvents {
			h[ev] = append(stripOurs(h[ev]), map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": `"C:/x/ai-session-monitor/bin/aimon.exe" hook codex`}},
			})
		}
	}
	if err := editCodexHooks(path, add); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".aimon-bak")
	if err != nil || string(backup) != orig {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
	if err := editCodexHooks(path, add); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "aimon.exe") != len(claudeEvents) || !strings.Contains(string(b), "other-tool") {
		t.Fatalf("idempotent install wrong:\n%s", b)
	}
	if err := editCodexHooks(path, func(h map[string]any) {
		for _, ev := range claudeEvents {
			if g := stripOurs(h[ev]); len(g) > 0 {
				h[ev] = g
			} else {
				delete(h, ev)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "aimon") || !strings.Contains(string(b), "other-tool") || !strings.Contains(string(b), `"description": "keep"`) {
		t.Fatalf("uninstall wrong:\n%s", b)
	}
}

func hook(t *testing.T, event, transcript string) map[string]string {
	in, _ := json.Marshal(map[string]string{"session_id": "abc", "transcript_path": transcript, "cwd": `D:\work\AI測試教學_課程`, "hook_event_name": event})
	var out bytes.Buffer
	runHook("claude", bytes.NewReader(in), &out)
	var m map[string]string
	_ = json.Unmarshal(out.Bytes(), &m)
	return m
}

func TestHookStatusAndNudges(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, userPrompt, assistant("m1", 150_000, 0, 10, 10, "2026-10-14T01:00:00Z"))
	// Stop 時對話 15 萬：還沒到門檻，不提醒
	if m := hook(t, "Stop", path); m != nil {
		t.Fatalf("unexpected nudge %v", m)
	}
	s, ok := loadSession("claude-abc")
	if !ok || s.Status != "idle" || s.Project() != "AI測試教學_課程" {
		t.Fatalf("session=%+v", s)
	}
	// 長到 25 萬：第一級提醒，且只跳一次
	writeLines(t, path, userPrompt, assistant("m2", 250_000, 0, 10, 10, "2026-10-14T01:01:00Z"))
	if m := hook(t, "Stop", path); !strings.Contains(m["systemMessage"], "/clear") {
		t.Fatalf("want level-1 nudge, got %v", m)
	}
	if m := hook(t, "Stop", path); m != nil {
		t.Fatalf("repeated nudge %v", m)
	}
	// AI 在終端機切到別的資料夾，專案名稱不跟著變
	in, _ := json.Marshal(map[string]string{"session_id": "abc", "transcript_path": path, "cwd": `D:\work\AI測試教學_課程\sub`, "hook_event_name": "Stop"})
	runHook("claude", bytes.NewReader(in), &bytes.Buffer{})
	if s, _ := loadSession("claude-abc"); s.Project() != "AI測試教學_課程" {
		t.Fatalf("project changed to %q", s.Project())
	}
	// 被按掉後，新送出一句會回到清單
	s, _ = loadSession("claude-abc")
	s.HiddenAt = nowMs()
	_ = saveSession(s)
	hook(t, "UserPromptSubmit", path)
	if s, _ = loadSession("claude-abc"); s.HiddenAt != 0 || s.Status != "working" {
		t.Fatalf("after submit: %+v", s)
	}
}

func TestCodexHookCommandPerShell(t *testing.T) {
	if got := codexHookCommand(`C:\Users\a\aimon.exe`, "windows"); got != `C:/Users/a/aimon.exe hook codex` {
		t.Fatalf("windows: %s", got)
	}
	if got := codexHookCommand("/Users/a b/aimon", "darwin"); got != `"/Users/a b/aimon" hook codex` {
		t.Fatalf("darwin: %s", got)
	}
}
