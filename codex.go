package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// CodexUsage 是 Codex token_usage_record 裡一次模型請求的用量。
// input_tokens 已包含快取讀取與寫入，計價時要先扣掉再分別套單價。
type CodexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

func (u CodexUsage) Cost(model string) float64 {
	p := priceOf(model)
	ordinary := max(int64(0), u.Input-u.Cached-u.CacheWrite)
	inMul, outMul := 1.0, 1.0
	// OpenAI 這些模型的長上下文請求（>272K）整筆改用長文價。
	if strings.HasPrefix(model, "gpt-") && u.Input > 272_000 {
		inMul, outMul = 2, 1.5
	}
	input := (float64(ordinary)*p.Input +
		float64(u.Cached)*p.Read +
		float64(u.CacheWrite)*p.Input*1.25) * inMul
	return (input + float64(u.Output)*p.Output*outMul) / 1e6
}

// CodexParser 增量讀 Codex rollout JSONL。token_usage_record 同時有單次、
// 本句與全對話用量；這裡只累加唯一 response_id 的單次 usage，
// 避免把每次重寫的累計值重複計價。
type CodexParser struct {
	Offset          int64           `json:"offset"`
	FirstPrompt     string          `json:"firstPrompt"`
	Model           string          `json:"model"`
	Effort          string          `json:"effort"`
	Window          int             `json:"window"`
	LastAt          int64           `json:"lastAt"`
	TTL             int64           `json:"ttl"`
	Total           float64         `json:"total"`
	Turn            float64         `json:"turn"`
	Reads           int64           `json:"reads"`
	LastUsage       *CodexUsage     `json:"lastUsage"`
	SeenResponses   map[string]bool `json:"seenResponses,omitempty"`
	SeenFallback    map[string]bool `json:"seenFallback,omitempty"`
	HasUsageRecords bool            `json:"hasUsageRecords,omitempty"`
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func (p *CodexParser) Update(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < p.Offset {
		*p = CodexParser{}
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
		if err != nil {
			break
		}
	}
	return nil
}

func (p *CodexParser) feed(line []byte) {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	switch l.Type {
	case "turn_context":
		var v struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		}
		if json.Unmarshal(l.Payload, &v) == nil {
			if v.Model != "" {
				p.Model = v.Model
				p.TTL = codexTTL(v.Model)
			}
			p.Effort = v.Effort
		}
	case "response_item":
		p.feedResponseItem(l.Payload)
	case "token_usage_record":
		p.feedUsageRecord(l.Payload, l.Timestamp)
	case "event_msg":
		p.feedEvent(l.Payload, l.Timestamp)
	}
}

func (p *CodexParser) feedResponseItem(raw json.RawMessage) {
	var v struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Type != "message" || v.Role != "user" || p.FirstPrompt != "" {
		return
	}
	for _, c := range v.Content {
		text := strings.TrimSpace(c.Text)
		if c.Type == "input_text" && text != "" && !strings.HasPrefix(text, "<") {
			p.FirstPrompt = clip(text)
			return
		}
	}
}

func (p *CodexParser) feedUsageRecord(raw json.RawMessage, timestamp string) {
	var v struct {
		ResponseID string     `json:"response_id"`
		Usage      CodexUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &v) != nil || v.ResponseID == "" {
		return
	}
	p.HasUsageRecords = true
	if p.SeenResponses == nil {
		p.SeenResponses = map[string]bool{}
	}
	if p.SeenResponses[v.ResponseID] {
		return
	}
	p.SeenResponses[v.ResponseID] = true
	p.addUsage(v.Usage, timestamp)
}

func (p *CodexParser) feedEvent(raw json.RawMessage, timestamp string) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return
	}
	if head.Type == "task_started" {
		p.Turn = 0 // Codex 一個 task 就是使用者送出的一句；工具結果不會觸發它
		return
	}
	if head.Type != "token_count" {
		return
	}
	var v struct {
		Info *struct {
			Last   CodexUsage `json:"last_token_usage"`
			Window int        `json:"model_context_window"`
		} `json:"info"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Info == nil {
		return
	}
	if v.Info.Window > 0 {
		p.Window = v.Info.Window // Codex 實際可用上限，可能小於 API 模型上限
	}
	if p.HasUsageRecords {
		return // 新版同時寫 token_usage_record，不要再算一次 event_msg
	}
	key := fmt.Sprintf("%d/%d/%d/%d", v.Info.Last.Input, v.Info.Last.Cached, v.Info.Last.CacheWrite, v.Info.Last.Output)
	if p.SeenFallback == nil {
		p.SeenFallback = map[string]bool{}
	}
	if p.SeenFallback[key] {
		return
	}
	p.SeenFallback[key] = true
	p.addUsage(v.Info.Last, timestamp)
}

func (p *CodexParser) addUsage(u CodexUsage, timestamp string) {
	c := u.Cost(p.Model)
	p.Total += c
	p.Turn += c
	p.Reads += u.Cached
	p.LastUsage = &u
	if t, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		p.LastAt = t.UnixMilli()
	}
}

func codexTTL(model string) int64 {
	if strings.HasPrefix(model, "gpt-5.6") || strings.HasPrefix(model, "gpt-6") {
		return 30 * 60 // 官方保證至少 30 分鐘，實際可能更久
	}
	return 0
}

func (p *CodexParser) Stats() Stats {
	s := Stats{Title: p.FirstPrompt, Model: p.Model, Effort: p.Effort, Window: p.Window, LastAt: p.LastAt,
		TTL: p.TTL, Total: p.Total, TurnCost: p.Turn, CacheReads: p.Reads}
	if p.LastUsage != nil {
		s.Context = p.LastUsage.Input
		s.Cache = p.LastUsage.Cached + p.LastUsage.CacheWrite
	}
	if s.Window == 0 {
		s.Window = priceOf(p.Model).Window
	}
	return s
}
