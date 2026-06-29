package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
)

type ExportedSession struct {
	ID       string              `json:"id"`
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
		data, err := json.MarshalIndent(ExportedSession{ID: sess.ID, Messages: sess.Messages}, "", "  ")
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
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}

func RenderMarkdown(sess *Session) string {
	var out bytes.Buffer
	fmt.Fprintln(&out, "# Conversation Export")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "- **Session**: `%s`\n", sess.ID)
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

func DefaultExportFilename(sess *Session) string {
	stem := "conversation"
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		for _, block := range msg.Content {
			if strings.TrimSpace(block.Text) != "" {
				stem = strings.TrimSpace(strings.Split(block.Text, "\n")[0])
				return safeExportStem(stem) + ".md"
			}
		}
	}
	return stem + ".md"
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
