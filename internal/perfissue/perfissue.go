package perfissue

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/insights"
)

type Options struct {
	Workspace      string
	Limit          int
	TokenThreshold int
	ToolThreshold  int
	CreatedAt      time.Time
}

type Signal struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

type Report struct {
	Kind           string          `json:"kind"`
	Action         string          `json:"action"`
	Status         string          `json:"status"`
	Workspace      string          `json:"workspace,omitempty"`
	CreatedAt      string          `json:"created_at"`
	TokenThreshold int             `json:"token_threshold"`
	ToolThreshold  int             `json:"tool_threshold"`
	TotalTokens    int             `json:"total_tokens"`
	Signals        []Signal        `json:"signals,omitempty"`
	Insights       insights.Report `json:"insights"`
	File           string          `json:"file,omitempty"`
	Bytes          int             `json:"bytes,omitempty"`
}

func Build(source insights.Report, options Options) Report {
	createdAt := options.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	tokenThreshold := options.TokenThreshold
	if tokenThreshold <= 0 {
		tokenThreshold = 100000
	}
	toolThreshold := options.ToolThreshold
	if toolThreshold <= 0 {
		toolThreshold = 50
	}
	totalTokens := source.Usage.Input + source.Usage.Output + source.Usage.CacheCreation + source.Usage.CacheRead
	report := Report{
		Kind:           "perf_issue",
		Action:         "diagnose",
		Status:         "ok",
		Workspace:      strings.TrimSpace(options.Workspace),
		CreatedAt:      createdAt.Format(time.RFC3339),
		TokenThreshold: tokenThreshold,
		ToolThreshold:  toolThreshold,
		TotalTokens:    totalTokens,
		Insights:       source,
	}
	report.Signals = performanceSignals(source, totalTokens, tokenThreshold, toolThreshold)
	if source.Sessions == 0 {
		report.Status = "empty"
	} else if hasWarning(report.Signals) {
		report.Status = "warn"
	}
	return report
}

func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Performance Issue")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Workspace != "" {
		fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	}
	fmt.Fprintf(out, "  Sessions         %d\n", report.Insights.Sessions)
	fmt.Fprintf(out, "  Messages         %d\n", report.Insights.Messages)
	fmt.Fprintf(out, "  Prompts          %d\n", report.Insights.Prompts)
	fmt.Fprintf(out, "  Tool uses        %d\n", report.Insights.ToolUses)
	fmt.Fprintf(out, "  Tokens           total=%d input=%d output=%d cache_create=%d cache_read=%d\n",
		report.TotalTokens,
		report.Insights.Usage.Input,
		report.Insights.Usage.Output,
		report.Insights.Usage.CacheCreation,
		report.Insights.Usage.CacheRead,
	)
	if report.File != "" {
		fmt.Fprintf(out, "  File             %s\n", report.File)
		fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	}
	if len(report.Signals) != 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Signals")
		for _, signal := range report.Signals {
			fmt.Fprintf(out, "  - [%s] %s: %s\n", signal.Severity, signal.Kind, signal.Message)
		}
	}
	if len(report.Insights.TopTools) != 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Top tools")
		for _, tool := range report.Insights.TopTools {
			fmt.Fprintf(out, "  %s %d\n", tool.Name, tool.Count)
		}
	}
	if len(report.Insights.RecentSessions) != 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Recent sessions")
		for _, sess := range report.Insights.RecentSessions {
			sessionTokens := sess.Usage.Input + sess.Usage.Output + sess.Usage.CacheCreation + sess.Usage.CacheRead
			fmt.Fprintf(out, "  %s messages=%d prompts=%d tools=%d tokens=%d\n", sess.ID, sess.Messages, sess.Prompts, sess.ToolUses, sessionTokens)
		}
	}
}

