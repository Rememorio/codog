package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
)

const (
	ExportMarkdown = "markdown"
	ExportJSON     = "json"
	ExportJSONL    = "jsonl"
	ExportHTML     = "html"
)

type ExportedSession struct {
	ID       string              `json:"id"`
	Identity SessionIdentity     `json:"identity"`
	Messages []anthropic.Message `json:"messages"`
}

func (s *Store) Export(id string, format string) ([]byte, *Session, error) {
	if strings.TrimSpace(id) == "" {
		id = "latest"
	}
	format, err := NormalizeExportFormat(format)
	if err != nil {
		return nil, nil, err
	}
	sess, err := s.Open(id)
	if err != nil {
		return nil, nil, err
	}
	switch format {
	case ExportMarkdown:
		return []byte(RenderMarkdown(sess)), sess, nil
	case ExportJSON:
		data, err := json.MarshalIndent(ExportedSession{ID: sess.ID, Identity: sess.Identity, Messages: sess.Messages}, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return append(data, '\n'), sess, nil
	case ExportJSONL:
		data, err := os.ReadFile(sess.Path)
		if err != nil {
			return nil, nil, err
		}
		return data, sess, nil
	case ExportHTML:
		return []byte(RenderHTML(sess)), sess, nil
	default:
		return nil, nil, fmt.Errorf("unsupported export format %q", format)
	}
}

func NormalizeExportFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "md", "markdown", "text":
		return ExportMarkdown, nil
	case "json":
		return ExportJSON, nil
	case "jsonl", "raw":
		return ExportJSONL, nil
	case "html", "htm":
		return ExportHTML, nil
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}

func RenderMarkdown(sess *Session) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "# Conversation Export")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "- **Session**: `%s`\n", sess.ID)
	if strings.TrimSpace(sess.Identity.Title) != "" {
		fmt.Fprintf(&out, "- **Title**: %s\n", sess.Identity.Title)
	}
	if strings.TrimSpace(sess.Identity.Workspace) != "" {
		fmt.Fprintf(&out, "- **Workspace**: `%s`\n", sess.Identity.Workspace)
	}
	if strings.TrimSpace(sess.Identity.Purpose) != "" {
		fmt.Fprintf(&out, "- **Purpose**: `%s`\n", sess.Identity.Purpose)
	}
	fmt.Fprintf(&out, "- **Messages**: %d\n", len(sess.Messages))
	for index, msg := range sess.Messages {
		fmt.Fprintln(&out)
		fmt.Fprintf(&out, "## %d. %s\n\n", index+1, msg.Role)
		for _, block := range msg.Content {
			renderBlockMarkdown(&out, block)
		}
	}
	return out.String()
}

func RenderHTML(sess *Session) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "<!doctype html>")
	fmt.Fprintln(&out, `<html lang="en">`)
	fmt.Fprintln(&out, "<head>")
	fmt.Fprintln(&out, `<meta charset="utf-8">`)
	fmt.Fprintln(&out, `<meta name="viewport" content="width=device-width, initial-scale=1">`)
	fmt.Fprintf(&out, "<title>Codog Session %s</title>\n", html.EscapeString(sess.ID))
	fmt.Fprintln(&out, `<style>`)
	fmt.Fprintln(&out, `body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f8fafc;color:#0f172a}`)
	fmt.Fprintln(&out, `main{max-width:920px;margin:0 auto;padding:32px 20px}`)
	fmt.Fprintln(&out, `header{border-bottom:1px solid #dbe3ef;margin-bottom:24px;padding-bottom:16px}`)
	fmt.Fprintln(&out, `h1{font-size:26px;margin:0 0 8px}`)
	fmt.Fprintln(&out, `.meta{color:#475569;font-size:14px}`)
	fmt.Fprintln(&out, `.message{border:1px solid #dbe3ef;background:white;border-radius:8px;margin:16px 0;padding:16px}`)
	fmt.Fprintln(&out, `.role{font-weight:650;text-transform:uppercase;letter-spacing:0;font-size:12px;color:#475569;margin-bottom:12px}`)
	fmt.Fprintln(&out, `pre{white-space:pre-wrap;word-break:break-word;background:#0f172a;color:#e2e8f0;border-radius:6px;padding:12px;overflow:auto}`)
	fmt.Fprintln(&out, `.text{white-space:pre-wrap;line-height:1.55}`)
	fmt.Fprintln(&out, `</style>`)
	fmt.Fprintln(&out, "</head>")
	fmt.Fprintln(&out, "<body>")
	fmt.Fprintln(&out, "<main>")
	fmt.Fprintln(&out, "<header>")
	title := strings.TrimSpace(sess.Identity.Title)
	if title == "" {
		title = "Conversation Export"
	}
	fmt.Fprintf(&out, "<h1>%s</h1>\n", html.EscapeString(title))
	fmt.Fprintf(&out, `<div class="meta">Session %s - %d messages</div>`+"\n", html.EscapeString(sess.ID), len(sess.Messages))
	if strings.TrimSpace(sess.Identity.Workspace) != "" {
		fmt.Fprintf(&out, `<div class="meta">Workspace %s</div>`+"\n", html.EscapeString(sess.Identity.Workspace))
	}
	if strings.TrimSpace(sess.Identity.Purpose) != "" {
		fmt.Fprintf(&out, `<div class="meta">Purpose %s</div>`+"\n", html.EscapeString(sess.Identity.Purpose))
	}
	fmt.Fprintln(&out, "</header>")
	for index, msg := range sess.Messages {
		fmt.Fprintln(&out, `<section class="message">`)
		fmt.Fprintf(&out, `<div class="role">%d. %s</div>`+"\n", index+1, html.EscapeString(msg.Role))
		for _, block := range msg.Content {
			renderBlockHTML(&out, block)
		}
		fmt.Fprintln(&out, "</section>")
	}
	fmt.Fprintln(&out, "</main>")
	fmt.Fprintln(&out, "</body>")
	fmt.Fprintln(&out, "</html>")
	return out.String()
}

