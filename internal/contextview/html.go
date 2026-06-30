package contextview

import (
	"bytes"
	"fmt"
	"html"
	"strings"
)

func RenderHTML(report Report) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "<!doctype html>")
	fmt.Fprintln(&out, `<html lang="en">`)
	fmt.Fprintln(&out, "<head>")
	fmt.Fprintln(&out, `<meta charset="utf-8">`)
	fmt.Fprintln(&out, `<meta name="viewport" content="width=device-width, initial-scale=1">`)
	fmt.Fprintf(&out, "<title>Codog Context %s</title>\n", html.EscapeString(report.Workspace.Name))
	fmt.Fprintln(&out, `<style>`)
	fmt.Fprintln(&out, `body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f8fafc;color:#102033}`)
	fmt.Fprintln(&out, `main{max-width:1100px;margin:0 auto;padding:32px 20px}`)
	fmt.Fprintln(&out, `header{border-bottom:1px solid #d9e2ec;margin-bottom:24px;padding-bottom:16px}`)
	fmt.Fprintln(&out, `h1{font-size:28px;margin:0 0 8px}`)
	fmt.Fprintln(&out, `h2{font-size:16px;margin:0 0 12px}`)
	fmt.Fprintln(&out, `.meta{color:#52657a;font-size:14px}`)
	fmt.Fprintln(&out, `.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:14px}`)
	fmt.Fprintln(&out, `.panel{border:1px solid #d9e2ec;background:white;border-radius:8px;padding:16px;margin-bottom:16px}`)
	fmt.Fprintln(&out, `.metric{font-size:24px;font-weight:650}`)
	fmt.Fprintln(&out, `.label{font-size:12px;text-transform:uppercase;letter-spacing:0;color:#52657a}`)
	fmt.Fprintln(&out, `ul{margin:0;padding-left:20px}`)
	fmt.Fprintln(&out, `code{background:#eef3f8;border-radius:4px;padding:2px 4px}`)
	fmt.Fprintln(&out, `</style>`)
	fmt.Fprintln(&out, "</head>")
	fmt.Fprintln(&out, "<body><main>")
	fmt.Fprintln(&out, "<header>")
	fmt.Fprintln(&out, "<h1>Codog Context</h1>")
	fmt.Fprintf(&out, `<div class="meta">%s - status %s</div>`+"\n", html.EscapeString(report.Workspace.Path), html.EscapeString(report.Status))
	fmt.Fprintln(&out, "</header>")
	fmt.Fprintln(&out, `<section class="grid">`)
	metric(&out, "Messages", report.Session.MessageCount)
	metric(&out, "Tools", report.Tools.Count)
	metric(&out, "Memory files", report.Memory.InstructionFiles)
	metric(&out, "Focused paths", report.Focus.FocusedPaths)
	metric(&out, "Estimated tokens", report.Prompt.EstimatedTokens)
	fmt.Fprintln(&out, "</section>")
	panel(&out, "Config", []string{
		"Model: " + valueOrNone(report.Config.Model),
		"Permission: " + valueOrNone(report.Config.PermissionMode),
		fmt.Sprintf("Max turns: %d", report.Config.MaxTurns),
		fmt.Sprintf("Max tokens: %d", report.Config.MaxTokens),
	})
	panel(&out, "Git", []string{
		"Available: " + fmt.Sprint(report.Git.Available),
		"Branch: " + valueOrNone(report.Git.Branch),
		"Clean: " + fmt.Sprint(report.Git.Clean),
		fmt.Sprintf("Staged: %d, unstaged: %d, untracked: %d, conflicts: %d", report.Git.Staged, report.Git.Unstaged, report.Git.Untracked, report.Git.Conflicts),
	})
	panel(&out, "Session", []string{
		"Active: " + fmt.Sprint(report.Session.Active),
		"ID: " + valueOrNone(report.Session.ID),
		fmt.Sprintf("Messages: %d", report.Session.MessageCount),
	})
	panel(&out, "Tokens", []string{
		fmt.Sprintf("Input: %d", report.TokenEstimate.InputTokens),
		fmt.Sprintf("Output: %d", report.TokenEstimate.OutputTokens),
		fmt.Sprintf("Total: %d", report.TokenEstimate.TotalTokens),
		fmt.Sprintf("Estimated USD: %.5f", report.TokenEstimate.EstimatedUSD),
	})
	panel(&out, "Signals", nonEmpty(report.Signals, "none"))
	fmt.Fprintln(&out, "</main></body></html>")
	return out.String()
}

func metric(out *bytes.Buffer, label string, value int) {
	fmt.Fprintln(out, `<div class="panel">`)
	fmt.Fprintf(out, `<div class="label">%s</div>`+"\n", html.EscapeString(label))
	fmt.Fprintf(out, `<div class="metric">%d</div>`+"\n", value)
	fmt.Fprintln(out, "</div>")
}

func panel(out *bytes.Buffer, title string, items []string) {
	fmt.Fprintln(out, `<section class="panel">`)
	fmt.Fprintf(out, "<h2>%s</h2>\n", html.EscapeString(title))
	fmt.Fprintln(out, "<ul>")
	for _, item := range items {
		fmt.Fprintf(out, "<li>%s</li>\n", html.EscapeString(item))
	}
	fmt.Fprintln(out, "</ul>")
	fmt.Fprintln(out, "</section>")
}

func nonEmpty(values []string, fallback string) []string {
	out := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return []string{fallback}
	}
	return out
}
