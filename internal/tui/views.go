package tui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Rememorio/codog/internal/slash"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func renderQuickOpen(matches []string, selected int, query string, width int, previewPath string, previewLines []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " quick open "
	if query != "" {
		title = fmt.Sprintf(" quick open: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, styles.completion().Render("  start typing to search files")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matching files")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, limit)))
	}
	if previewPath != "" {
		lines = append(lines, styles.completionTitle().Render(" preview "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(previewPath, limit)))
		if len(previewLines) == 0 {
			lines = append(lines, styles.completion().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, styles.completion().Render("  Enter/Tab insert @file · Shift+Tab insert path · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderTodosPanel(items []TodoItem, loading bool, errText string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	title := " tasks "
	if len(items) > 0 {
		completed, inProgress, pending := todoCounts(items)
		title = fmt.Sprintf(" tasks: %d total, %d done, %d active, %d open ", len(items), completed, inProgress, pending)
	}
	lines := []string{styles.completionTitle().Render(title)}
	switch {
	case loading:
		lines = append(lines, styles.completion().Render("  loading tasks..."))
	case strings.TrimSpace(errText) != "":
		lines = append(lines, styles.completion().Render("  error: "+truncateForComposer(errText, limit)))
	case len(items) == 0:
		lines = append(lines, styles.completion().Render("  no tasks"))
	default:
		visible := items
		if len(visible) > 10 {
			visible = visible[:10]
		}
		for _, item := range visible {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(renderTodoLine(item), limit)))
		}
		if hidden := len(items) - len(visible); hidden > 0 {
			lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d more", hidden)))
		}
	}
	lines = append(lines, styles.completion().Render("  Ctrl+T close · /todos manage tasks · Ctrl+Shift+T background tasks"))
	return strings.Join(lines, "\n")
}

func renderTodoLine(item TodoItem) string {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	marker := "[ ]"
	switch status {
	case "completed":
		marker = "[x]"
	case "in_progress":
		marker = "[~]"
	}
	content := strings.TrimSpace(item.ActiveForm)
	if content == "" {
		content = strings.TrimSpace(item.Content)
	}
	if content == "" {
		content = "(empty task)"
	}
	priority := strings.TrimSpace(item.Priority)
	if priority != "" {
		priority = " " + priority
	}
	id := strings.TrimSpace(item.ID)
	if id != "" {
		id += " "
	}
	return fmt.Sprintf("%s %s%s%s", marker, id, content, priority)
}

func todoCounts(items []TodoItem) (completed int, inProgress int, pending int) {
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		default:
			pending++
		}
	}
	return completed, inProgress, pending
}

func normalizeTUITodoItems(items []TodoItem) []TodoItem {
	out := make([]TodoItem, 0, len(items))
	for index, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = fmt.Sprintf("todo-%d", index+1)
		}
		item.Content = strings.TrimSpace(item.Content)
		item.ActiveForm = strings.TrimSpace(item.ActiveForm)
		item.Status = strings.TrimSpace(item.Status)
		if item.Status == "" {
			item.Status = "pending"
		}
		item.Priority = strings.TrimSpace(item.Priority)
		if item.Priority == "" {
			item.Priority = "medium"
		}
		out = append(out, item)
	}
	return out
}

func renderGlobalSearch(matches []globalSearchMatch, selected int, query string, width int, previewPath string, previewLine int, previewLines []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " global search "
	if query != "" {
		title = fmt.Sprintf(" global search: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if query == "" {
		return strings.Join(append(lines, styles.completion().Render("  type to search workspace")), "\n")
	}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		label := fmt.Sprintf("%s:%d  %s", match.File, match.Line, strings.TrimSpace(match.Text))
		lines = append(lines, style.Render(prefix+truncateForComposer(label, limit)))
	}
	if previewPath != "" {
		lines = append(lines, styles.completionTitle().Render(" preview "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(fmt.Sprintf("%s:%d", previewPath, previewLine), limit)))
		if len(previewLines) == 0 {
			lines = append(lines, styles.completion().Render("  (empty file)"))
		}
		for _, line := range previewLines {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
	}
	lines = append(lines, styles.completion().Render("  Enter/Tab insert @file#Lline · Shift+Tab insert path:line · Esc cancel"))
	return strings.Join(lines, "\n")
}

func readQuickOpenPreview(path string, maxLines int, maxBytes int64) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if maxLines <= 0 {
		maxLines = 8
	}
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	info, err := os.Stat(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	if info.IsDir() {
		return []string{"(directory)"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	truncated := false
	if int64(len(data)) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
		return []string{"(binary file)"}
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]string, 0, min(len(parts), maxLines)+1)
	for index, line := range parts {
		if index >= maxLines {
			truncated = true
			break
		}
		lines = append(lines, line)
	}
	if truncated {
		lines = append(lines, "(preview truncated)")
	}
	return lines
}

func searchWorkspaceFiles(query string, files []string, limit int, maxMatchesPerFile int, maxBytes int64) []globalSearchMatch {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if maxMatchesPerFile <= 0 {
		maxMatchesPerFile = 5
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	out := []globalSearchMatch{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		info, err := os.Stat(file)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if int64(len(data)) > maxBytes {
			data = data[:maxBytes]
		}
		if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
			continue
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		perFile := 0
		for index, line := range strings.Split(text, "\n") {
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			out = append(out, globalSearchMatch{File: file, Line: index + 1, Text: line})
			perFile++
			if len(out) >= limit {
				return out
			}
			if perFile >= maxMatchesPerFile {
				break
			}
		}
	}
	return out
}

func readGlobalSearchPreview(path string, line int, contextLines int, maxBytes int64) []string {
	path = strings.TrimSpace(path)
	if path == "" || line <= 0 {
		return nil
	}
	if contextLines < 0 {
		contextLines = 2
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return []string{"(preview unavailable)"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"(preview unavailable)"}
	}
	truncated := false
	if int64(len(data)) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
		return []string{"(binary file)"}
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return nil
	}
	index := line - 1
	if index < 0 {
		index = 0
	}
	if index >= len(parts) {
		index = len(parts) - 1
	}
	start := max(0, index-contextLines)
	end := min(len(parts), index+contextLines+1)
	out := make([]string, 0, end-start+1)
	for current := start; current < end; current++ {
		marker := " "
		if current == index {
			marker = ">"
		}
		out = append(out, fmt.Sprintf("%s%4d: %s", marker, current+1, parts[current]))
	}
	if truncated {
		out = append(out, "(preview truncated)")
	}
	return out
}

func globalSearchMatchLabels(matches []globalSearchMatch) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, fmt.Sprintf("%s:%d", match.File, match.Line))
	}
	return out
}

