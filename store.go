package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session 是 hook 寫、面板讀的一列。每個對話一個檔：<agent>-<id>.json
type Session struct {
	Agent      string `json:"agent"` // "claude"、"codex"
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Transcript string `json:"transcript"`
	Status     string `json:"status"` // "working"、"idle"、"ended"
	UpdatedAt  int64  `json:"updatedAt"`
	// 使用者在面板按掉的時間；之後這個對話再有新動靜（送出、開始）就重新出現
	HiddenAt int64 `json:"hiddenAt,omitempty"`
	// 提醒已經跳到第幾級，避免同一級一直跳
	Nudged int `json:"nudged,omitempty"`
}

func (s Session) Key() string { return s.Agent + "-" + s.ID }

func (s Session) Project() string {
	if s.Cwd == "" {
		return "?"
	}
	return filepath.Base(filepath.Clean(s.Cwd))
}

func dataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "ai-session-monitor")
}

func sessionsDir() string { return filepath.Join(dataDir(), "sessions") }

func sessionPath(key string) string {
	// key 來自 hook 的 session_id，只留安全字元
	clean := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, key)
	return filepath.Join(sessionsDir(), clean+".json")
}

func loadSession(key string) (Session, bool) {
	b, err := os.ReadFile(sessionPath(key))
	if err != nil {
		return Session{}, false
	}
	var s Session
	if json.Unmarshal(b, &s) != nil {
		return Session{}, false
	}
	return s, true
}

// saveSession 先寫暫存檔再改名，避免面板讀到寫一半的檔
func saveSession(s Session) error {
	if err := os.MkdirAll(sessionsDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	p := sessionPath(s.Key())
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func listSessions() []Session {
	entries, _ := os.ReadDir(sessionsDir())
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sessionsDir(), e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(b, &s) == nil && s.ID != "" {
			out = append(out, s)
		}
	}
	return out
}

func nowMs() int64 { return time.Now().UnixMilli() }
