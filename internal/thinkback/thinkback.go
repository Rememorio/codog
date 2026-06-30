package thinkback

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/insights"
	"github.com/Rememorio/codog/internal/session"
)

type Options struct {
	Workspace string
	Year      int
	Limit     int
	Output    string
}

type Report struct {
	Kind     string          `json:"kind"`
	Year     int             `json:"year"`
	Output   string          `json:"output"`
	Written  bool            `json:"written"`
	Insights insights.Report `json:"insights"`
}

func Build(store *session.Store, options Options) (Report, string, error) {
	if store == nil {
		return Report{}, "", fmt.Errorf("session store is required")
	}
	options = normalizeOptions(options)
	summary, err := insights.Build(store, insights.Options{Limit: options.Limit})
	if err != nil {
		return Report{}, "", err
	}
	report := Report{
		Kind:     "think_back",
		Year:     options.Year,
		Output:   resolveOutput(options.Workspace, options.Output, options.Year),
		Insights: summary,
	}
	return report, RenderHTML(report), nil
}

func Write(store *session.Store, options Options) (Report, error) {
	report, page, err := Build(store, options)
	if err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(report.Output) == "" {
		return Report{}, fmt.Errorf("think-back output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(report.Output), 0o755); err != nil {
		return Report{}, err
	}
	if err := os.WriteFile(report.Output, []byte(page), 0o644); err != nil {
		return Report{}, err
	}
	report.Written = true
	return report, nil
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Think Back")
	fmt.Fprintf(w, "  Year             %d\n", report.Year)
	fmt.Fprintf(w, "  Output           %s\n", report.Output)
	fmt.Fprintf(w, "  Written          %t\n", report.Written)
	fmt.Fprintf(w, "  Sessions         %d\n", report.Insights.Sessions)
	fmt.Fprintf(w, "  Prompts          %d\n", report.Insights.Prompts)
	fmt.Fprintf(w, "  Tool uses        %d\n", report.Insights.ToolUses)
	fmt.Fprintf(w, "  Tokens           input=%d output=%d\n", report.Insights.Usage.Input, report.Insights.Usage.Output)
	if len(report.Insights.TopTools) != 0 {
		fmt.Fprintln(w, "  Top tools")
		for _, tool := range report.Insights.TopTools {
			fmt.Fprintf(w, "    %s %d\n", tool.Name, tool.Count)
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

func RenderHTML(report Report) string {
	var tools strings.Builder
	for _, tool := range report.Insights.TopTools {
		tools.WriteString("<li><span>")
		tools.WriteString(html.EscapeString(tool.Name))
		tools.WriteString("</span><strong>")
		tools.WriteString(strconv.Itoa(tool.Count))
		tools.WriteString("</strong></li>")
	}
	if tools.Len() == 0 {
		tools.WriteString("<li><span>No tool calls recorded</span><strong>0</strong></li>")
	}
	var prompts strings.Builder
	for _, prompt := range report.Insights.RecentPrompts {
		prompts.WriteString("<li><small>")
		prompts.WriteString(html.EscapeString(prompt.SessionID))
		prompts.WriteString("#")
		prompts.WriteString(strconv.Itoa(prompt.Index))
		prompts.WriteString("</small><p>")
		prompts.WriteString(html.EscapeString(prompt.Text))
		prompts.WriteString("</p></li>")
	}
	if prompts.Len() == 0 {
		prompts.WriteString("<li><small>No prompts</small><p>Start a Codog session to build your review.</p></li>")
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Codog Think Back %d</title>
<style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
body{margin:0;background:#0f172a;color:#e5e7eb}
main{max-width:980px;margin:0 auto;padding:48px 20px 64px}
h1{font-size:48px;line-height:1;margin:0 0 8px}
p{color:#cbd5e1}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:28px 0}
.metric{border:1px solid #334155;border-radius:8px;padding:16px;background:#111827}
.metric strong{display:block;font-size:30px;color:#f8fafc}
section{margin-top:28px}
ul{list-style:none;margin:0;padding:0;display:grid;gap:10px}
li{border:1px solid #334155;border-radius:8px;padding:14px;background:#111827}
li strong{float:right;color:#93c5fd}
small{color:#94a3b8}
</style>
</head>
<body>
<main>
<h1>Codog Think Back %d</h1>
<p>A local year-in-review snapshot built from saved sessions, prompts, tool calls, and token usage.</p>
<div class="grid">
<div class="metric"><span>Sessions</span><strong>%d</strong></div>
<div class="metric"><span>Prompts</span><strong>%d</strong></div>
<div class="metric"><span>Tool Uses</span><strong>%d</strong></div>
<div class="metric"><span>Tokens</span><strong>%d</strong></div>
</div>
<section><h2>Top Tools</h2><ul>%s</ul></section>
<section><h2>Recent Prompts</h2><ul>%s</ul></section>
</main>
</body>
</html>
`, report.Year, report.Year, report.Insights.Sessions, report.Insights.Prompts, report.Insights.ToolUses, report.Insights.Usage.Input+report.Insights.Usage.Output, tools.String(), prompts.String())
}

func normalizeOptions(options Options) Options {
	if options.Year <= 0 {
		options.Year = time.Now().Year()
	}
	if options.Limit <= 0 {
		options.Limit = 8
	}
	options.Workspace = strings.TrimSpace(options.Workspace)
	options.Output = strings.TrimSpace(options.Output)
	return options
}

func resolveOutput(workspace, output string, year int) string {
	if output == "" {
		output = filepath.Join(".codog", fmt.Sprintf("think-back-%d.html", year))
	}
	if filepath.IsAbs(output) || strings.TrimSpace(workspace) == "" {
		return filepath.Clean(output)
	}
	return filepath.Join(workspace, output)
}