func globalSearchReference(match globalSearchMatch, mention bool) string {
	if mention {
		return fmt.Sprintf("@%s#L%d ", match.File, match.Line)
	}
	return fmt.Sprintf("%s:%d ", match.File, match.Line)
}

func renderPendingAttachments(attachments []string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(attachments) == 0 {
		return ""
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" attachments: %d ", len(attachments)))}
	start := 0
	if len(attachments) > 4 {
		start = len(attachments) - 4
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(attachments); index++ {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(attachments[index], 100))))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentPanel(attachments []string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(attachments) == 0 {
		return ""
	}
	selected = clampIndex(selected, len(attachments))
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" attachments %d/%d ", selected+1, len(attachments)))}
	for index, attachment := range attachments {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%d. %s", prefix, index+1, truncateForComposer(attachment, limit))))
	}
	lines = append(lines, styles.completion().Render("  Left/Right select · Backspace/Delete remove · Down/Esc close"))
	return strings.Join(lines, "\n")
}

func renderPermissionRequest(request PermissionRequest, selected int, inputMode bool, inputAnswer string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	tool := strings.TrimSpace(request.Tool)
	if tool == "" {
		tool = "tool"
	}
	required := strings.TrimSpace(request.Required)
	if required == "" {
		required = "additional permission"
	}
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{
		styles.role("permission").Render("Permission request"),
		truncateForComposer(fmt.Sprintf("Allow %s to use %s?", tool, required), limit),
	}
	if message := strings.TrimSpace(request.Message); message != "" {
		lines = append(lines, styles.role("permission").Render("Warning: ")+truncateForComposer(strings.Join(strings.Fields(message), " "), max(12, limit-9)))
	}
	if input := strings.TrimSpace(request.Input); input != "" {
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(strings.Join(strings.Fields(input), " "), max(12, limit-2))))
	}
	answers := []string{"Yes"}
	if request.AllowAlways {
		label := "Yes, and don't ask again this session"
		if rule := strings.TrimSpace(request.SuggestedRule); rule != "" {
			label = "Yes, and don't ask again for: " + rule
		}
		answers = append(answers, label)
	}
	answers = append(answers, "No")
	answerValues := []string{"y"}
	if request.AllowAlways {
		answerValues = append(answerValues, "a")
	}
	answerValues = append(answerValues, "n")
	selected = clampIndex(selected, len(answers))
	for index, label := range answers {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		if inputMode && index < len(answerValues) && answerValues[index] == inputAnswer {
			label += ":"
		}
		lines = append(lines, style.Render(truncateForComposer(prefix+label, limit)))
	}
	hint := "  Enter select · Up/Down navigate · Tab amend · Esc deny"
	if inputMode {
		switch inputAnswer {
		case "a":
			hint = "  Edit the session rule · Enter allow · Tab/Esc collapse"
		case "n":
			hint = "  Add guidance for a safer approach · Enter deny · Tab/Esc collapse"
		default:
			hint = "  Add next-step guidance · Enter allow · Tab/Esc collapse"
		}
	}
	lines = append(lines, styles.completion().Render(truncateForComposer(hint, limit)))
	return strings.Join(lines, "\n")
}

func renderedQuestionAnswered(index int, selections [][]bool, customValues []string) bool {
	if index >= 0 && index < len(customValues) && strings.TrimSpace(customValues[index]) != "" {
		return true
	}
	if index >= 0 && index < len(selections) {
		for _, selected := range selections[index] {
			if selected {
				return true
			}
		}
	}
	return false
}

func renderedQuestionAnswer(index int, question Question, selections [][]bool, customValues []string) string {
	parts := []string{}
	if index >= 0 && index < len(selections) {
		for optionIndex, selected := range selections[index] {
			if selected && optionIndex < len(question.Options) {
				parts = append(parts, question.Options[optionIndex].Label)
			}
		}
	}
	if index >= 0 && index < len(customValues) {
		if custom := strings.TrimSpace(customValues[index]); custom != "" {
			parts = append(parts, custom)
		}
	}
	return strings.Join(parts, ", ")
}

