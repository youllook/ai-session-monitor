package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 提醒門檻：講師要調就改這裡
const (
	nudgeLongContext = 200_000 // 對話長度到這裡提醒一次
	nudgeHugeContext = 500_000 // 到這裡再提醒一次
	nudgeTurnCost    = 0.50    // 一句超過這個金額（US$）也算第二級
	nudgeExpiredMin  = 50_000  // 快取過期後回來，對話至少這麼長才提醒
)

type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
}

// runHook：Claude Code 與 Codex 都在 SessionStart／UserPromptSubmit／Stop／SessionEnd 時呼叫。
// 任何錯誤都吞掉、正常結束：監看工具不能卡住使用者的對話。
func runHook(agent string, stdin io.Reader, stdout io.Writer) {
	raw, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	defer func() {
		if r := recover(); r != nil {
			hookLog(agent, fmt.Sprintf("panic: %v", r), raw)
		}
	}()
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		// 看不懂的輸入不擋使用者，但留紀錄：工具改版換了欄位時才查得到原因
		hookLog(agent, fmt.Sprintf("看不懂的 hook 輸入（err=%v）", err), raw)
		return
	}
	// Codex Desktop 在 Windows 沙箱裡跑 hook：讀得到記錄、連得到本機，但不能寫任何檔
	// （2026-10-14 實測）。寫不了就把事件交給面板代辦，面板回的提醒照樣輸出
	if !canWriteData() {
		delegateToPanel(agent, raw, stdout)
		return
	}
	key := agent + "-" + in.SessionID
	s, _ := loadSession(key)
	s.Agent, s.ID = agent, in.SessionID
	// 專案＝對話一開始的資料夾；之後 AI 在終端機切換資料夾不算換專案
	if s.Cwd == "" || in.Event == "SessionStart" {
		s.Cwd = in.Cwd
	}
	if in.TranscriptPath != "" {
		s.Transcript = in.TranscriptPath
	}
	s.UpdatedAt = nowMs()

	var msg string
	switch in.Event {
	case "SessionStart":
		s.Status, s.HiddenAt = "idle", 0
		ensurePanel()
	case "UserPromptSubmit":
		st := parseFor(s, true)
		// 快取過期後才回來：這句要整段重建
		if st.LastAt > 0 && st.Context >= nudgeExpiredMin && nowMs() > st.LastAt+st.TTL*1000 {
			msg = fmt.Sprintf("快取已過期：這句要把整段對話（%s）重新寫進快取，約 %s。換題目的話建議 /clear。",
				tokensK(st.Context), usd(rebuildCost(st)))
		}
		s.Status, s.HiddenAt = "working", 0
		ensurePanel()
	case "Stop":
		s.Status = "idle"
		st := parseFor(s, true)
		level := 0
		switch {
		case st.Context >= nudgeHugeContext || st.TurnCost >= nudgeTurnCost:
			level = 2
		case st.Context >= nudgeLongContext:
			level = 1
		}
		if level > s.Nudged {
			s.Nudged = level
			msg = nudgeText(level, st)
		} else if level < s.Nudged {
			s.Nudged = level // /clear 或 /compact 之後降回來，下次再越線會再提醒
		}
	case "SessionEnd":
		s.Status = "ended"
	}
	_ = saveSession(s)
	if msg != "" {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"systemMessage": "💸 " + msg})
	}
}

func nudgeText(level int, st Stats) string {
	read := priceOf(st.Model).Read
	if strings.HasPrefix(st.Model, "gpt-") && st.Context > 272_000 {
		read *= 2
	}
	floor := float64(st.Context) / 1e6 * read
	if level >= 2 {
		return fmt.Sprintf("這句花了 %s，對話已經 %s：之後每句光重讀就要 %s 起跳。不需要前面的內容就 /clear，還需要就 /compact。",
			usd(st.TurnCost), tokensK(st.Context), usd(floor))
	}
	return fmt.Sprintf("對話已經 %s，每句光重讀就要 %s 起跳，越聊越貴。換下一件事請 /clear。",
		tokensK(st.Context), usd(floor))
}

func rebuildCost(st Stats) float64 {
	x := 1.25
	if strings.HasPrefix(st.Model, "gpt-") && st.Context > 272_000 {
		x *= 2
	} else if st.TTL >= 3600 {
		x = 2
	}
	return float64(st.Context) / 1e6 * priceOf(st.Model).Input * x
}

// parseFor 用存起來的進度增量讀記錄；save＝把進度寫回（hook 用）
func parseFor(s Session, save bool) Stats {
	p := loadParser(s)
	if s.Transcript != "" {
		_ = p.Update(s.Transcript)
	}
	if save {
		saveParser(s.Key(), p)
	}
	return p.Stats()
}

func parserPath(key string) string {
	return filepath.Join(dataDir(), "parse", filepath.Base(sessionPath(key)))
}

type transcriptParser interface {
	Update(string) error
	Stats() Stats
}

func newParser(agent string) transcriptParser {
	if agent == "codex" {
		return &CodexParser{}
	}
	return &ClaudeParser{}
}

func loadParser(s Session) transcriptParser {
	p := newParser(s.Agent)
	if b, err := os.ReadFile(parserPath(s.Key())); err == nil {
		_ = json.Unmarshal(b, p)
	}
	return p
}

func saveParser(key string, p transcriptParser) {
	_ = os.MkdirAll(filepath.Dir(parserPath(key)), 0o755)
	b, _ := json.Marshal(p)
	tmp := parserPath(key) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, parserPath(key))
	}
}

// ensurePanel：面板沒在跑就在背景開起來（第一次開對話時跳出視窗）
func ensurePanel() {
	if os.Getenv("AIMON_NO_PANEL") == "1" {
		return // 不想自動跳面板的人（與測試）可以關掉
	}
	c, err := net.DialTimeout("tcp", panelAddress(), 300*time.Millisecond)
	if err == nil {
		c.Close()
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "panel")
	detach(cmd)
	_ = cmd.Start()
}

// hookLog 把出問題的 hook 輸入記到 <dataDir>/hook.log（只留最後約 256KB）
func hookLog(agent, why string, raw []byte) {
	p := filepath.Join(dataDir(), "hook.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 256<<10 {
		_ = os.Rename(p, p+".old")
	}
	_ = os.MkdirAll(dataDir(), 0o755)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(raw) > 4000 {
		raw = raw[:4000]
	}
	fmt.Fprintf(f, "%s [%s] %s\n%s\n\n", time.Now().Format(time.RFC3339), agent, why, raw)
}

// canWriteData：試寫一個暫存檔，確認這個身分能寫監看資料夾
func canWriteData() bool {
	if os.MkdirAll(sessionsDir(), 0o755) != nil {
		return false
	}
	f, err := os.CreateTemp(sessionsDir(), ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// delegateToPanel 把原始 hook 輸入交給面板處理；面板沒在跑就只能放棄（沙箱裡也開不了面板）
func delegateToPanel(agent string, raw []byte, stdout io.Writer) {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post("http://"+panelAddress()+"/api/hook?agent="+url.QueryEscape(agent), "application/json", bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintln(os.Stderr, "aimon：寫不了紀錄，面板也連不到：", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(stdout, io.LimitReader(resp.Body, 64<<10))
}
