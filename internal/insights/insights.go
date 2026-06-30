package insights

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
)

type Options struct {
	Limit int
}

type Report struct {
	Kind              string           `json:"kind"`
	Sessions          int              `json:"sessions"`
	Messages          int              `json:"messages"`
	UserMessages      int              `json:"user_messages"`
	AssistantMessages int              `json:"assistant_messages"`
	Prompts           int              `json:"prompts"`
	ToolUses          int              `json:"tool_uses"`
	Usage             TokenTotals      `json:"usage"`
	TopTools          []ToolCount      `json:"top_tools,omitempty"`
	RecentSessions    []SessionSummary `json:"recent_sessions,omitempty"`
	RecentPrompts     []PromptSummary  `json:"recent_prompts,omitempty"`
}

type TokenTotals struct {
	Input         int `json:"input"`
	Output        int `json:"output"`
	CacheCreation int `json:"cache_creation"`
	CacheRead     int `json:"cache_read"`
}

type ToolCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type SessionSummary struct {
	ID                string      `json:"id"`
	Messages          int         `json:"messages"`
	UserMessages      int         `json:"user_messages"`
	AssistantMessages int         `json:"assistant_messages"`
	Prompts           int         `json:"prompts"`
	ToolUses          int         `json:"tool_uses"`
	Usage             TokenTotals `json:"usage"`
}

type PromptSummary struct {
	SessionID string `json:"session_id"`
	Index     int    `json:"index"`
	Text      string `json:"text"`
}

func Build(store *session.Store, options Options) (Report, error) {
	if store == nil {
		return Report{}, fmt.Errorf("session store is required")
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 5
	}
	sessions, err := store.List()
	if err != nil {
		return Report{}, err
	}
	report := Report{Kind: "insights", Sessions: len(sessions)}
	toolCounts := map[string]int{}
	for _, sess := range sessions {
		summary := summarizeSession(sess)
		prompts, err := store.PromptHistory(sess.ID)
		if err != nil {
			return Report{}, err
		}
		summary.Prompts = len(prompts)
		usages, err := store.Usage(sess.ID)
		if err != nil {
			return Report{}, err
		}
		for _, entry := range usages {
			summary.Usage.Add(entry.Usage)
		}
		report.Messages += summary.Messages
		report.UserMessages += summary.UserMessages
		report.AssistantMessages += summary.AssistantMessages
		report.Prompts += summary.Prompts
		report.ToolUses += summary.ToolUses
		report.Usage.Merge(summary.Usage)
		for _, msg := range sess.Messages {
			for _, block := range msg.Content {
				if block.Type == "tool_use" && strings.TrimSpace(block.Name) != "" {
					toolCounts[block.Name]++
				}
			}
		}
		if len(report.RecentSessions) < limit {
			report.RecentSessions = append(report.RecentSessions, summary)
		}
		appendRecentPrompts(&report.RecentPrompts, prompts, limit)
	}
	report.TopTools = topTools(toolCounts, limit)
	return report, nil
}

func summarizeSession(sess session.Session) SessionSummary {
	summary := SessionSummary{ID: sess.ID, Messages: len(sess.Messages)}
	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			summary.UserMessages++
		case "assistant":
			summary.AssistantMessages++
		}
		for _, block := range msg.Content {
			if block.Type == "tool_use" {
				summary.ToolUses++
			}
		}
	}
	return summary
}

func (t *TokenTotals) Add(usage anthropic.Usage) {
	t.Input += usage.InputTokens
	t.Output += usage.OutputTokens
	t.CacheCreation += usage.CacheCreationInputTokens
	t.CacheRead += usage.CacheReadInputTokens
}

func (t *TokenTotals) Merge(other TokenTotals) {
	t.Input += other.Input
	t.Output += other.Output
	t.CacheCreation += other.CacheCreation
	t.CacheRead += other.CacheRead
}

func appendRecentPrompts(out *[]PromptSummary, prompts []session.PromptEntry, limit int) {
	for i := len(prompts) - 1; i >= 0 && len(*out) < limit; i-- {
		text := strings.TrimSpace(prompts[i].Text)
		if text == "" {
			continue
		}
		*out = append(*out, PromptSummary{
			SessionID: prompts[i].SessionID,
			Index:     prompts[i].Index,
			Text:      preview(text, 120),
		})
	}
}

func topTools(counts map[string]int, limit int) []ToolCount {
	items := make([]ToolCount, 0, len(counts))
	for name, count := range counts {
		items = append(items, ToolCount{Name: name, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Name < items[j].Name
		}
		return items[i].Count > items[j].Count
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Insights")
	fmt.Fprintf(w, "  Sessions         %d\n", report.Sessions)
	fmt.Fprintf(w, "  Messages         %d user=%d assistant=%d\n", report.Messages, report.UserMessages, report.AssistantMessages)
	fmt.Fprintf(w, "  Prompts          %d\n", report.Prompts)
	fmt.Fprintf(w, "  Tool uses        %d\n", report.ToolUses)
	fmt.Fprintf(w, "  Tokens           input=%d output=%d cache_create=%d cache_read=%d\n", report.Usage.Input, report.Usage.Output, report.Usage.CacheCreation, report.Usage.CacheRead)
	if len(report.TopTools) != 0 {
		fmt.Fprintln(w, "  Top tools")
		for _, item := range report.TopTools {
			fmt.Fprintf(w, "    %s %d\n", item.Name, item.Count)
		}
	}
	if len(report.RecentSessions) != 0 {
		fmt.Fprintln(w, "  Recent sessions")
		for _, sess := range report.RecentSessions {
			fmt.Fprintf(w, "    %s messages=%d prompts=%d tools=%d\n", sess.ID, sess.Messages, sess.Prompts, sess.ToolUses)
		}
	}
	if len(report.RecentPrompts) != 0 {
		fmt.Fprintln(w, "  Recent prompts")
		for _, prompt := range report.RecentPrompts {
			fmt.Fprintf(w, "    %s#%d %s\n", prompt.SessionID, prompt.Index, prompt.Text)
		}
	}
}

func RenderJSON(w io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

func preview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 1 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-1]) + "..."
}