func renderPermissionSettings(settings PermissionSettings, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" permissions ")}
	if len(settings.Modes) == 0 {
		lines = append(lines, styles.completion().Render("  no permission modes"))
	} else {
		selected = clampIndex(selected, len(settings.Modes))
		for index, option := range settings.Modes {
			prefix := "  "
			style := styles.completion()
			if index == selected {
				prefix = "> "
				style = styles.selectedCompletion()
			}
			label := strings.TrimSpace(option.Label)
			if label == "" {
				label = option.Name
			}
			if option.Current {
				label += " · current"
			}
			lines = append(lines, style.Render(truncateForComposer(prefix+label, limit)))
			if index == selected && strings.TrimSpace(option.Description) != "" {
				lines = append(lines, styles.completion().Render(truncateForComposer("    "+option.Description, limit)))
			}
		}
	}
	lines = appendPermissionRuleSummary(lines, "Allow", settings.Allow, limit, styles)
	lines = appendPermissionRuleSummary(lines, "Ask", settings.Ask, limit, styles)
	lines = appendPermissionRuleSummary(lines, "Deny", settings.Deny, limit, styles)
	lines = append(lines, styles.completion().Render("  Up/Down choose · Enter apply · Esc close"))
	return strings.Join(lines, "\n")
}

func appendPermissionRuleSummary(lines []string, label string, rules []string, limit int, styles themeStyles) []string {
	if len(rules) == 0 {
		return lines
	}
	shown := rules
	if len(shown) > 2 {
		shown = shown[:2]
	}
	text := fmt.Sprintf("  %s: %s", label, strings.Join(shown, ", "))
	if len(shown) < len(rules) {
		text += fmt.Sprintf(" · %d more", len(rules)-len(shown))
	}
	return append(lines, styles.completion().Render(truncateForComposer(text, limit)))
}

func informationVisibleLines(height int) int {
	if height <= 0 {
		return 12
	}
	return max(4, height-8)
}

func renderInformation(view InformationView, offset int, width int, height int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	title := strings.TrimSpace(view.Title)
	if title == "" {
		title = "Information"
	}
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	visible := informationVisibleLines(height)
	maximum := max(0, len(view.Lines)-visible)
	offset = min(max(0, offset), maximum)
	end := min(len(view.Lines), offset+visible)
	lines := []string{styles.completionTitle().Render(" " + strings.ToLower(title) + " ")}
	for _, line := range view.Lines[offset:end] {
		lines = append(lines, styles.completion().Render(truncateForComposer("  "+line, limit)))
	}
	if len(view.Lines) == 0 {
		lines = append(lines, styles.completion().Render("  no information"))
	}
	position := "Esc close"
	if view.DismissOnConfirm {
		position = "Enter/Space/Esc close"
	}
	if len(view.Lines) > visible {
		if view.DismissOnConfirm {
			position = fmt.Sprintf("%d-%d/%d · Up/Down scroll · Enter/Space/Esc close", offset+1, end, len(view.Lines))
		} else {
			position = fmt.Sprintf("%d-%d/%d · Up/Down scroll · Esc close", offset+1, end, len(view.Lines))
		}
	}
	lines = append(lines, styles.completion().Render("  "+position))
	return strings.Join(lines, "\n")
}

func commandViewVisibleLines(height int) int {
	if height <= 0 {
		return 10
	}
	return max(4, height-10)
}

func commandViewItemCapacity(tab CommandViewTab, height int) int {
	headerLines := 0
	for _, line := range tab.Lines {
		if strings.TrimSpace(line) != "" {
			headerLines++
		}
		if headerLines == 2 {
			break
		}
	}
	return max(1, commandViewVisibleLines(height)-headerLines-1)
}