func DefaultExportFilename(sess *Session) string {
	return DefaultExportFilenameForFormat(sess, ExportMarkdown)
}

func DefaultExportFilenameForFormat(sess *Session, format string) string {
	normalized, err := NormalizeExportFormat(format)
	if err != nil {
		normalized = ExportMarkdown
	}
	stem := "conversation"
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		for _, block := range msg.Content {
			if strings.TrimSpace(block.Text) != "" {
				stem = strings.TrimSpace(strings.Split(block.Text, "\n")[0])
				return safeExportStem(stem) + exportExtension(normalized)
			}
		}
	}
	return stem + exportExtension(normalized)
}

func renderBlockMarkdown(out *bytes.Buffer, block anthropic.ContentBlock) {
	switch block.Type {
	case "text":
		if block.Text != "" {
			fmt.Fprintln(out, block.Text)
			fmt.Fprintln(out)
		}
	case "tool_use":
		input := strings.TrimSpace(string(block.Input))
		if input == "" {
			input = "{}"
		}
		fmt.Fprintf(out, "[tool_use id=%s name=%s] %s\n\n", block.ID, block.Name, input)
	case "tool_result":
		fmt.Fprintf(out, "[tool_result id=%s error=%t] %s\n\n", block.ToolUseID, block.IsError, block.Content)
	default:
		summary := strings.TrimSpace(block.Text)
		if summary == "" {
			summary = strings.TrimSpace(block.Content)
		}
		if summary != "" {
			fmt.Fprintf(out, "[%s] %s\n\n", block.Type, summary)
		}
	}
}

func renderBlockHTML(out *bytes.Buffer, block anthropic.ContentBlock) {
	switch block.Type {
	case "text":
		if block.Text != "" {
			fmt.Fprintf(out, `<div class="text">%s</div>`+"\n", html.EscapeString(block.Text))
		}
	case "tool_use":
		input := strings.TrimSpace(string(block.Input))
		if input == "" {
			input = "{}"
		}
		fmt.Fprintf(out, `<pre>[tool_use id=%s name=%s] %s</pre>`+"\n", html.EscapeString(block.ID), html.EscapeString(block.Name), html.EscapeString(input))
	case "tool_result":
		fmt.Fprintf(out, `<pre>[tool_result id=%s error=%t] %s</pre>`+"\n", html.EscapeString(block.ToolUseID), block.IsError, html.EscapeString(block.Content))
	default:
		summary := strings.TrimSpace(block.Text)
		if summary == "" {
			summary = strings.TrimSpace(block.Content)
		}
		if summary != "" {
			fmt.Fprintf(out, `<pre>[%s] %s</pre>`+"\n", html.EscapeString(block.Type), html.EscapeString(summary))
		}
	}
}

func exportExtension(format string) string {
	switch format {
	case ExportJSON:
		return ".json"
	case ExportJSONL:
		return ".jsonl"
	case ExportHTML:
		return ".html"
	default:
		return ".md"
	}
}

func safeExportStem(value string) string {
	value = strings.ToLower(value)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	parts := strings.Fields(strings.Trim(re.ReplaceAllString(value, " "), " "))
	if len(parts) == 0 {
		return "conversation"
	}
	if len(parts) > 8 {
		parts = parts[:8]
	}
	stem := strings.Join(parts, "-")
	if stem == "" {
		return "conversation"
	}
	return stem
}

func ValidateExportOutputPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("export output path is required")
	}
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return errors.New("export output path is a directory")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != "." && parent != "" {
		parentInfo, err := os.Stat(parent)
		if err != nil {
			return err
		}
		if !parentInfo.IsDir() {
			return errors.New("export output parent is not a directory")
		}
	}
	return nil
}
