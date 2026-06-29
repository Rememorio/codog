package usage

import (
	"math"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Summary struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
}

func Estimate(messages []anthropic.Message, model string) Summary {
	var input, output int
	for _, msg := range messages {
		count := estimateMessage(msg)
		if msg.Role == "assistant" {
			output += count
		} else {
			input += count
		}
	}
	total := input + output
	return Summary{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  total,
		EstimatedUSD: math.Round(float64(total)*pricePerToken(model)*100000) / 100000,
	}
}

func estimateMessage(msg anthropic.Message) int {
	var chars int
	for _, block := range msg.Content {
		chars += len(block.Text) + len(block.Content) + len(block.Name) + len(block.Input)
	}
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func pricePerToken(model string) float64 {
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "opus"):
		return 0.000015
	case strings.Contains(name, "haiku"):
		return 0.0000008
	default:
		return 0.000003
	}
}
