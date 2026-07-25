package agent

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/claudemigrate"
)

type claudeImportRequest struct {
	Action      string
	SourceHome  string
	MaxSessions int
	MaxAge      time.Duration
	Format      string
}

// ClaudeImport inspects or imports compatible local Claude Code assets.
func (a *App) ClaudeImport(args []string) error {
	req, err := parseClaudeImportArgs(args)
	if err != nil {
		return err
	}
	report, err := claudemigrate.Run(claudemigrate.Options{
		SourceHome:   req.SourceHome,
		Workspace:    a.Workspace,
		SessionStore: a.Sessions,
		MaxSessions:  req.MaxSessions,
		MaxAge:       req.MaxAge,
		Apply:        req.Action == "run",
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderClaudeImportText(a.Out, report)
	return nil
}

func parseClaudeImportArgs(args []string) (claudeImportRequest, error) {
	const usage = "codog import [status|run] [--source DIR] [--max-sessions N|--all] [--max-age DAYS] [--output-format text|json]"
	req := claudeImportRequest{
		Action:      "status",
		MaxSessions: claudemigrate.DefaultMaxSessions,
		MaxAge:      claudemigrate.DefaultMaxAge,
		Format:      "text",
	}
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var sourceAlias string
	var maxAgeDays string
	var all bool
	var jsonOutput bool
	var outputFormat string
	positionals := []string{}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positionals = append(positionals, args[0])
		args = args[1:]
	}
	flags.StringVar(&req.SourceHome, "source", "", "")
	flags.StringVar(&sourceAlias, "claude-home", "", "")
	flags.IntVar(&req.MaxSessions, "max-sessions", req.MaxSessions, "")
	flags.StringVar(&maxAgeDays, "max-age", "30", "")
	flags.BoolVar(&all, "all", false, "")
	flags.BoolVar(&jsonOutput, "json", false, "")
	flags.StringVar(&outputFormat, "output-format", "", "")
	flags.StringVar(&outputFormat, "o", "", "")
	if err := flags.Parse(args); err != nil {
		return req, fmt.Errorf("import: %w\nUsage: %s", err, usage)
	}
	positionals = append(positionals, flags.Args()...)
	if sourceAlias != "" {
		req.SourceHome = sourceAlias
	}
	if req.MaxSessions < 0 {
		return req, invalidFlagValueError{Flag: "--max-sessions", Value: fmt.Sprint(req.MaxSessions), Message: "expected a non-negative integer", Usage: usage}
	}
	age, err := claudemigrate.ParseMaxAge(maxAgeDays)
	if err != nil {
		return req, invalidFlagValueError{Flag: "--max-age", Value: maxAgeDays, Message: err.Error(), Usage: usage}
	}
	req.MaxAge = age
	if all {
		req.MaxSessions = int(^uint(0) >> 1)
		req.MaxAge = time.Duration(1<<63 - 1)
	}
	if jsonOutput {
		req.Format = "json"
	}
	if outputFormat != "" {
		format, err := normalizeOutputFormat("import", outputFormat, []string{"text", "json"})
		if err != nil {
			return req, err
		}
		req.Format = format
	}
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{Command: "import", Args: positionals[1:], Usage: usage}
	}
	if len(positionals) == 1 {
		switch strings.ToLower(strings.TrimSpace(positionals[0])) {
		case "", "status", "show", "inspect", "detect":
			req.Action = "status"
		case "run", "apply", "migrate":
			req.Action = "run"
		default:
			return req, invalidFlagValueError{
				Flag:    "action",
				Value:   positionals[0],
				Message: "expected status or run",
				Usage:   usage,
			}
		}
	}
	return req, nil
}

func renderClaudeImportText(out io.Writer, report claudemigrate.Report) {
	fmt.Fprintln(out, "Claude Code migration")
	fmt.Fprintf(out, "  Status       %s\n", report.Status)
	fmt.Fprintf(out, "  Source       %s\n", report.SourceHome)
	fmt.Fprintf(out, "  Workspace    %s\n", report.Workspace)
	fmt.Fprintln(out, "  Compatible assets")
	for _, asset := range report.Assets {
		fmt.Fprintf(out, "    %-14s %d (%s)\n", asset.Kind, asset.Count, asset.Mode)
	}
	fmt.Fprintln(out, "  Sessions")
	fmt.Fprintf(out, "    Discovered   %d\n", report.SessionsDiscovered)
	fmt.Fprintf(out, "    Eligible     %d\n", report.SessionsEligible)
	fmt.Fprintf(out, "    Imported     %d\n", report.SessionsImported)
	fmt.Fprintf(out, "    Skipped      %d\n", report.SessionsSkipped)
	fmt.Fprintf(out, "    Failed       %d\n", report.SessionsFailed)
	for _, item := range report.Sessions {
		label := item.SessionID
		if label == "" {
			label = item.Source
		}
		fmt.Fprintf(out, "    %-12s %s", item.Status, label)
		if item.Reason != "" {
			fmt.Fprintf(out, " (%s)", item.Reason)
		}
		fmt.Fprintln(out)
	}
	for _, note := range report.Notes {
		fmt.Fprintln(out, "  "+note)
	}
	if report.Action == "status" && report.Status == "ready" {
		fmt.Fprintln(out, "  Next         codog import run")
	}
}