func renderExportDialog(dialog ExportDialog, selected int, filenameInput bool, input string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" export conversation ")}
	if filenameInput {
		lines = append(lines,
			styles.completion().Render("  Enter filename:"),
			truncateForComposer(input, limit),
			styles.completion().Render("  Enter save · Esc go back"),
		)
		return strings.Join(lines, "\n")
	}
	options := []struct {
		label       string
		description string
	}{
		{label: "Copy to clipboard", description: "Copy the conversation to the system clipboard"},
		{label: "Save to file", description: "Save the conversation in the current workspace"},
	}
	selected = clampIndex(selected, len(options))
	lines = append(lines, styles.completion().Render("  Select export method:"))
	for index, option := range options {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(truncateForComposer(prefix+option.label, limit)))
		if index == selected {
			description := option.description
			if index == 1 && strings.TrimSpace(dialog.DefaultFilename) != "" {
				description += " · " + strings.TrimSpace(dialog.DefaultFilename)
			}
			lines = append(lines, styles.completion().Render(truncateForComposer("    "+description, limit)))
		}
	}
	lines = append(lines, styles.completion().Render("  ↑/↓ select · Enter continue · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderTextInputDialog(dialog TextInputDialog, input string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	title := strings.TrimSpace(dialog.Title)
	if title == "" {
		title = "input"
	}
	prompt := strings.TrimSpace(dialog.Prompt)
	if prompt == "" {
		prompt = "Enter a value:"
	}
	return strings.Join([]string{
		styles.completionTitle().Render(" " + strings.ToLower(title) + " "),
		styles.completion().Render(truncateForComposer("  "+prompt, limit)),
		truncateForComposer(input, limit),
		styles.completion().Render("  Enter confirm · Esc cancel"),
	}, "\n")
}

func renderDiffDialog(sources []DiffSource, sourceIndex int, fileIndex int, detail bool, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	sources = normalizeDiffSources(sources)
	if len(sources) == 0 {
		return styles.completionTitle().Render(" diff ") + "\n" + styles.completion().Render("  no changes")
	}
	sourceIndex = clampIndex(sourceIndex, len(sources))
	source := sources[sourceIndex]
	limit := 100
	if width > 0 {
		limit = max(12, width-8)
	}
	title := fmt.Sprintf(" diff %d/%d: %s ", sourceIndex+1, len(sources), source.Name)
	lines := []string{styles.completionTitle().Render(title)}
	if source.Subtitle != "" {
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(source.Subtitle, limit)))
	}
	if len(source.Files) == 0 {
		lines = append(lines, styles.completion().Render("  no changed files"))
		lines = append(lines, styles.completion().Render("  Left/Right source · Esc close"))
		return strings.Join(lines, "\n")
	}
	fileIndex = clampIndex(fileIndex, len(source.Files))
	selected := source.Files[fileIndex]
	if detail {
		header := fmt.Sprintf("  %s %s", strings.ToUpper(selected.Status), selected.Path)
		lines = append(lines, styles.selectedCompletion().Render(truncateForComposer(header, limit)))
		diff := strings.TrimSpace(selected.Diff)
		if diff == "" {
			diff = selected.Summary
		}
		if diff == "" {
			diff = "(no diff preview)"
		}
		for _, line := range firstLines(diff, 12) {
			lines = append(lines, styles.completion().Render("  "+truncateForComposer(line, limit)))
		}
		lines = append(lines, styles.completion().Render("  Left back · Esc close"))
		return strings.Join(lines, "\n")
	}
	stats := fmt.Sprintf("%d changed %s", len(source.Files), plural("file", len(source.Files)))
	lines = append(lines, styles.completion().Render("  "+stats))
	for index, file := range source.Files {
		prefix := "  "
		style := styles.completion()
		if index == fileIndex {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		summary := strings.TrimSpace(file.Summary)
		if summary != "" {
			summary = " · " + summary
		}
		lines = append(lines, style.Render(truncateForComposer(fmt.Sprintf("%s%s %s%s", prefix, strings.ToUpper(file.Status), file.Path, summary), limit)))
	}
	lines = append(lines, styles.completion().Render("  Up/Down file · Left/Right source · Enter detail · Esc close"))
	return strings.Join(lines, "\n")
}

