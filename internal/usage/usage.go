package usage

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Report struct {
	Kind         string         `json:"kind"`
	Action       string         `json:"action"`
	Status       string         `json:"status"`
	SessionID    string         `json:"session_id,omitempty"`
	Model        string         `json:"model"`
	MessageCount int            `json:"message_count"`
	Summary      Summary        `json:"summary"`
	Roles        []RoleUsage    `json:"roles"`
	Blocks       []BlockUsage   `json:"blocks"`
	ToolUse      ToolUseSummary `json:"tool_use"`
}

type Summary struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
}

type RoleUsage struct {
	Role     string `json:"role"`
	Messages int    `json:"messages"`
	Tokens   int    `json:"tokens"`
}

type BlockUsage struct {
	Type   string `json:"type"`
	Count  int    `json:"count"`
	Tokens int    `json:"tokens"`
}

type ToolUseSummary struct {
	ToolUses    int `json:"tool_uses"`
	ToolResults int `json:"tool_results"`
	Errors      int `json:"errors"`
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

func BuildReport(sessionID string, model string, messages []anthropic.Message) Report {
	roleIndex := map[string]*RoleUsage{}
	blockIndex := map[string]*BlockUsage{}
	toolUse := ToolUseSummary{}
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		if roleIndex[role] == nil {
			roleIndex[role] = &RoleUsage{Role: role}
		}
		roleIndex[role].Messages++
		roleIndex[role].Tokens += estimateMessage(msg)
		for _, block := range msg.Content {
			blockType := strings.TrimSpace(block.Type)
			if blockType == "" {
				blockType = "unknown"
			}
			if blockIndex[blockType] == nil {
				blockIndex[blockType] = &BlockUsage{Type: blockType}
			}
			blockIndex[blockType].Count++
			blockIndex[blockType].Tokens += estimateBlock(block)
			switch blockType {
			case "tool_use":
				toolUse.ToolUses++
			case "tool_result":
				toolUse.ToolResults++
				if block.IsError {
					toolUse.Errors++
				}
			}
		}
	}
	return Report{
		Kind:         "usage",
		Action:       "show",
		Status:       "ok",
		SessionID:    sessionID,
		Model:        model,
		MessageCount: len(messages),
		Summary:      Estimate(messages, model),
		Roles:        sortedRoles(roleIndex),
		Blocks:       sortedBlocks(blockIndex),
		ToolUse:      toolUse,
	}
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Usage")
	if report.SessionID != "" {
		fmt.Fprintf(w, "  Session          %s\n", report.SessionID)
	}
	fmt.Fprintf(w, "  Model            %s\n", valueOrNone(report.Model))
	fmt.Fprintf(w, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(w, "  Input tokens     %d\n", report.Summary.InputTokens)
	fmt.Fprintf(w, "  Output tokens    %d\n", report.Summary.OutputTokens)
	fmt.Fprintf(w, "  Total tokens     %d\n", report.Summary.TotalTokens)
	fmt.Fprintf(w, "  Estimated USD    %.5f\n", report.Summary.EstimatedUSD)
	fmt.Fprintf(w, "  Tool use         calls=%d results=%d errors=%d\n", report.ToolUse.ToolUses, report.ToolUse.ToolResults, report.ToolUse.Errors)
	if len(report.Roles) != 0 {
		fmt.Fprintln(w, "Roles")
		for _, role := range report.Roles {
			fmt.Fprintf(w, "  %s\tmessages=%d tokens=%d\n", role.Role, role.Messages, role.Tokens)
		}
	}
	if len(report.Blocks) != 0 {
		fmt.Fprintln(w, "Blocks")
		for _, block := range report.Blocks {
			fmt.Fprintf(w, "  %s\tcount=%d tokens=%d\n", block.Type, block.Count, block.Tokens)
		}
	}
}

func estimateMessage(msg anthropic.Message) int {
	var chars int
	for _, block := range msg.Content {
		chars += blockChars(block)
	}
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func estimateBlock(block anthropic.ContentBlock) int {
	chars := blockChars(block)
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func blockChars(block anthropic.ContentBlock) int {
	return len(block.Text) + len(block.Content) + len(block.Name) + len(block.Input)
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

func sortedRoles(index map[string]*RoleUsage) []RoleUsage {
	roles := make([]RoleUsage, 0, len(index))
	for _, role := range index {
		roles = append(roles, *role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Role < roles[j].Role })
	return roles
}

func sortedBlocks(index map[string]*BlockUsage) []BlockUsage {
	blocks := make([]BlockUsage, 0, len(index))
	for _, block := range index {
		blocks = append(blocks, *block)
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Type < blocks[j].Type })
	return blocks
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
