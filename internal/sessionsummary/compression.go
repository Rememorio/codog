package sessionsummary

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
)

const (
	defaultCompressionMaxChars     = 1200
	defaultCompressionMaxLines     = 24
	defaultCompressionMaxLineChars = 160
)

type CompressionBudget struct {
	MaxChars     int `json:"max_chars"`
	MaxLines     int `json:"max_lines"`
	MaxLineChars int `json:"max_line_chars"`
}

type CompressionResult struct {
	Summary               string `json:"summary"`
	OriginalChars         int    `json:"original_chars"`
	CompressedChars       int    `json:"compressed_chars"`
	OriginalLines         int    `json:"original_lines"`
	CompressedLines       int    `json:"compressed_lines"`
	RemovedDuplicateLines int    `json:"removed_duplicate_lines"`
	OmittedLines          int    `json:"omitted_lines"`
	Truncated             bool   `json:"truncated"`
}

func DefaultCompressionBudget() CompressionBudget {
	return CompressionBudget{
		MaxChars:     defaultCompressionMaxChars,
		MaxLines:     defaultCompressionMaxLines,
		MaxLineChars: defaultCompressionMaxLineChars,
	}
}

func CompressText(summary string, budget CompressionBudget) CompressionResult {
	budget = normalizeCompressionBudget(budget)
	trimmed := strings.TrimSpace(summary)
	originalChars := runeLen(trimmed)
	originalLines := lineCount(trimmed)
	normalized := normalizeSummaryLines(trimmed, budget.MaxLineChars)
	if len(normalized.lines) == 0 || budget.MaxChars == 0 || budget.MaxLines == 0 {
		return CompressionResult{
			OriginalChars:         originalChars,
			OriginalLines:         originalLines,
			RemovedDuplicateLines: normalized.removedDuplicateLines,
			OmittedLines:          len(normalized.lines),
			Truncated:             originalChars > 0,
		}
	}

	selected := selectSummaryLineIndexes(normalized.lines, budget)
	compressedLines := make([]string, 0, len(selected)+1)
	for _, index := range selected {
		compressedLines = append(compressedLines, normalized.lines[index])
	}
	if len(compressedLines) == 0 {
		compressedLines = append(compressedLines, truncateRunes(normalized.lines[0], budget.MaxChars))
	}
	omittedLines := len(normalized.lines) - len(compressedLines)
	if omittedLines > 0 {
		pushSummaryLineWithBudget(&compressedLines, fmt.Sprintf("- ... %d additional line(s) omitted.", omittedLines), budget)
	}
	compressed := strings.Join(compressedLines, "\n")
	return CompressionResult{
		Summary:               compressed,
		OriginalChars:         originalChars,
		CompressedChars:       runeLen(compressed),
		OriginalLines:         originalLines,
		CompressedLines:       len(compressedLines),
		RemovedDuplicateLines: normalized.removedDuplicateLines,
		OmittedLines:          omittedLines,
		Truncated:             compressed != trimmed,
	}
}

func CompressSummaryText(summary string) string {
	return CompressText(summary, DefaultCompressionBudget()).Summary
}

func BuildCompactionSummary(omitted []anthropic.Message, retained int) CompressionResult {
	lines := compactionSummaryLines(omitted, retained)
	return CompressText(strings.Join(lines, "\n"), DefaultCompressionBudget())
}

type normalizedSummary struct {
	lines                 []string
	removedDuplicateLines int
}

func normalizeSummaryLines(summary string, maxLineChars int) normalizedSummary {
	seen := map[string]bool{}
	var out []string
	removed := 0
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.Join(strings.Fields(raw), " ")
		if line == "" {
			continue
		}
		line = truncateRunes(line, maxLineChars)
		key := strings.ToLower(line)
		if seen[key] {
			removed++
			continue
		}
		seen[key] = true
		out = append(out, line)
	}
	return normalizedSummary{lines: out, removedDuplicateLines: removed}
}

func selectSummaryLineIndexes(lines []string, budget CompressionBudget) []int {
	selected := map[int]bool{}
	for priority := 0; priority <= 3; priority++ {
		for index, line := range lines {
			if selected[index] || summaryLinePriority(line) != priority {
				continue
			}
			candidate := selectedSummaryLines(lines, selected, index)
			if len(candidate) > budget.MaxLines || joinedRuneLen(candidate) > budget.MaxChars {
				continue
			}
			selected[index] = true
		}
	}
	indexes := make([]int, 0, len(selected))
	for index := range selected {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func selectedSummaryLines(lines []string, selected map[int]bool, next int) []string {
	indexes := make([]int, 0, len(selected)+1)
	for index := range selected {
		indexes = append(indexes, index)
	}
	indexes = append(indexes, next)
	sort.Ints(indexes)
	out := make([]string, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, lines[index])
	}
	return out
}

func pushSummaryLineWithBudget(lines *[]string, line string, budget CompressionBudget) {
	candidate := append(append([]string(nil), (*lines)...), line)
	if len(candidate) <= budget.MaxLines && joinedRuneLen(candidate) <= budget.MaxChars {
		*lines = candidate
	}
}

func summaryLinePriority(line string) int {
	switch {
	case line == "Summary:" || line == "Conversation summary:" || isCoreSummaryDetail(line):
		return 0
	case strings.HasSuffix(line, ":"):
		return 1
	case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "  - "):
		return 2
	default:
		return 3
	}
}