func firstLines(text string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

func renderStashNotice(stash *composerStash, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if stash == nil {
		return ""
	}
	summary := truncateForComposer(strings.Join(strings.Fields(stash.Text), " "), 80)
	if summary == "" {
		summary = fmt.Sprintf("%d pending %s", len(stash.Attachments), plural("attachment", len(stash.Attachments)))
	}
	lines := []string{styles.completionTitle().Render(" stashed prompt ")}
	lines = append(lines, styles.completion().Render("  Ctrl+S restore: "+summary))
	if len(stash.Attachments) > 0 {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  attachments: %d", len(stash.Attachments))))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentSummary(attachments []string) string {
	if len(attachments) == 0 {
		return "No pending attachments."
	}
	lines := []string{fmt.Sprintf("Pending attachments: %d", len(attachments))}
	for index, attachment := range attachments {
		lines = append(lines, fmt.Sprintf("  %d. %s", index+1, attachment))
	}
	return strings.Join(lines, "\n")
}

func renderSubmittedInput(prompt string, attachments []string) string {
	prompt = strings.TrimSpace(prompt)
	if len(attachments) == 0 {
		return prompt
	}
	lines := make([]string, 0, len(attachments)+2)
	if prompt != "" {
		lines = append(lines, prompt, "")
	}
	lines = append(lines, "Attachments:")
	for _, attachment := range attachments {
		lines = append(lines, "- "+attachment)
	}
	return strings.Join(lines, "\n")
}

func statusBarText(status string, width int) string {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ready"
	}
	if strings.EqualFold(status, "permission") {
		return responsiveStatus(width, 70, "permission · Up/Down · Enter · Tab amend · Esc deny", "permission · Up/Down choose · Enter select · Tab amend · y/n/a shortcuts · Esc deny")
	}
	if strings.EqualFold(status, "question") {
		return responsiveStatus(width, 70, "question · Up/Down · Enter · Esc", "question · Up/Down choose · Enter select · type for custom response · Esc cancel")
	}
	if isBusyStatus(status) {
		return busyStatusBarText(status, width)
	}
	if strings.HasPrefix(strings.ToLower(status), "quick open") {
		return responsiveStatus(width, 80, "quick open · type · Enter · Esc", "quick open · type to search · Enter/Tab insert @file · Shift+Tab path · Esc cancel")
	}
	if strings.EqualFold(status, "model picker") {
		return responsiveStatus(width, 80, "model picker · Enter select · Esc", "model picker · Up/Down choose · Enter select · Esc cancel")
	}
	if strings.EqualFold(status, "message actions") {
		return responsiveStatus(width, 80, "message actions · Enter apply · Esc", "message actions · Up/Down choose · Enter apply · Esc cancel")
	}
	if strings.HasPrefix(strings.ToLower(status), "global search") {
		return responsiveStatus(width, 80, "global search · type · Enter · Esc", "global search · type to search · Enter/Tab insert @line · Shift+Tab path:line · Esc cancel")
	}
	if strings.HasPrefix(strings.ToLower(status), "todos") || strings.EqualFold(status, "loading todos") {
		return responsiveStatus(width, 80, "tasks · Ctrl+T close", "tasks · Ctrl+T close · /todos manage tasks · Ctrl+Shift+T background tasks")
	}
	if strings.EqualFold(status, "ctrl+x") {
		return "Ctrl+X · Ctrl+E edit in $EDITOR · Ctrl+K stop background · Ctrl+C compact · Esc cancel"
	}
	switch {
	case width > 0 && width < 70:
		return fmt.Sprintf("%s · Enter · Tab · Ctrl-R · Esc", status)
	case width > 0 && width < 90:
		return fmt.Sprintf("%s · Enter send · Shift+Enter newline · Tab · Ctrl-R · Ctrl-D · Esc", status)
	case width > 0 && width < 110:
		return fmt.Sprintf("%s · Enter send · Shift+Enter newline · Tab complete · Ctrl-R history · Ctrl-L clear · Ctrl-D exit", status)
	default:
		return fmt.Sprintf("%s · Enter send · Shift+Enter or \\+Enter newline · Tab complete · Ctrl-R history · Ctrl+Shift+P files · Ctrl+Shift+F search · Ctrl+T tasks · Ctrl-O transcript · Ctrl-L clear · Ctrl-D exit", status)
	}
}

func responsiveStatus(width int, breakpoint int, compact string, full string) string {
	if width > 0 && width < breakpoint {
		return compact
	}
	return full
}

func busyStatusBarText(status string, width int) string {
	if width > 0 && width < 70 {
		return fmt.Sprintf("%s · Esc cancel", status)
	}
	if width > 0 && width < 90 {
		return fmt.Sprintf("%s · Esc/Ctrl-C cancel current turn", status)
	}
	return fmt.Sprintf("%s · Esc/Ctrl-C cancel current turn · wait for tools to stop", status)
}

func (m model) promptFooterText(width int) string {
	limit := width
	if limit <= 0 {
		limit = 120
	}
	baseStatus := strings.TrimSpace(m.status)
	if baseStatus == "" {
		baseStatus = m.mode()
	}
	status := statusBarText(baseStatus, width)
	hints := m.promptFooterHints(width)
	if m.transcriptMode && !strings.EqualFold(baseStatus, "transcript") {
		status = appendStatusMode(status, "transcript", width)
	}
	mode := permissionModeFooterLabel(m.modeLabel)
	if !footerHintsContain(hints, mode) {
		status = appendStatusMode(status, mode, width)
	}
	status = truncateFooterLine(status, limit)
	if len(hints) == 0 {
		return status
	}
	byline := truncateFooterLine(strings.Join(hints, " · "), limit)
	if strings.TrimSpace(byline) == "" {
		return status
	}
	return status + "\n" + byline
}

func footerHintsContain(hints []string, text string) bool {
	if text == "" {
		return false
	}
	for _, hint := range hints {
		if strings.Contains(hint, text) {
			return true
		}
	}
	return false
}

type footerHints []string

func (h *footerHints) add(hint string) {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return
	}
	for _, existing := range *h {
		if strings.EqualFold(existing, hint) {
			return
		}
	}
	*h = append(*h, hint)
}

func permissionModeFooterLabel(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "default":
		return ""
	case "accept edits":
		return "⏵⏵ accept edits on"
	case "plan":
		return "⏸ plan mode on"
	case "read-only":
		return "⏸ read-only mode on"
	case "bypass permissions":
		return "⏵⏵ bypass permissions on"
	case "danger full access":
		return "⏵⏵ danger full access on"
	default:
		return strings.TrimSpace(label)
	}
}

func trimFooterHints(hints []string, width int) []string {
	if width <= 0 || len(hints) <= 2 {
		return hints
	}
	limit := 8
	switch {
	case width < 70:
		limit = 3
	case width < 95:
		limit = 4
	case width < 120:
		limit = 5
	case width >= 150:
		limit = 14
	}
	if len(hints) > limit {
		return hints[:limit]
	}
	return hints
}

func truncateFooterLine(line string, width int) string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return line
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	var builder strings.Builder
	used := 0
	for _, r := range line {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > width-3 {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}
	return builder.String() + "..."
}

func fitFooterText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = truncateFooterLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func appendStatusMode(status string, mode string, width int) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return status
	}
	suffix := " · " + mode + " · Shift+Tab mode"
	if width > 0 && len([]rune(status+suffix)) > width {
		suffix = " · " + mode
	}
	out := status + suffix
	if width <= 0 || len([]rune(out)) <= width {
		return out
	}
	return truncateFooterLine(out, width)
}

func isBusyStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "running slash", "interrupting", "backgrounding", "canceling background":
		return true
	default:
		return false
	}
}

