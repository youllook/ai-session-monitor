package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed skill/SKILL.md
var skillMD []byte

//go:embed skill/codex/SKILL.md
var codexSkillMD []byte

// 這四個時機足夠：開始、送出（執行中）、回完（閒置＋提醒）、結束
var claudeEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}

const hookMarker = "ai-session-monitor"

func claudeHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

func codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

func installedBin() string {
	name := "aimon"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dataDir(), "bin", name)
}

// installClaude：把自己複製到固定位置、把 hook 合併進 ~/.claude/settings.json、放導覽 skill
func installClaude() error {
	bin := installedBin()
	if err := copySelf(bin); err != nil {
		return fmt.Errorf("複製程式失敗：%w", err)
	}
	settings := filepath.Join(claudeHome(), "settings.json")
	cmd := fmt.Sprintf("%q hook claude", filepath.ToSlash(bin))
	if err := editHooks(settings, func(h map[string]any) {
		for _, ev := range claudeEvents {
			groups := stripOurs(h[ev])
			groups = append(groups, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 10}},
			})
			h[ev] = groups
		}
	}); err != nil {
		return err
	}
	skillDir := filepath.Join(claudeHome(), "skills", "ai-session-monitor")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0o644); err != nil {
		return err
	}
	fmt.Println("已安裝：")
	fmt.Println("  程式  ", bin)
	fmt.Println("  hook  ", settings, "（原檔備份在 settings.json.aimon-bak）")
	fmt.Println("  導覽  ", filepath.Join(skillDir, "SKILL.md"))
	fmt.Println("下次開 Claude Code 對話時面板會自己跳出來；也可以執行：", bin, "panel")
	return nil
}

func uninstallClaude() error {
	settings := filepath.Join(claudeHome(), "settings.json")
	if err := editHooks(settings, func(h map[string]any) {
		for _, ev := range claudeEvents {
			if g := stripOurs(h[ev]); len(g) > 0 {
				h[ev] = g
			} else {
				delete(h, ev)
			}
		}
	}); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(claudeHome(), "skills", "ai-session-monitor"))
	fmt.Println("已移除 hook 與導覽 skill。程式與紀錄留在", dataDir(), "，不要了可以整個刪掉。")
	return nil
}

// installCodex：用使用者層級 hooks.json 加四個 lifecycle hook，不動 config.toml 與 notify。
func installCodex() error {
	bin := installedBin()
	if err := copySelf(bin); err != nil {
		return fmt.Errorf("複製程式失敗：%w", err)
	}
	hooksPath := filepath.Join(codexHome(), "hooks.json")
	cmd := codexHookCommand(bin, runtime.GOOS)
	if err := editCodexHooks(hooksPath, func(h map[string]any) {
		for _, ev := range claudeEvents {
			groups := stripOurs(h[ev])
			timeout := 10
			if ev == "SessionEnd" {
				timeout = 3 // Codex 限制 SessionEnd 最長三秒
			}
			groups = append(groups, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": timeout}},
			})
			h[ev] = groups
		}
	}); err != nil {
		return err
	}
	skillDir := filepath.Join(codexHome(), "skills", "ai-session-monitor")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), codexSkillMD, 0o644); err != nil {
		return err
	}
	fmt.Println("已安裝 Codex：")
	fmt.Println("  程式  ", bin)
	fmt.Println("  hook  ", hooksPath, "（原檔備份在 hooks.json.aimon-bak）")
	fmt.Println("  導覽  ", filepath.Join(skillDir, "SKILL.md"))
	fmt.Println("首次一般使用前請在 Codex 執行 /hooks 檢視並信任這四個 hook。")
	return nil
}