func isCoreSummaryDetail(line string) bool {
	for _, prefix := range []string{
		"- Scope:",
		"- Current work:",
		"- Pending work:",
		"- Key files referenced:",
		"- Tools mentioned:",
		"- Recent user requests:",
		"- Previously compacted context:",
		"- Newly compacted context:",
		"- Last assistant response:",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func compactionSummaryLines(omitted []anthropic.Message, retained int) []string {
	lines := []string{
		"Conversation summary:",
		fmt.Sprintf("- Scope: Previous Codog context was auto-compacted; %d older message(s) were omitted and %d recent message(s) are retained.", len(omitted), retained),
	}
	if previous := previousCompactionSummary(omitted); previous != "" {
		lines = append(lines, "- Previously compacted context: "+previous)
	}
	if current := lastRolePreview(omitted, "user"); current != "" {
		lines = append(lines, "- Current work: "+current)
	}
	if assistant := lastRolePreview(omitted, "assistant"); assistant != "" {
		lines = append(lines, "- Last assistant response: "+assistant)
	}
	if tools := toolNamesSummary(omitted); tools != "" {
		lines = append(lines, "- Tools mentioned: "+tools)
	}
	results, errors := toolResultCounts(omitted)
	if results > 0 {
		lines = append(lines, fmt.Sprintf("- Tool results: %d result message(s), %d error result(s).", results, errors))
	}
	if timeline := compactTimeline(omitted, 6); len(timeline) > 0 {
		lines = append(lines, "- Recent omitted messages:")
		lines = append(lines, timeline...)
	}
	return lines
}

func previousCompactionSummary(messages []anthropic.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		for _, block := range messages[i].Content {
			text := strings.TrimSpace(firstNonEmpty(block.Text, block.Content))
			if strings.Contains(strings.ToLower(text), "auto-compacted") {
				return truncate(text, previewLimit)
			}
		}
	}
	return ""
}

func lastRolePreview(messages []anthropic.Message, role string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != role {
			continue
		}
		if role == "user" && messageOnlyToolResults(messages[i]) {
			continue
		}
		if preview := previewMessage(messages[i]); preview != nil {
			return preview.Text
		}
	}
	return ""
}

func messageOnlyToolResults(msg anthropic.Message) bool {
	if len(msg.Content) == 0 {
		return false
	}
	for _, block := range msg.Content {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}

func toolNamesSummary(messages []anthropic.Message) string {
	seen := map[string]bool{}
	var names []string
	for _, msg := range messages {
		for _, block := range msg.Content {
			name := strings.TrimSpace(block.Name)
			if block.Type != "tool_use" || name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func toolResultCounts(messages []anthropic.Message) (int, int) {
	results := 0
	errors := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Type != "tool_result" {
				continue
			}
			results++
			if block.IsError {
				errors++
			}
		}
	}
	return results, errors
}

func compactTimeline(messages []anthropic.Message, limit int) []string {
	start := len(messages) - limit
	if start < 0 {
		start = 0
	}
	var out []string
	for _, msg := range messages[start:] {
		preview := previewMessage(msg)
		if preview == nil {
			continue
		}
		out = append(out, fmt.Sprintf("  - %s: %s", msg.Role, preview.Text))
	}
	return out
}

func normalizeCompressionBudget(budget CompressionBudget) CompressionBudget {
	defaults := DefaultCompressionBudget()
	if budget.MaxChars < 0 {
		budget.MaxChars = 0
	}
	if budget.MaxLines < 0 {
		budget.MaxLines = 0
	}
	if budget.MaxLineChars < 0 {
		budget.MaxLineChars = 0
	}
	if budget.MaxChars == 0 && budget.MaxLines == 0 && budget.MaxLineChars == 0 {
		return defaults
	}
	if budget.MaxChars == 0 {
		budget.MaxChars = defaults.MaxChars
	}
	if budget.MaxLines == 0 {
		budget.MaxLines = defaults.MaxLines
	}
	if budget.MaxLineChars == 0 {
		budget.MaxLineChars = defaults.MaxLineChars
	}
	return budget
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || runeLen(value) <= limit {
		return value
	}
	if limit == 1 {
		return "."
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "."
}

func joinedRuneLen(lines []string) int {
	total := 0
	for _, line := range lines {
		total += runeLen(line)
	}
	if len(lines) > 1 {
		total += len(lines) - 1
	}
	return total
}

func runeLen(value string) int {
	return len([]rune(value))
}

func lineCount(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	return len(strings.Split(value, "\n"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