func (m *model) setHistory(history []string) {
	m.history = normalizeHistory(history)
	m.historyPos = -1
}

func normalizeHistory(history []string) []string {
	seen := map[string]struct{}{}
	reversed := make([]string, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		text := strings.TrimSpace(history[index])
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		reversed = append(reversed, text)
	}
	out := make([]string, 0, len(reversed))
	for index := len(reversed) - 1; index >= 0; index-- {
		out = append(out, reversed[index])
	}
	return out
}

func (m model) canNavigateHistory() bool {
	if len(m.history) == 0 || m.busy || m.helpOpen || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen {
		return false
	}
	if strings.Contains(m.textarea.Value(), "\n") {
		return false
	}
	return m.historyPos >= 0 || strings.TrimSpace(m.textarea.Value()) == ""
}

func (m *model) navigateHistory(delta int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyPos < 0 {
		m.draft = m.textarea.Value()
		if delta < 0 {
			m.historyPos = len(m.history) - 1
		} else {
			return
		}
	} else {
		m.historyPos += delta
	}
	if m.historyPos < 0 {
		m.historyPos = -1
		m.textarea.SetValue(m.draft)
		m.status = "compose"
		return
	}
	if m.historyPos >= len(m.history) {
		m.historyPos = -1
		m.textarea.SetValue(m.draft)
		m.status = "compose"
		return
	}
	m.textarea.SetValue(m.history[m.historyPos])
	m.status = fmt.Sprintf("history %d/%d", m.historyPos+1, len(m.history))
}

func (m *model) openHistorySearch() {
	if m.searchOpen {
		return
	}
	m.searchOpen = true
	m.draft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.updateHistorySearch()
}

func (m *model) updateHistorySearch() {
	m.searchHits = filterHistory(m.history, m.textarea.Value(), 8)
	if m.searchPos < 0 || m.searchPos >= len(m.searchHits) {
		m.searchPos = 0
	}
	m.status = fmt.Sprintf("history search %d/%d", min(len(m.searchHits), m.searchPos+1), len(m.searchHits))
	if len(m.searchHits) == 0 {
		m.status = "history search"
	}
}

func (m *model) moveHistorySearch(delta int) {
	if len(m.searchHits) == 0 {
		return
	}
	m.searchPos = (m.searchPos + delta + len(m.searchHits)) % len(m.searchHits)
	m.status = fmt.Sprintf("history search %d/%d", m.searchPos+1, len(m.searchHits))
}

func (m *model) closeHistorySearch(accept bool) {
	if accept && len(m.searchHits) > 0 {
		if m.searchPos < 0 || m.searchPos >= len(m.searchHits) {
			m.searchPos = 0
		}
		m.pushComposerUndoValue(m.draft)
		m.textarea.SetValue(m.searchHits[m.searchPos])
		m.status = "history selected"
	} else {
		m.textarea.SetValue(m.draft)
		m.status = m.mode()
	}
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
}

func (m model) openTaskBoard() (tea.Model, tea.Cmd) {
	if m.helpOpen {
		m.helpOpen = false
	}
	m.matches = nil
	m.selected = 0
	m.status = "loading tasks"
	m.refreshViewport()
	return m, runTaskBoardCommand(m.ctx, m.taskBoard)
}

func (m model) toggleTodos() (tea.Model, tea.Cmd) {
	if m.todosOpen {
		m.closeTodos()
		return m, nil
	}
	m.todosOpen = true
	m.todosLoading = true
	m.todoErr = ""
	m.todoItems = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.status = "loading todos"
	return m, runTodoListCommand(m.ctx, m.todos)
}

func (m *model) closeTodos() {
	m.todosOpen = false
	m.todosLoading = false
	m.todoErr = ""
	m.todoItems = nil
	m.status = m.mode()
}

func (m *model) openQuickOpen() {
	if m.quickOpen {
		return
	}
	m.quickOpen = true
	m.quickOpenDraft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.updateQuickOpen()
}

func (m *model) updateQuickOpen() {
	m.quickOpenMatches = filterQuickOpenFileCandidates(m.textarea.Value(), m.fileCandidates, 8)
	if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
		m.quickOpenSelected = 0
	}
	if len(m.quickOpenMatches) == 0 {
		m.quickOpenPreviewPath = ""
		m.quickOpenPreviewLines = nil
		m.status = "quick open"
		return
	}
	m.refreshQuickOpenPreview()
	m.status = fmt.Sprintf("quick open %d/%d", m.quickOpenSelected+1, len(m.quickOpenMatches))
}

func (m *model) moveQuickOpen(delta int) {
	if len(m.quickOpenMatches) == 0 {
		return
	}
	m.quickOpenSelected = (m.quickOpenSelected + delta + len(m.quickOpenMatches)) % len(m.quickOpenMatches)
	m.refreshQuickOpenPreview()
	m.status = fmt.Sprintf("quick open %d/%d", m.quickOpenSelected+1, len(m.quickOpenMatches))
}

func (m *model) setQuickOpenIndex(index int) {
	if len(m.quickOpenMatches) == 0 {
		return
	}
	m.quickOpenSelected = clampIndex(index, len(m.quickOpenMatches))
	m.refreshQuickOpenPreview()
	m.status = fmt.Sprintf("quick open %d/%d", m.quickOpenSelected+1, len(m.quickOpenMatches))
}

