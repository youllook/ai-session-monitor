package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// Stats 是一個對話目前的帳：面板與提醒都只看這個
type Stats struct {
	Title      string  `json:"title"`
	Model      string  `json:"model"`
	Effort     string  `json:"effort"`
	Context    int64   `json:"context"`    // 主對話最近一次請求的整段提示
	Window     int     `json:"window"`     // 模型的上下文上限
	Cache      int64   `json:"cache"`      // 最近一次請求在快取裡的部分（讀＋寫）
	LastAt     int64   `json:"lastAt"`     // 最近一次回覆時間（Unix 毫秒），快取倒數從這裡起算
	TTL        int64   `json:"ttl"`        // 快取時效（秒）：300 或 3600
	TurnCost   float64 `json:"turnCost"`   // 這一句（使用者最近一則訊息觸發的所有請求）
	Total      float64 `json:"total"`      // 整個對話累計
	CacheReads int64   `json:"cacheReads"` // 累計讀快取 token
}

// ClaudeParser 增量讀 Claude Code 的對話記錄（JSONL，只會往後長）。
// 同一則回覆會依內容區塊寫成好幾行、帶同一個 message.id 與同一份 usage，
// 所以用 id 去重：id 換了才把上一則記進帳（committed），最新一則先當 pending。
type ClaudeParser struct {
	Offset      int64   `json:"offset"`
	Title       string  `json:"title"`
	FirstPrompt string  `json:"firstPrompt"`
	Model       string  `json:"model"`
	Effort      string  `json:"effort"`
	LastAt      int64   `json:"lastAt"`
	TTL         int64   `json:"ttl"`
	Total       float64 `json:"total"`
	Turn        float64 `json:"turn"`
	Reads       int64   `json:"reads"`
	PendingID   string  `json:"pendingId"`
	Pending     *Usage  `json:"pending"`
	PendingMod  string  `json:"pendingModel"`
	LastUsage   *Usage  `json:"lastUsage"`
}

type claudeLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Effort      string `json:"effort"`
	Timestamp   string `json:"timestamp"`
	CustomTitle string `json:"customTitle"`
	Summary     string `json:"summary"`
	Message     *struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *Usage          `json:"usage"`
	} `json:"message"`
}

func (p *ClaudeParser) commit() {
	if p.Pending == nil {
		return
	}
	c := p.Pending.Cost(p.PendingMod)
	p.Total += c
	p.Turn += c
	p.Reads += p.Pending.CacheRead
	p.Pending, p.PendingID, p.PendingMod = nil, "", ""
}

// Update 從上次讀到的位置往後讀；檔案變短（被換掉）就從頭來
func (p *ClaudeParser) Update(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < p.Offset {
		*p = ClaudeParser{}
	}
	if _, err := f.Seek(p.Offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			p.Offset += int64(len(line))
			p.feed(line)
		}
		// 沒有換行的半行留到下次（對方可能還在寫）
		if err != nil {
			break
		}
	}
	return nil
}

func (p *ClaudeParser) feed(line []byte) {
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	switch l.Type {
	case "custom-title":
		if l.CustomTitle != "" {
			p.Title = l.CustomTitle
		}
	case "summary":
		if p.Title == "" && l.Summary != "" {
			p.Title = l.Summary
		}
	case "user":
		if l.IsSidechain || l.IsMeta || l.Message == nil {
			return
		}
		text, isPrompt := promptText(l.Message.Content)
		if !isPrompt {
			return // 工具結果不算新的一句
		}
		p.commit()
		p.Turn = 0
		if p.FirstPrompt == "" {
			p.FirstPrompt = text
		}
	case "assistant":
		if l.IsSidechain {
			return
		}
		// effort 在 assistant 記錄的最外層；只採主對話最新的值。
		p.Effort = l.Effort
		if l.Message == nil || l.Message.Usage == nil {
			return
		}
		m := l.Message
		if m.ID != p.PendingID {
			p.commit()
			p.PendingID = m.ID
		}
		u := *m.Usage
		p.Pending, p.PendingMod = &u, m.Model
		p.Model = m.Model
		p.LastUsage = &u
		if u.CacheBreak != nil && u.CacheBreak.Write1h > 0 {
			p.TTL = 3600
		} else if u.CacheBreak != nil && u.CacheBreak.Write5m > 0 {
			p.TTL = 300
		} else if p.TTL == 0 {
			p.TTL = u.TTLSeconds()
		}
		if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
			p.LastAt = t.UnixMilli()
		}
	}
}

// promptText：使用者自己打的字才算一句；只有 tool_result 的不算
func promptText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clip(s), !strings.HasPrefix(strings.TrimSpace(s), "<")
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	for _, b := range blocks {
		if b.Type == "text" && !strings.HasPrefix(strings.TrimSpace(b.Text), "<") {
			return clip(b.Text), true
		}
	}
	return "", false
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// Stats 把目前的帳整理給面板；pending 那則也算進去
func (p *ClaudeParser) Stats() Stats {
	s := Stats{Title: p.Title, Model: p.Model, Effort: p.Effort, LastAt: p.LastAt, TTL: p.TTL,
		Total: p.Total, TurnCost: p.Turn, CacheReads: p.Reads}
	if s.Title == "" {
		s.Title = p.FirstPrompt
	}
	if p.Pending != nil {
		c := p.Pending.Cost(p.PendingMod)
		s.Total += c
		s.TurnCost += c
		s.CacheReads += p.Pending.CacheRead
	}
	if p.LastUsage != nil {
		s.Context = p.LastUsage.Prompt()
		s.Cache = p.LastUsage.CacheRead + p.LastUsage.CacheCreate
	}
	s.Window = priceOf(p.Model).Window
	return s
}
