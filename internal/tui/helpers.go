package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func isLocalHelpInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/help", "help", "?":
		return true
	default:
		return false
	}
}

func renderTranscriptEntry(entry transcriptEntry, width int, index int, total int, transcriptMode bool, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	if entry.Tool != nil {
		if !transcriptMode {
			return renderToolActivity(*entry.Tool, width, false, styles)
		}
		text := toolActivityTranscriptText(*entry.Tool)
		header := fmt.Sprintf("%03d/%03d tool · %d %s · %d %s", index+1, max(1, total), transcriptLineCount(text), plural("line", transcriptLineCount(text)), len([]rune(text)), plural("char", len([]rune(text))))
		return styles.role("tool").Render(header) + "\n" + renderToolActivity(*entry.Tool, width, true, styles)
	}
	if !transcriptMode {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = "(empty)"
		}
		marker := transcriptRoleMarker(role)
		prefix := styles.role(role).Render(marker)
		contentWidth := max(4, width-lipgloss.Width(marker)-1)
		content := wrapTranscriptText(text, contentWidth)
		if strings.EqualFold(role, "assistant") {
			content = renderAssistantMarkdown(text, contentWidth, styles)
		}
		wrapped := strings.ReplaceAll(content, "\n", "\n  ")
		return prefix + " " + wrapped
	}
	text := entry.Text
	if text == "" {
		text = "(empty)"
	}
	header := fmt.Sprintf("%03d/%03d %s · %d %s · %d %s", index+1, max(1, total), role, transcriptLineCount(text), plural("line", transcriptLineCount(text)), len([]rune(text)), plural("char", len([]rune(text))))
	content := wrapTranscriptText(text, width)
	if strings.EqualFold(role, "assistant") {
		content = renderAssistantMarkdown(text, width, styles)
	}
	return styles.role(role).Render(header) + "\n" + content
}

func renderRawTranscriptEntry(entry transcriptEntry) string {
	role := strings.ToLower(strings.TrimSpace(entry.Role))
	if role == "" {
		role = "message"
	}
	text := entry.Text
	if entry.Tool != nil {
		role = "tool"
		text = toolActivityTranscriptText(*entry.Tool)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(ansi.Strip(text))
	if text == "" {
		text = "(empty)"
	}
	return role + "\n" + text
}

func renderToolActivity(activity ToolActivity, width int, expanded bool, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	width = max(12, width)
	status := strings.ToLower(strings.TrimSpace(activity.Status))
	if activity.IsError {
		status = "error"
	}
	marker := "●"
	markerStyle := styles.role("tool")
	suffix := ""
	switch status {
	case "running":
		marker = "◐"
		suffix = " running"
	case "success":
		markerStyle = styles.role("success")
	case "error":
		marker = "!"
		markerStyle = styles.role("error")
		suffix = " failed"
	}
	name := toolActivityDisplayName(activity.Name)
	if summary := toolActivityInputSummary(activity.Name, activity.Input); summary != "" {
		name += "(" + summary + ")"
	}
	headerLimit := max(1, width-lipgloss.Width(marker)-1-lipgloss.Width(suffix))
	header := markerStyle.Render(marker) + " " + styles.panelTitle().Render(truncateForComposer(name, headerLimit))
	if suffix != "" {
		header += styles.completion().Render(suffix)
	}
	lines := []string{header}
	outputLines := toolActivityOutputLines(activity, expanded)
	if len(outputLines) == 0 {
		switch status {
		case "running":
			outputLines = []string{"Running..."}
		case "success":
			outputLines = []string{"Done"}
		}
	}
	contentWidth := max(8, width-2)
	for _, line := range outputLines {
		wrapped := wrapTranscriptText(line, contentWidth)
		for _, part := range strings.Split(wrapped, "\n") {
			lines = append(lines, styles.completion().Render("  "+part))
		}
	}
	return strings.Join(lines, "\n")
}

func toolActivityDisplayName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "bashtool":
		return "Bash"
	case "powershell", "powershelltool":
		return "PowerShell"
	case "read", "read_file", "readfiletool":
		return "Read"
	case "write", "write_file", "writefiletool":
		return "Write"
	case "edit", "edit_file", "multiedit", "multi_edit", "apply_patch", "editfiletool":
		return "Edit"
	case "grep", "greptool":
		return "Grep"
	case "glob", "globtool":
		return "Glob"
	case "web_search", "websearchtool":
		return "Web Search"
	case "web_fetch", "webfetchtool":
		return "Web Fetch"
	case "ask_user_question", "askuserquestiontool":
		return "Ask User"
	}
	name = strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(name))
	words := strings.Fields(name)
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[index] = string(runes)
	}
	if len(words) == 0 {
		return "Tool"
	}
	return strings.Join(words, " ")
}