func (m *model) refreshQuickOpenPreview() {
	if len(m.quickOpenMatches) == 0 {
		m.quickOpenPreviewPath = ""
		m.quickOpenPreviewLines = nil
		return
	}
	if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
		m.quickOpenSelected = 0
	}
	path := m.quickOpenMatches[m.quickOpenSelected]
	if path == m.quickOpenPreviewPath && len(m.quickOpenPreviewLines) > 0 {
		return
	}
	m.quickOpenPreviewPath = path
	m.quickOpenPreviewLines = readQuickOpenPreview(path, 8, 32*1024)
}

func (m *model) closeQuickOpen(accept bool, mention bool) {
	if accept && len(m.quickOpenMatches) > 0 {
		if m.quickOpenSelected < 0 || m.quickOpenSelected >= len(m.quickOpenMatches) {
			m.quickOpenSelected = 0
		}
		m.pushComposerUndoValue(m.quickOpenDraft)
		selected := m.quickOpenMatches[m.quickOpenSelected]
		insert := selected + " "
		if mention {
			insert = "@" + insert
		}
		m.textarea.SetValue(insertWithComposerSpacing(m.quickOpenDraft, insert))
		m.textarea.CursorEnd()
		if mention {
			m.status = "file referenced"
		} else {
			m.status = "path inserted"
		}
	} else {
		m.textarea.SetValue(m.quickOpenDraft)
		m.textarea.CursorEnd()
		m.status = m.mode()
	}
	m.quickOpen = false
	m.quickOpenDraft = ""
	m.quickOpenMatches = nil
	m.quickOpenSelected = 0
	m.quickOpenPreviewPath = ""
	m.quickOpenPreviewLines = nil
	m.matches = nil
	m.selected = 0
	m.refreshCompletionMenu()
}

func (m *model) openGlobalSearch() {
	if m.globalSearch {
		return
	}
	m.globalSearch = true
	m.globalSearchDraft = m.textarea.Value()
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.searchOpen = false
	m.searchHits = nil
	m.searchPos = 0
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.updateGlobalSearch()
}

func (m *model) updateGlobalSearch() {
	m.globalSearchMatches = searchWorkspaceFiles(m.textarea.Value(), m.fileCandidates, 50, 5, 256*1024)
	if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
		m.globalSearchSelected = 0
	}
	if len(m.globalSearchMatches) == 0 {
		m.globalSearchPreviewPath = ""
		m.globalSearchPreviewLine = 0
		m.globalSearchPreviewLines = nil
		m.status = "global search"
		return
	}
	m.refreshGlobalSearchPreview()
	m.status = fmt.Sprintf("global search %d/%d", m.globalSearchSelected+1, len(m.globalSearchMatches))
}

func (m *model) moveGlobalSearch(delta int) {
	if len(m.globalSearchMatches) == 0 {
		return
	}
	m.globalSearchSelected = (m.globalSearchSelected + delta + len(m.globalSearchMatches)) % len(m.globalSearchMatches)
	m.refreshGlobalSearchPreview()
	m.status = fmt.Sprintf("global search %d/%d", m.globalSearchSelected+1, len(m.globalSearchMatches))
}

func (m *model) setGlobalSearchIndex(index int) {
	if len(m.globalSearchMatches) == 0 {
		return
	}
	m.globalSearchSelected = clampIndex(index, len(m.globalSearchMatches))
	m.refreshGlobalSearchPreview()
	m.status = fmt.Sprintf("global search %d/%d", m.globalSearchSelected+1, len(m.globalSearchMatches))
}

func (m *model) refreshGlobalSearchPreview() {
	if len(m.globalSearchMatches) == 0 {
		m.globalSearchPreviewPath = ""
		m.globalSearchPreviewLine = 0
		m.globalSearchPreviewLines = nil
		return
	}
	if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
		m.globalSearchSelected = 0
	}
	selected := m.globalSearchMatches[m.globalSearchSelected]
	if selected.File == m.globalSearchPreviewPath && selected.Line == m.globalSearchPreviewLine && len(m.globalSearchPreviewLines) > 0 {
		return
	}
	m.globalSearchPreviewPath = selected.File
	m.globalSearchPreviewLine = selected.Line
	m.globalSearchPreviewLines = readGlobalSearchPreview(selected.File, selected.Line, 2, 64*1024)
}

func (m *model) closeGlobalSearch(accept bool, mention bool) {
	if accept && len(m.globalSearchMatches) > 0 {
		if m.globalSearchSelected < 0 || m.globalSearchSelected >= len(m.globalSearchMatches) {
			m.globalSearchSelected = 0
		}
		m.pushComposerUndoValue(m.globalSearchDraft)
		insert := globalSearchReference(m.globalSearchMatches[m.globalSearchSelected], mention)
		m.textarea.SetValue(insertWithComposerSpacing(m.globalSearchDraft, insert))
		m.textarea.CursorEnd()
		if mention {
			m.status = "line referenced"
		} else {
			m.status = "location inserted"
		}
	} else {
		m.textarea.SetValue(m.globalSearchDraft)
		m.textarea.CursorEnd()
		m.status = m.mode()
	}
	m.globalSearch = false
	m.globalSearchDraft = ""
	m.globalSearchMatches = nil
	m.globalSearchSelected = 0
	m.globalSearchPreviewPath = ""
	m.globalSearchPreviewLine = 0
	m.globalSearchPreviewLines = nil
	m.matches = nil
	m.selected = 0
	m.refreshCompletionMenu()
}

