package main

import (
	"fmt"
	"strconv"
	"strings"
)

// 212.3K、1M、1.2M
func tokensK(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

func usd(n float64) string {
	if n < 10 {
		return fmt.Sprintf("$%.2f", n)
	}
	return fmt.Sprintf("$%.1f", n)
}