func toolActivityInputSummary(name string, input string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(input)), &payload) != nil {
		return truncateForComposer(strings.Join(strings.Fields(input), " "), 100)
	}
	value := func(keys ...string) string {
		for _, key := range keys {
			if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		return ""
	}
	canonical := strings.ToLower(strings.TrimSpace(name))
	summary := ""
	switch canonical {
	case "bash", "bashtool", "powershell", "powershelltool":
		summary = value("command", "code")
	case "read", "read_file", "readfiletool", "write", "write_file", "writefiletool", "edit", "edit_file", "editfiletool":
		summary = value("path", "file_path")
	case "multi_edit", "multiedit":
		summary = value("path", "file_path")
		if summary == "" {
			if edits, ok := payload["edits"].([]any); ok && len(edits) > 0 {
				if edit, editOK := edits[0].(map[string]any); editOK {
					summary, _ = edit["path"].(string)
					if summary == "" {
						summary, _ = edit["file_path"].(string)
					}
				}
			}
		}
	case "apply_patch":
		summary = "patch"
	case "notebook_edit", "notebookedittool":
		summary = value("notebook_path")
	case "grep", "greptool":
		summary = value("pattern", "query")
		if path := value("path"); path != "" {
			summary += " in " + path
		}
	case "glob", "globtool":
		summary = value("pattern")
		if path := value("path"); path != "" {
			summary += " in " + path
		}
	case "web_search", "websearchtool":
		summary = value("query")
	case "web_fetch", "webfetchtool":
		summary = value("url")
	case "ask_user_question", "askuserquestiontool":
		if questions, ok := payload["questions"].([]any); ok {
			summary = fmt.Sprintf("%d %s", len(questions), plural("question", len(questions)))
		} else {
			summary = value("question")
		}
	default:
		summary = value("prompt", "query", "name", "id", "path")
	}
	return truncateForComposer(strings.Join(strings.Fields(summary), " "), 100)
}

func toolActivityOutputLines(activity ToolActivity, expanded bool) []string {
	output := strings.TrimSpace(activity.Output)
	if output == "" {
		return nil
	}
	if expanded {
		return strings.Split(output, "\n")
	}
	lines := summarizedToolOutputLines(activity.Name, output)
	if len(lines) == 0 {
		lines = strings.Split(output, "\n")
	}
	return truncateToolOutputLines(lines, 4)
}

func summarizedToolOutputLines(name string, output string) []string {
	var payload map[string]any
	if json.Unmarshal([]byte(output), &payload) != nil {
		return nil
	}
	if summarized := specializedToolActivityOutput(name, payload); len(summarized) > 0 {
		return summarized
	}
	lines := []string{}
	lines = append(lines, payloadStringLines(payload, "", "stdout")...)
	lines = append(lines, payloadStringLines(payload, "stderr", "stderr")...)
	lines = append(lines, payloadStringLines(payload, "error", "error")...)
	lines = append(lines, payloadStringLines(payload, "", "output", "message", "content")...)
	if len(lines) == 0 {
		lines = payloadMetadataLines(payload)
	}
	return lines
}

func payloadStringLines(payload map[string]any, label string, keys ...string) []string {
	for _, key := range keys {
		value, ok := payload[key].(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			continue
		}
		if label != "" {
			value = label + ": " + value
		}
		return strings.Split(value, "\n")
	}
	return nil
}

func payloadMetadataLines(payload map[string]any) []string {
	lines := []string{}
	path, _ := payload["path"].(string)
	if path == "" {
		path, _ = payload["file_path"].(string)
	}
	if path != "" {
		line := path
		if count, ok := payload["bytes"].(float64); ok {
			line += fmt.Sprintf(" · %.0f bytes", count)
		}
		lines = append(lines, line)
	}
	if exitCode, ok := payload["exit_code"].(float64); ok {
		line := fmt.Sprintf("Exit code %.0f", exitCode)
		if duration, durationOK := payload["duration_ms"].(float64); durationOK {
			line += fmt.Sprintf(" · %.0f ms", duration)
		}
		lines = append(lines, line)
	}
	return lines
}

func truncateToolOutputLines(lines []string, limit int) []string {
	if len(lines) <= limit {
		return lines
	}
	hidden := len(lines) - limit
	return append(append([]string(nil), lines[:limit]...), fmt.Sprintf("... %d more %s", hidden, plural("line", hidden)))
}

func specializedToolActivityOutput(name string, payload map[string]any) []string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tool_search":
		names := jsonStringList(payload["match_names"])
		if len(names) == 0 {
			return []string{"No matching tools"}
		}
		return []string{"Loaded " + strings.Join(names, ", ")}
	case "web_fetch":
		if title, ok := payload["title"].(string); ok && strings.TrimSpace(title) != "" {
			return []string{"Title: " + strings.TrimSpace(title)}
		}
		for _, key := range []string{"summary", "result"} {
			if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.Split(strings.TrimSpace(value), "\n")
			}
		}
		return nil
	default:
		return nil
	}
}