func uninstallCodex() error {
	hooksPath := filepath.Join(codexHome(), "hooks.json")
	if _, err := os.Stat(hooksPath); err == nil {
		if err := editCodexHooks(hooksPath, func(h map[string]any) {
			for _, ev := range claudeEvents {
				if g := stripOurs(h[ev]); len(g) > 0 {
					h[ev] = g
				} else {
					delete(h, ev)
				}
			}
		}); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.RemoveAll(filepath.Join(codexHome(), "skills", "ai-session-monitor"))
	fmt.Println("已移除 Codex hook 與導覽 skill。程式與紀錄留在", dataDir(), "，不要了可以整個刪掉。")
	return nil
}

// stripOurs 拿掉我們自己加過的那組（重複安裝不會疊兩份），別人的 hook 原封不動
func stripOurs(v any) []any {
	groups, _ := v.([]any)
	out := []any{}
	for _, g := range groups {
		m, _ := g.(map[string]any)
		hooks, _ := m["hooks"].([]any)
		ours := len(hooks) > 0
		for _, h := range hooks {
			hm, _ := h.(map[string]any)
			c, _ := hm["command"].(string)
			if !strings.Contains(c, hookMarker) {
				ours = false
			}
		}
		if !ours {
			out = append(out, g)
		}
	}
	return out
}

// editHooks 只改 "hooks" 這一欄；其他欄位的順序與內容照舊
func editHooks(path string, fn func(map[string]any)) error {
	return editHooksFile(path, fn, false)
}

// Codex 設定改動前必須確實寫成備份；Claude 保留原有容錯行為。
func editCodexHooks(path string, fn func(map[string]any)) error {
	return editHooksFile(path, fn, true)
}

func editHooksFile(path string, fn func(map[string]any), requireBackup bool) error {
	orig, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keys, vals, err := readTopLevel(orig)
	if err != nil {
		return fmt.Errorf("%s 不是合法的 JSON，沒有改它：%w", path, err)
	}
	hooks := map[string]any{}
	if raw, ok := vals["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("%s 的 hooks 格式看不懂，沒有改它：%w", filepath.Base(path), err)
		}
	} else {
		keys = append(keys, "hooks")
	}
	fn(hooks)
	var hb bytes.Buffer
	enc := json.NewEncoder(&hb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(hooks); err != nil {
		return err
	}
	vals["hooks"] = bytes.TrimSpace(hb.Bytes())

	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			compact.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		compact.Write(kb)
		compact.WriteByte(':')
		if err := json.Compact(&compact, vals[k]); err != nil {
			return err
		}
	}
	compact.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if len(orig) > 0 {
		if err := os.WriteFile(path+".aimon-bak", orig, 0o644); err != nil && requireBackup {
			return fmt.Errorf("備份 %s 失敗，沒有改原檔：%w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// readTopLevel 依原順序讀出最外層的 key 與原始值
func readTopLevel(b []byte) ([]string, map[string]json.RawMessage, error) {
	vals := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, vals, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("最外層不是物件")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = raw
	}
	return keys, vals, nil
}

func copySelf(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if s, _ := filepath.EvalSymlinks(self); s != "" {
		self = s
	}
	if d, _ := filepath.Abs(dst); strings.EqualFold(self, d) {
		return nil // 已經是從安裝位置執行
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	// Windows 上執行中的 exe 不能覆蓋，但可以改名讓開
	_ = os.Rename(dst, dst+".old")
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	_ = os.Remove(dst + ".old")
	return nil
}

// codexHookCommand：Codex 終端機版用 PowerShell 7 跑 hook，Desktop 桌面版的執行方式不同，
// 引號或 & 這類 shell 專用寫法在其中一邊會失敗（2026-10-14 實測）。所以 Windows 上給
// 不帶引號的路徑：PowerShell、cmd、直接執行都吃；路徑有空格時改用 8.3 短路徑避開空格。
// mac 走 sh，直接引號即可
func codexHookCommand(bin, goos string) string {
	if goos != "windows" {
		return fmt.Sprintf("%q hook codex", filepath.ToSlash(bin))
	}
	p := bin
	if strings.Contains(p, " ") {
		p = shortPath(p)
	}
	if strings.Contains(p, " ") {
		return fmt.Sprintf("& %q hook codex", filepath.ToSlash(p)) // 短路徑也拿不到時退回 PowerShell 寫法
	}
	return filepath.ToSlash(p) + " hook codex"
}
