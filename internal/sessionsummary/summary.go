package sessionsummary

import (
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/usage"
)

const previewLimit = 160

type Preview struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type Report struct {
	Kind              string             `json:"kind"`
	Action            string             `json:"action"`
	Status            string             `json:"status"`
	SessionID         string             `json:"session_id"`
	Path              string             `json:"path,omitempty"`
	Model             string             `json:"model"`
	MessageCount      int                `json:"message_count"`
	UserMessages      int                `json:"user_messages"`
	AssistantMessages int                `json:"assistant_messages"`
	ToolUses          int                `json:"tool_uses"`
	ToolResults       int                `json:"tool_results"`
	ToolErrors        int                `json:"tool_errors"`
	TokenEstimate     usage.Summary      `json:"token_estimate"`
	FirstUser         *Preview           `json:"first_user,omitempty"`
	LastUser          *Preview           `json:"last_user,omitempty"`
	LastAssistant     *Preview           `json:"last_assistant,omitempty"`
	Roles             []usage.RoleUsage  `json:"roles,omitempty"`
	Blocks            []usage.BlockUsage `json:"blocks,omitempty"`
}

func Build(sessionID string, path string, model string, messages []anthropic.Message) Report {
	usageReport := usage.BuildReport(sessionID, model, messages)
	report := Report{
		Kind:          "summary",
		Action:        "show",
		Status:        "ok",
		SessionID:     sessionID,
		Path:          path,
		Model:         model,
		MessageCount:  len(messages),
		TokenEstimate: usageReport.Summary,
		ToolUses:      usageReport.ToolUse.ToolUses,
		ToolResults:   usageReport.ToolUse.ToolResults,
		ToolErrors:    usageReport.ToolUse.Errors,
		Roles:         usageReport.Roles,
		Blocks:        usageReport.Blocks,
	}
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			report.UserMessages++
			preview := previewMessage(msg)
			if preview != nil {
				if report.FirstUser == nil {
					first := *preview
					report.FirstUser = &first
				}
				last := *preview
				report.LastUser = &last
			}
		case "assistant":
			report.AssistantMessages++
			if preview := previewMessage(msg); preview != nil {
				last := *preview
				report.LastAssistant = &last
			}
		}
	}
	return report
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Summary")
	fmt.Fprintf(w, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(w, "  Model            %s\n", valueOrNone(report.Model))
	fmt.Fprintf(w, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(w, "  User messages    %d\n", report.UserMessages)
	fmt.Fprintf(w, "  Assistant msgs   %d\n", report.AssistantMessages)
	fmt.Fprintf(w, "  Tokens           %d\n", report.TokenEstimate.TotalTokens)
	fmt.Fprintf(w, "  Tool use         calls=%d results=%d errors=%d\n", report.ToolUses, report.ToolResults, report.ToolErrors)
	if report.FirstUser != nil {
		fmt.Fprintf(w, "  First user       %s\n", report.FirstUser.Text)
	}
	if report.LastUser != nil {
		fmt.Fprintf(w, "  Last user        %s\n", report.LastUser.Text)
	}
	if report.LastAssistant != nil {
		fmt.Fprintf(w, "  Last assistant   %s\n", report.LastAssistant.Text)
	}
	if report.Path != "" {
		fmt.Fprintf(w, "  Path             %s\n", report.Path)
	}
}

func previewMessage(msg anthropic.Message) *Preview {
	var parts []string
	for _, block := range msg.Content {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			text = strings.TrimSpace(block.Content)
		}
		if text == "" && block.Type == "tool_use" {
			text = "tool_use:" + block.Name
		}
		if text == "" {
			continue
		}
		parts = append(parts, oneLine(text))
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	if text == "" {
		return nil
	}
	return &Preview{Role: msg.Role, Text: truncate(text, previewLimit)}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