func jsonStringList(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func toolActivityTranscriptText(activity ToolActivity) string {
	parts := []string{toolActivityDisplayName(activity.Name)}
	if input := strings.TrimSpace(activity.Input); input != "" {
		parts = append(parts, input)
	}
	if output := strings.TrimSpace(activity.Output); output != "" {
		parts = append(parts, output)
	}
	return strings.Join(parts, "\n")
}

func transcriptRoleMarker(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "●"
	case "user":
		return "❯"
	case "tool":
		return "●"
	case "permission", "question":
		return "◆"
	case "error":
		return "!"
	default:
		return "·"
	}
}

func transcriptLineCount(text string) int {
	if text == "" {
		return 0
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Count(text, "\n") + 1
}

func wrapTranscriptText(text string, width int) string {
	if width <= 0 {
		return text
	}
	sourceLines := strings.Split(text, "\n")
	out := make([]string, 0, len(sourceLines))
	for _, line := range sourceLines {
		wrapped := wrapTranscriptLine(line, width)
		out = append(out, wrapped...)
	}
	return strings.Join(out, "\n")
}

func wrapTranscriptLine(line string, width int) []string {
	if len([]rune(line)) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}
	out := []string{}
	current := ""
	for _, word := range words {
		for len([]rune(word)) > width {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			runes := []rune(word)
			out = append(out, string(runes[:width]))
			word = string(runes[width:])
		}
		if current == "" {
			current = word
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		out = append(out, current)
		current = word
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func helpPanel(candidates []string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	sections := []string{
		styles.panelTitle().Render(" help "),
		"Codog is an interactive coding agent. Type a task, reference @files, run !shell commands, or use slash commands.",
		"Enter sends the composer. Shift+Enter, Alt+Enter, Ctrl+J, or a trailing backslash inserts a newline.",
		"",
		"Common workflows",
		"  ask normally       describe the code change, investigation, or test you want",
		"  @path              attach a file reference to the next prompt",
		"  !command           run a local shell command directly",
		"  /attach PATH       stage files or images for the next prompt",
		"  /paste             insert clipboard text or stage clipboard images",
		"",
		"Core commands",
		"  /status   inspect workspace and runtime",
		"  /context  inspect context; /attach files; /paste clipboard",
		"  /diff     view git changes",
		"  /review   review current diff",
		"  /exit     quit",
		"",
		"Keys",
		"  Enter       submit composer",
		"  Shift+Enter insert newline",
		"  Alt+Enter   insert newline fallback",
		"  \\+Enter     replace trailing backslash with newline",
		"  Ctrl+S      stash or restore composer",
		"  Ctrl+G      edit composer in $EDITOR",
		"  Ctrl+X Ctrl+E edit composer in $EDITOR",
		"  Ctrl+X Ctrl+K stop background tasks",
		"  Ctrl+X Ctrl+C compact session",
		"  Ctrl+X Ctrl+U undo last file change",
		"  Ctrl+X Ctrl+S export conversation",
		"  Ctrl+X Ctrl+Y copy conversation",
		"  Ctrl+X Backspace remove last attachment",
		"  Ctrl+_      undo composer edit",
		"  Ctrl+Shift+- undo composer edit",
		"  Ctrl+V      paste clipboard text or image",
		"  Ctrl+Shift+P quick open files",
		"  Ctrl+P      quick open fallback",
		"  Ctrl+Shift+F search workspace",
		"  Ctrl+F      search workspace fallback",
		"  Alt+P       open model picker",
		"  Alt/Meta+M  cycle permission mode fallback",
		"  Alt+O       toggle fast mode",
		"  Alt+T       cycle thinking effort",
		"  Shift+Up    open message actions",
		"  Ctrl+T      toggle tasks",
		"  Ctrl+O      toggle expanded transcript",
		"  Ctrl+L      clear screen",
		"  Ctrl+U      delete before cursor",
		"  Ctrl+K      delete after cursor",
		"  Ctrl+D      exit when composer is empty",
		"  Ctrl+R      search prompt history",
		"  Tab         complete slash command or @file reference",
		"  Up/Down     choose a shown completion",
		"  Up          edit queued prompts while a turn is running",
		"  Up/Down     recall prompt history when composer is empty",
		"  Ctrl+J      insert newline",
		"  PgUp/PgDn   scroll transcript",
		"  Ctrl+B      run composer prompt in background",
		"  Ctrl+Shift+T show background task board",
		"  ?           toggle this help panel",
		"  Esc         clear input, close panels, or press twice to exit",
		"  Ctrl+C      interrupt running work or exit immediately",
	}
	if len(candidates) > 0 {
		sections = append(sections, "", "Completions")
		for _, candidate := range candidates {
			sections = append(sections, "  "+candidate)
		}
	}
	return lipgloss.NewStyle().Width(max(10, width-2)).Render(strings.Join(sections, "\n"))
}

func isREPLExitInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/exit", "/quit", "exit", "quit":
		return true
	default:
		return false
	}
}

func resolveThemeStyles(themed []themeStyles) themeStyles {
	if len(themed) > 0 {
		return themed[0]
	}
	return stylesForTheme("auto")
}
