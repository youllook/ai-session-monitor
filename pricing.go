package main

import "strings"

// Price 是 API 牌價（US$／百萬 token）。快取寫入：5 分鐘＝輸入 1.25 倍、1 小時＝2 倍。
type Price struct {
	Input  float64
	Output float64
	Read   float64 // 快取讀取，各代不同
	Window int     // 上下文上限（token）
}

// 依模型 id 子字串比對，第一個符合的算數；新模型上市改這裡。2026-10-14 查價。
var prices = []struct {
	id string
	p  Price
}{
	{"gpt-6.1-sol", Price{2, 10, 0.1, 1_050_000}},
	{"gpt-6-astra", Price{10, 50, 1, 1_050_000}},
	{"gpt-6-luna", Price{0.1, 0.5, 0.01, 1_050_000}},
	{"gpt-5.6-sol", Price{4, 20, 0.4, 1_050_000}},
	{"claude-fable-5-1", Price{10, 50, 0.25, 1_000_000}},
	{"claude-mythos-5-1", Price{10, 50, 0.25, 1_000_000}},
	{"claude-fable", Price{10, 50, 1, 1_000_000}},
	{"claude-mythos", Price{10, 50, 1, 1_000_000}},
	{"claude-opus-5-5", Price{4, 20, 0.2, 1_000_000}},
	{"claude-opus-5", Price{5, 25, 0.5, 1_000_000}},
	{"claude-opus-4-8", Price{5, 25, 0.5, 1_000_000}},
	{"claude-opus-4-7", Price{5, 25, 0.5, 1_000_000}},
	{"claude-opus-4-6", Price{5, 25, 0.5, 1_000_000}},
	{"claude-opus", Price{5, 25, 0.5, 200_000}},
	{"claude-sonnet-5", Price{2, 10, 0.2, 1_000_000}},
	{"claude-sonnet-4-6", Price{3, 15, 0.3, 1_000_000}},
	{"claude-sonnet", Price{3, 15, 0.3, 200_000}},
	{"claude-haiku-5", Price{0.1, 0.5, 0.01, 1_000_000}},
	{"claude-haiku", Price{1, 5, 0.1, 200_000}},
}

// 認不出的模型先照 Opus 5.5 算
var fallbackPrice = Price{4, 20, 0.2, 1_000_000}

func priceOf(model string) Price {
	for _, e := range prices {
		if strings.Contains(model, e.id) {
			return e.p
		}
	}
	return fallbackPrice
}

// Usage 是一次請求的 token 數（API 的拼法）
type Usage struct {
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CacheRead   int64 `json:"cache_read_input_tokens"`
	CacheCreate int64 `json:"cache_creation_input_tokens"`
	CacheBreak  *struct {
		Write5m int64 `json:"ephemeral_5m_input_tokens"`
		Write1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// Prompt 是這次請求送出的整段提示＝目前對話長度
func (u Usage) Prompt() int64 { return u.Input + u.CacheRead + u.CacheCreate }

// Cost 依牌價換算一次請求；快取寫入有 5m／1h 細分就照細分算，沒有就當 5m
func (u Usage) Cost(model string) float64 {
	p := priceOf(model)
	w5, w1 := u.CacheCreate, int64(0)
	if u.CacheBreak != nil && u.CacheBreak.Write5m+u.CacheBreak.Write1h > 0 {
		w5, w1 = u.CacheBreak.Write5m, u.CacheBreak.Write1h
	}
	tok := float64(u.Input)*p.Input +
		float64(w5)*p.Input*1.25 +
		float64(w1)*p.Input*2 +
		float64(u.CacheRead)*p.Read +
		float64(u.Output)*p.Output
	return tok / 1e6
}

// TTLSeconds：這次有寫 1 小時快取就算 1 小時，否則 5 分鐘
func (u Usage) TTLSeconds() int64 {
	if u.CacheBreak != nil && u.CacheBreak.Write1h > 0 {
		return 3600
	}
	return 300
}