func filterHistory(history []string, query string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := []string{}
	seen := map[string]struct{}{}
	for index := len(history) - 1; index >= 0 && len(out) < limit; index-- {
		text := strings.TrimSpace(history[index])
		if text == "" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(text), query) {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func (m *model) appendHistory(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	m.history = append(m.history, value)
	m.history = normalizeHistory(m.history)
}

func truncateForComposer(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if limit <= 0 {
		limit = 80
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func clampIndex(index int, length int) int {
	if length <= 0 {
		return 0
	}
	return min(max(index, 0), length-1)
}

func (m model) completionCandidates() []string {
	if len(m.candidates) > 0 {
		return m.candidates
	}
	return slash.MenuCandidates(slash.CandidateOptions{})
}

func (m *model) layout(width int, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	m.width = width
	m.height = height
	minimumWidth := 40
	if m.inline {
		minimumWidth = 8
	}
	m.textarea.SetWidth(max(minimumWidth, width-4))
	composerHeight := 4
	reservedHeight := 9
	if m.inline {
		composerHeight = 1
		reservedHeight = 5
	}
	m.textarea.SetHeight(composerHeight)
	viewportHeight := height - reservedHeight
	if viewportHeight < 6 {
		viewportHeight = 6
	}
	m.viewport.Width = max(minimumWidth, width)
	m.viewport.Height = viewportHeight
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	if m.helpOpen {
		m.viewport.SetContent(helpPanel(m.completionCandidates(), m.viewport.Width, stylesForTheme(m.theme)))
		return
	}
	lines := []string{}
	start := 0
	if m.inline && !m.transcriptMode {
		start = min(max(m.printedEntries, 0), len(m.transcript))
	}
	for index, entry := range m.transcript[start:] {
		index += start
		lines = append(lines, m.renderTranscriptEntry(entry, max(8, m.viewport.Width-2), index))
	}
	m.viewport.SetContent(strings.Join(lines, "\n\n"))
}

func (m *model) prepareInlineTranscript() {
	if !m.inline || len(m.transcript) == 0 {
		return
	}
	m.initialPrint = m.renderTranscriptRange(0, len(m.transcript))
	m.printedEntries = len(m.transcript)
	m.refreshViewport()
}

func (m *model) flushInlineTranscript() tea.Cmd {
	if !m.inline || m.printedEntries >= len(m.transcript) {
		return nil
	}
	content := m.renderTranscriptRange(m.printedEntries, len(m.transcript))
	m.printedEntries = len(m.transcript)
	m.refreshViewport()
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return tea.Println(content)
}

func (m model) renderTranscriptRange(start int, end int) string {
	start = min(max(start, 0), len(m.transcript))
	end = min(max(end, start), len(m.transcript))
	width := max(8, m.viewport.Width-2)
	entries := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		entries = append(entries, m.renderTranscriptEntry(m.transcript[index], width, index))
	}
	return strings.Join(entries, "\n\n")
}

func (m model) renderTranscriptEntry(entry transcriptEntry, width int, index int) string {
	styles := stylesForTheme(m.theme)
	if strings.EqualFold(strings.TrimSpace(entry.Role), "welcome") {
		return renderWelcome(welcomeInfo{
			Version:    m.version,
			Model:      m.currentModel,
			Permission: m.modeLabel,
			Workspace:  m.workspace,
			GitBranch:  m.gitBranch,
		}, width, styles)
	}
	return renderTranscriptEntry(entry, width, index, len(m.transcript), m.transcriptMode, styles)
}

func sequenceCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Sequence(filtered...)
	}
}

func (m model) mode() string {
	if m.helpOpen {
		return "help"
	}
	if m.quickOpen {
		return "quick open"
	}
	if m.globalSearch {
		return "global search"
	}
	if m.todosOpen {
		return "todos"
	}
	if m.sessionPicker != nil {
		return "resume"
	}
	if m.permissionSettings != nil {
		return "permissions"
	}
	if m.commandView != nil {
		if title := strings.ToLower(strings.TrimSpace(m.commandView.Title)); title != "" {
			return title
		}
		return "settings"
	}
	if m.exportDialog != nil {
		return "export"
	}
	if m.information != nil {
		if title := strings.ToLower(strings.TrimSpace(m.information.Title)); title != "" {
			return title
		}
		return "information"
	}
	if m.modelPicker {
		return "model picker"
	}
	if m.themePicker {
		return "theme picker"
	}
	if m.messageActions {
		return "message actions"
	}
	if m.attachmentsOpen {
		return "attachments"
	}
	if m.diffDialog {
		if m.diffDetail {
			return "diff detail"
		}
		return "diff"
	}
	value := strings.TrimSpace(m.textarea.Value())
	if isBashModeInput(value) {
		return "bash"
	}
	if m.vimEnabled && m.vimNormal {
		return "vim normal"
	}
	if len(m.matches) > 0 {
		return fmt.Sprintf("%d completions", len(m.matches))
	}
	if strings.HasPrefix(value, "/") {
		return "slash"
	}
	if value == "" {
		return "ready"
	}
	return "compose"
}