func RenderMarkdown(report Report) string {
	var builder strings.Builder
	builder.WriteString("# Codog Performance Issue\n\n")
	builder.WriteString(fmt.Sprintf("- Created: %s\n", report.CreatedAt))
	if report.Workspace != "" {
		builder.WriteString(fmt.Sprintf("- Workspace: %s\n", report.Workspace))
	}
	builder.WriteString(fmt.Sprintf("- Status: %s\n", report.Status))
	builder.WriteString(fmt.Sprintf("- Sessions: %d\n", report.Insights.Sessions))
	builder.WriteString(fmt.Sprintf("- Messages: %d\n", report.Insights.Messages))
	builder.WriteString(fmt.Sprintf("- Prompts: %d\n", report.Insights.Prompts))
	builder.WriteString(fmt.Sprintf("- Tool uses: %d\n", report.Insights.ToolUses))
	builder.WriteString(fmt.Sprintf("- Tokens: total=%d input=%d output=%d cache_create=%d cache_read=%d\n",
		report.TotalTokens,
		report.Insights.Usage.Input,
		report.Insights.Usage.Output,
		report.Insights.Usage.CacheCreation,
		report.Insights.Usage.CacheRead,
	))
	builder.WriteString("\n## Signals\n\n")
	if len(report.Signals) == 0 {
		builder.WriteString("No performance signals crossed the configured thresholds.\n")
	} else {
		for _, signal := range report.Signals {
			builder.WriteString(fmt.Sprintf("- **%s / %s**: %s\n", signal.Severity, signal.Kind, signal.Message))
		}
	}
	if len(report.Insights.TopTools) != 0 {
		builder.WriteString("\n## Top Tools\n\n")
		for _, tool := range report.Insights.TopTools {
			builder.WriteString(fmt.Sprintf("- `%s`: %d\n", tool.Name, tool.Count))
		}
	}
	if len(report.Insights.RecentSessions) != 0 {
		builder.WriteString("\n## Recent Sessions\n\n")
		for _, sess := range report.Insights.RecentSessions {
			sessionTokens := sess.Usage.Input + sess.Usage.Output + sess.Usage.CacheCreation + sess.Usage.CacheRead
			builder.WriteString(fmt.Sprintf("- `%s`: messages=%d prompts=%d tools=%d tokens=%d\n", sess.ID, sess.Messages, sess.Prompts, sess.ToolUses, sessionTokens))
		}
	}
	builder.WriteString("\n## Raw Data\n\n```json\n")
	data, _ := json.MarshalIndent(report.Insights, "", "  ")
	builder.Write(data)
	builder.WriteString("\n```\n")
	return builder.String()
}

func RenderJSON(out io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(data))
	return nil
}

func performanceSignals(report insights.Report, totalTokens int, tokenThreshold int, toolThreshold int) []Signal {
	signals := []Signal{}
	if report.Sessions == 0 {
		return append(signals, Signal{Severity: "info", Kind: "no_sessions", Message: "no saved sessions are available for performance diagnosis"})
	}
	if totalTokens == 0 {
		signals = append(signals, Signal{Severity: "warn", Kind: "missing_usage", Message: "sessions exist but no token usage records were found"})
	}
	if totalTokens >= tokenThreshold {
		signals = append(signals, Signal{Severity: "warn", Kind: "high_token_usage", Message: fmt.Sprintf("total token usage %d reached threshold %d", totalTokens, tokenThreshold)})
	}
	if report.ToolUses >= toolThreshold {
		signals = append(signals, Signal{Severity: "warn", Kind: "high_tool_use", Message: fmt.Sprintf("tool use count %d reached threshold %d", report.ToolUses, toolThreshold)})
	}
	if report.Usage.CacheCreation > 0 && report.Usage.CacheRead == 0 {
		signals = append(signals, Signal{Severity: "info", Kind: "cache_not_reused", Message: "cache creation tokens were recorded but no cache read tokens were observed"})
	}
	if len(signals) == 0 {
		signals = append(signals, Signal{Severity: "info", Kind: "within_thresholds", Message: "no performance thresholds were exceeded"})
	}
	return signals
}

func hasWarning(signals []Signal) bool {
	for _, signal := range signals {
		if signal.Severity == "warn" || signal.Severity == "error" {
			return true
		}
	}
	return false
}
