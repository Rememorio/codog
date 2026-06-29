package prompthistory

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/session"
)

const DefaultLimit = 20

type Entry struct {
	Index     int       `json:"index"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
	Preview   string    `json:"preview"`
	SessionID string    `json:"session_id,omitempty"`
}

type Report struct {
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	SessionID string  `json:"session_id,omitempty"`
	Total     int     `json:"total"`
	Showing   int     `json:"showing"`
	Limit     int     `json:"limit"`
	Entries   []Entry `json:"entries"`
}

func Build(sessionID string, entries []session.PromptEntry, limit int) Report {
	if limit <= 0 {
		limit = DefaultLimit
	}
	start := 0
	if len(entries) > limit {
		start = len(entries) - limit
	}
	report := Report{
		Kind:      "prompt_history",
		Status:    "ok",
		SessionID: sessionID,
		Total:     len(entries),
		Showing:   len(entries) - start,
		Limit:     limit,
		Entries:   make([]Entry, 0, len(entries)-start),
	}
	if len(entries) == 0 {
		report.Status = "empty"
		return report
	}
	for _, entry := range entries[start:] {
		text := strings.TrimSpace(entry.Text)
		report.Entries = append(report.Entries, Entry{
			Index:     entry.Index,
			Time:      entry.Time,
			Text:      text,
			Preview:   Preview(text, 80),
			SessionID: entry.SessionID,
		})
	}
	return report
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Prompt history")
	if report.Total == 0 {
		fmt.Fprintln(w, "  Result           no prompts recorded yet")
		return
	}
	fmt.Fprintf(w, "  Total            %d\n", report.Total)
	fmt.Fprintf(w, "  Showing          %d most recent\n", report.Showing)
	fmt.Fprintf(w, "  Reverse search   Ctrl-R in the REPL\n")
	fmt.Fprintln(w)
	for _, entry := range report.Entries {
		timestamp := "unknown"
		if !entry.Time.IsZero() {
			timestamp = entry.Time.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "  %d. [%s] %s\n", entry.Index, timestamp, entry.Preview)
	}
}

func Preview(text string, limit int) string {
	if limit <= 0 {
		limit = 80
	}
	text = firstNonEmptyLine(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
