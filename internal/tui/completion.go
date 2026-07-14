package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Rememorio/codog/internal/slash"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m model) completeSlashCommand() model {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	candidates := m.filteredCompletionCandidates(value)
	if len(candidates) == 0 && isBashModeInput(value) {
		if completion, ok := m.bashHistoryCompletion(value); ok {
			m.textarea.SetValue(completeValue(completion))
			m.textarea.CursorEnd()
			m.matches = nil
			m.selected = 0
			m.commandArgumentHint = ""
			m.inlineGhostText = ""
			return m
		}
	}
	if !strings.HasPrefix(value, "/") && !isBashModeInput(value) {
		if completion, ok := m.midInputSlashCompletion(value); ok {
			m.textarea.SetValue(value[:completion.start] + completeValue(completion.candidate))
			m.textarea.CursorEnd()
			m.matches = nil
			m.selected = 0
			m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
			m.inlineGhostText = ""
			return m
		}
	}
	switch len(candidates) {
	case 0:
		m.matches = nil
		m.selected = 0
	case 1:
		m.textarea.SetValue(m.completeValue(value, candidates[0]))
		m.matches = nil
		m.selected = 0
		m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
		m.inlineGhostText = ""
	default:
		m.matches = candidates
		if m.selected < 0 || m.selected >= len(m.matches) {
			m.selected = 0
		}
	}
	return m
}

func (m *model) refreshCompletionMenu() {
	value := strings.Trim(m.textarea.Value(), "\r\n\t")
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	if value == "" || m.busy || m.searchOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.messageActions || m.attachmentsOpen || m.diffDialog {
		m.matches = nil
		m.selected = 0
		return
	}
	m.commandArgumentHint = slashCommandArgumentHint(value)
	candidates := m.filteredCompletionCandidates(value)
	if len(candidates) == 0 && isBashModeInput(value) {
		if completion, ok := m.bashHistoryCompletion(value); ok {
			m.inlineGhostText = completion
		}
		m.matches = nil
		m.selected = 0
		return
	}
	if len(candidates) == 0 && !strings.HasPrefix(value, "/") {
		if completion, ok := m.midInputSlashCompletion(value); ok {
			m.inlineGhostText = completion.display()
		}
		m.matches = nil
		m.selected = 0
		return
	}
	candidates = automaticCompletionCandidates(value, candidates)
	m.matches = candidates
	if len(m.matches) == 0 {
		m.selected = 0
		return
	}
	if m.selected < 0 || m.selected >= len(m.matches) {
		m.selected = 0
	}
}

func (m model) filteredCompletionCandidates(value string) []string {
	if isBashModeInput(value) {
		if prefix, ok := activeBashPathPrefix(value); ok {
			return filterBashPathCandidates(prefix, m.fileCandidates)
		}
		return nil
	}
	if strings.HasPrefix(value, "/") {
		candidates := m.completionCandidates()
		matches := slash.FilterCandidatesStable(value, candidates)
		if len(matches) == 0 && isSlashCommandNameInput(value) {
			return slash.SuggestWithCandidates(value, 8, candidates)
		}
		return matches
	}
	if prefix, ok := activeFileReferencePrefix(value); ok {
		return filterFileReferenceCandidates(prefix, m.fileCandidates)
	}
	return nil
}

func isSlashCommandNameInput(value string) bool {
	value = strings.Trim(value, "\r\n\t")
	return strings.HasPrefix(value, "/") && !strings.ContainsAny(strings.TrimPrefix(value, "/"), " \t\r\n")
}

func (m model) bashHistoryCompletion(value string) (string, bool) {
	value = strings.TrimRight(value, "\r\n\t")
	if !isBashModeInput(value) {
		return "", false
	}
	command := strings.TrimSpace(bashModeCommand(value))
	if command == "" {
		return "", false
	}
	normalized := strings.TrimSpace(value)
	for index := len(m.history) - 1; index >= 0; index-- {
		entry := strings.TrimSpace(m.history[index])
		if entry == "" || entry == normalized || !isBashModeInput(entry) {
			continue
		}
		if strings.HasPrefix(entry, normalized) {
			return entry, true
		}
	}
	return "", false
}

type midInputSlashCompletion struct {
	start     int
	token     string
	candidate string
	suffix    string
}

func (m model) midInputSlashCompletion(value string) (midInputSlashCompletion, bool) {
	token, start, ok := trailingMidInputSlashToken(value)
	if !ok {
		return midInputSlashCompletion{}, false
	}
	candidates := slash.FilterCandidates(token, m.completionCandidates())
	if len(candidates) == 0 {
		candidates = slash.SuggestWithCandidates(token, 1, m.completionCandidates())
	}
	if len(candidates) == 0 {
		return midInputSlashCompletion{}, false
	}
	candidate := firstSlashCommandToken(candidates[0])
	if candidate == "" || !strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(token)) {
		return midInputSlashCompletion{}, false
	}
	suffix := candidate[len(token):]
	if suffix == "" {
		return midInputSlashCompletion{}, false
	}
	return midInputSlashCompletion{start: start, token: token, candidate: candidate, suffix: suffix}, true
}

func (completion midInputSlashCompletion) display() string {
	if completion.token == "" || completion.suffix == "" {
		return ""
	}
	return completion.token + completion.suffix
}

func trailingMidInputSlashToken(value string) (string, int, bool) {
	if strings.HasPrefix(value, "/") {
		return "", 0, false
	}
	trimmed := strings.TrimRight(value, " \t\r\n")
	if trimmed == "" || len(trimmed) != len(value) {
		return "", 0, false
	}
	start := strings.LastIndexAny(trimmed, " \t\r\n")
	if start < 0 {
		return "", 0, false
	}
	tokenStart := start + 1
	token := trimmed[tokenStart:]
	if len(token) <= 1 || !strings.HasPrefix(token, "/") {
		return "", 0, false
	}
	if strings.ContainsAny(strings.TrimPrefix(token, "/"), `/\ "'`) {
		return "", 0, false
	}
	return token, tokenStart, true
}

func firstSlashCommandToken(candidate string) string {
	fields := strings.Fields(strings.TrimSpace(candidate))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	return fields[0]
}

func slashCommandArgumentHint(value string) string {
	if !strings.HasPrefix(strings.TrimSpace(value), "/") {
		return ""
	}
	trimmedRight := strings.TrimRight(value, "\r\n\t")
	fields := strings.Fields(trimmedRight)
	if len(fields) == 0 {
		return ""
	}
	if !strings.ContainsAny(trimmedRight, " \t") {
		return ""
	}
	spec, ok := slash.Lookup(fields[0])
	if !ok {
		return ""
	}
	usage := strings.TrimSpace(spec.Usage)
	if usage == "" {
		usage = spec.Name
	}
	args := strings.TrimSpace(strings.TrimPrefix(usage, spec.Name))
	if args == "" {
		return ""
	}
	return "arguments: " + args + "  ·  " + spec.Description
}

func isExactSlashCommandInput(value string) bool {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 1 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	_, ok := slash.Lookup(fields[0])
	return ok
}

func shouldAcceptCompletionOnEnter(value string) bool {
	return !isExactSlashCommandInput(value) && !isREPLExitInput(value) && !isLocalHelpInput(value)
}

func automaticCompletionCandidates(value string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	normalizedValue := strings.TrimSpace(value)
	exactCommand := ""
	if isExactSlashCommandInput(normalizedValue) {
		exactCommand = firstSlashCommandToken(normalizedValue)
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == normalizedValue && !strings.HasSuffix(candidate, " ") {
			continue
		}
		if exactCommand != "" && firstSlashCommandToken(candidate) != exactCommand {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func (m model) acceptSelectedCompletion() model {
	if len(m.matches) == 0 {
		return m
	}
	if m.selected < 0 || m.selected >= len(m.matches) {
		m.selected = 0
	}
	m.textarea.SetValue(m.completeValue(m.textarea.Value(), m.matches[m.selected]))
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = slashCommandArgumentHint(m.textarea.Value())
	m.inlineGhostText = ""
	return m
}

func (m model) completeValue(value string, candidate string) string {
	if isBashModeInput(value) {
		return completeBashPathValue(value, candidate)
	}
	if strings.HasPrefix(candidate, "@") {
		return completeFileReferenceValue(value, candidate)
	}
	return completeValue(candidate)
}

func completeValue(candidate string) string {
	if strings.HasSuffix(candidate, " ") {
		return candidate
	}
	return candidate + " "
}

func activeFileReferencePrefix(value string) (string, bool) {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return "", false
	}
	if index > 0 {
		previous := value[index-1]
		if previous != ' ' && previous != '\t' && previous != '\n' && previous != '(' && previous != '[' && previous != '{' {
			return "", false
		}
	}
	token := value[index:]
	if token == "" || strings.ContainsAny(token, " \t\r\n\"'") {
		return "", false
	}
	return token, true
}

func filterFileReferenceCandidates(prefix string, files []string) []string {
	query := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(prefix), "@"))
	out := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" {
			continue
		}
		lower := strings.ToLower(file)
		if query != "" && !strings.HasPrefix(lower, query) && !strings.Contains(lower, "/"+query) {
			continue
		}
		candidate := "@" + file
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func activeBashPathPrefix(value string) (string, bool) {
	value = strings.TrimRight(value, "\r\n\t")
	if !isBashModeInput(value) {
		return "", false
	}
	body := strings.TrimPrefix(strings.TrimSpace(value), "!")
	if strings.TrimSpace(body) == "" || strings.HasSuffix(body, " ") || strings.HasSuffix(body, "\t") {
		return "", false
	}
	index := strings.LastIndexAny(body, " \t\n")
	if index >= 0 {
		body = body[index+1:]
	}
	body = strings.Trim(body, `"'`)
	if body == "" || strings.HasPrefix(body, "-") || strings.HasPrefix(body, "$") {
		return "", false
	}
	return filepathToSlash(body), true
}

func filterBashPathCandidates(prefix string, files []string) []string {
	query := strings.ToLower(strings.TrimSpace(filepathToSlash(prefix)))
	if query == "" {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" || seen[file] {
			continue
		}
		lower := strings.ToLower(file)
		if !strings.HasPrefix(lower, query) && !strings.Contains(lower, "/"+query) {
			continue
		}
		seen[file] = true
		out = append(out, file)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func filterQuickOpenFileCandidates(query string, files []string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(tokens) == 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		file = strings.TrimSpace(filepathToSlash(file))
		if file == "" || seen[file] {
			continue
		}
		lower := strings.ToLower(file)
		matched := true
		for _, token := range tokens {
			if !strings.Contains(lower, token) && !fuzzySubsequence(token, lower) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		seen[file] = true
		out = append(out, file)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func fuzzySubsequence(query string, candidate string) bool {
	if query == "" {
		return true
	}
	queryRunes := []rune(query)
	pos := 0
	for _, r := range candidate {
		if pos >= len(queryRunes) {
			break
		}
		if r == queryRunes[pos] {
			pos++
		}
	}
	return pos == len(queryRunes)
}

func completeFileReferenceValue(value string, candidate string) string {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return completeValue(candidate)
	}
	return value[:index] + completeValue(candidate)
}

func completeBashPathValue(value string, candidate string) string {
	candidate = strings.TrimSpace(filepathToSlash(candidate))
	if candidate == "" {
		return value
	}
	trimmedRight := strings.TrimRight(value, "\r\n\t")
	searchStart := strings.LastIndexAny(trimmedRight, " \t\n")
	if searchStart < 0 {
		searchStart = 0
	} else {
		searchStart++
	}
	if searchStart < len(trimmedRight) && (trimmedRight[searchStart] == '"' || trimmedRight[searchStart] == '\'') {
		searchStart++
	}
	return trimmedRight[:searchStart] + completeValue(candidate)
}

func insertWithComposerSpacing(base string, insert string) string {
	if strings.TrimSpace(base) == "" {
		return insert
	}
	if strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\t") || strings.HasSuffix(base, "\n") {
		return base + insert
	}
	return base + " " + insert
}

func (m *model) pushComposerUndo() {
	m.pushComposerUndoValue(m.textarea.Value())
}

func (m *model) pushComposerUndoValue(value string) {
	const maxComposerUndo = 100
	if len(m.undoStack) > 0 && m.undoStack[len(m.undoStack)-1] == value {
		return
	}
	m.undoStack = append(m.undoStack, value)
	if len(m.undoStack) > maxComposerUndo {
		m.undoStack = append([]string(nil), m.undoStack[len(m.undoStack)-maxComposerUndo:]...)
	}
}

func (m *model) undoComposer() {
	current := m.textarea.Value()
	for len(m.undoStack) > 0 {
		last := m.undoStack[len(m.undoStack)-1]
		m.undoStack = m.undoStack[:len(m.undoStack)-1]
		if last == current {
			continue
		}
		m.textarea.SetValue(last)
		m.textarea.CursorEnd()
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.keyChordPrefix = ""
		m.ctrlXChord = false
		m.status = "undo"
		m.refreshCompletionMenu()
		return
	}
	m.status = "nothing to undo"
}

func (m *model) deleteComposerBeforeCursor() {
	m.deleteComposerWithTextareaKey(tea.KeyMsg{Type: tea.KeyCtrlU}, "deleted before cursor")
}

func (m *model) deleteComposerAfterCursor() {
	m.deleteComposerWithTextareaKey(tea.KeyMsg{Type: tea.KeyCtrlK}, "deleted after cursor")
}

func (m *model) deleteComposerWithTextareaKey(key tea.KeyMsg, status string) {
	before := m.textarea.Value()
	m.textarea, _ = m.textarea.Update(key)
	after := m.textarea.Value()
	if after == before {
		m.status = "nothing to delete"
		return
	}
	m.pushComposerUndoValue(before)
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	m.status = status
	m.refreshCompletionMenu()
}

func (m *model) moveComposerLineStart() {
	m.textarea.CursorStart()
	m.status = "line start"
}

func (m *model) moveComposerLineEnd() {
	m.textarea.CursorEnd()
	m.status = "line end"
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func renderCompletions(matches []string, selected int, themed ...themeStyles) string {
	const maxVisible = 8

	styles := resolveThemeStyles(themed)
	if len(matches) == 0 {
		return ""
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	title := " suggestions "
	start := 0
	end := len(matches)
	if len(matches) > maxVisible {
		start = min(max(selected-maxVisible/2, 0), len(matches)-maxVisible)
		end = start + maxVisible
		title = fmt.Sprintf(" suggestions · %d/%d ", selected+1, len(matches))
	}
	lines := []string{styles.completionTitle().Render(title)}
	for index, match := range matches[start:end] {
		actualIndex := start + index
		prefix := "  "
		style := styles.completion()
		if actualIndex == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+completionDisplayLine(match)))
	}
	lines = append(lines, styles.completion().Render("  Enter accept · Tab complete · Esc close"))
	return strings.Join(lines, "\n")
}

func completionDisplayLine(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	spec, ok := slash.DescribeCandidate(candidate)
	if !ok || strings.TrimSpace(spec.Description) == "" {
		if strings.HasPrefix(candidate, "@") {
			return truncateForComposer(candidate+"  -  file reference", 120)
		}
		if !strings.HasPrefix(candidate, "/") && (strings.Contains(candidate, "/") || strings.Contains(candidate, ".")) {
			return truncateForComposer(candidate+"  -  file path", 120)
		}
		return candidate
	}
	return truncateForComposer(candidate+"  -  "+spec.Description, 120)
}

func renderCommandAssist(argumentHint string, inlineHint string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	lines := []string{}
	if argumentHint != "" {
		lines = append(lines, styles.completionTitle().Render(" command args "))
		lines = append(lines, styles.completion().Render("  "+truncateForComposer(argumentHint, 120)))
	}
	if inlineHint != "" {
		if len(lines) == 0 {
			lines = append(lines, styles.completionTitle().Render(" command hint "))
		}
		lines = append(lines, styles.completion().Render("  "+truncateForComposer("ghost: "+inlineHint+"  ·  Tab accept", 120)))
	}
	return strings.Join(lines, "\n")
}

func renderHistorySearch(matches []string, selected int, query string, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	query = strings.TrimSpace(query)
	title := " history "
	if query != "" {
		title = fmt.Sprintf(" history: %s ", query)
	}
	lines := []string{styles.completionTitle().Render(title)}
	if len(matches) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no matches")), "\n")
	}
	if selected < 0 || selected >= len(matches) {
		selected = 0
	}
	for index, match := range matches {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(match, 100)))
	}
	return strings.Join(lines, "\n")
}

func renderQueuedPrompts(queued []queuedPrompt, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	if len(queued) == 0 {
		return ""
	}
	lines := []string{styles.completionTitle().Render(fmt.Sprintf(" queued prompts: %d ", len(queued)))}
	start := 0
	if len(queued) > 3 {
		start = len(queued) - 3
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  ... %d earlier", start)))
	}
	for index := start; index < len(queued); index++ {
		lines = append(lines, styles.completion().Render(fmt.Sprintf("  %d. %s", index+1, truncateForComposer(queuedPromptDisplay(queued[index]), 100))))
	}
	return strings.Join(lines, "\n")
}

func queuedPromptDisplay(prompt queuedPrompt) string {
	text := strings.TrimSpace(prompt.Text)
	if isBashModeInput(text) {
		command := bashModeCommand(text)
		if command == "" {
			text = "bash:"
		} else {
			text = "bash: " + command
		}
	}
	if len(prompt.Attachments) == 0 {
		return text
	}
	attachmentLabel := fmt.Sprintf("%d %s", len(prompt.Attachments), plural("attachment", len(prompt.Attachments)))
	if text == "" {
		return attachmentLabel
	}
	return fmt.Sprintf("%s [%s]", text, attachmentLabel)
}

func (m *model) openModelPicker() {
	if len(m.modelOptions) == 0 || m.selectModel == nil {
		m.status = "no model picker"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.modelPicker = true
	m.modelPickerSelected = indexOfModelOption(m.modelOptions, m.currentModel)
	m.status = "model picker"
}

func (m *model) openThemePicker() {
	if m.selectTheme == nil {
		m.status = "no theme picker"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	m.matches = nil
	m.selected = 0
	m.themePicker = true
	m.themePickerOriginal = m.theme
	m.themePickerSelected = indexOfTheme(ThemeNames(), m.theme)
	m.status = "theme picker"
	m.applyTheme()
}

func (m *model) closeThemePicker(restore bool) {
	if restore && m.themePickerOriginal != "" {
		m.theme = m.themePickerOriginal
	}
	m.themePicker = false
	m.themePickerSelected = 0
	m.themePickerOriginal = ""
	m.status = m.mode()
	m.applyTheme()
}

func (m *model) moveThemePicker(delta int) {
	options := ThemeNames()
	if len(options) == 0 {
		return
	}
	m.themePickerSelected = (m.themePickerSelected + delta + len(options)) % len(options)
	m.theme = options[m.themePickerSelected]
	m.status = "theme preview"
	m.applyTheme()
}

func (m *model) setThemePickerIndex(index int) {
	options := ThemeNames()
	if len(options) == 0 {
		return
	}
	m.themePickerSelected = clampIndex(index, len(options))
	m.theme = options[m.themePickerSelected]
	m.status = "theme preview"
	m.applyTheme()
}

func (m model) acceptThemePicker() (tea.Model, tea.Cmd) {
	options := ThemeNames()
	if len(options) == 0 || m.selectTheme == nil {
		m.closeThemePicker(true)
		return m, nil
	}
	selected := options[clampIndex(m.themePickerSelected, len(options))]
	previous := m.themePickerOriginal
	m.theme = selected
	m.themePicker = false
	m.themePickerOriginal = ""
	m.status = "saving theme"
	m.applyTheme()
	return m, runThemeSelectCommand(m.ctx, m.selectTheme, selected, previous)
}

func (m *model) applyTheme() {
	name, ok := NormalizeThemeName(m.theme)
	if !ok {
		name = "auto"
	}
	m.theme = name
	stylesForTheme(name).applyTextarea(&m.textarea)
	m.refreshViewport()
}

func (m *model) closeModelPicker() {
	m.modelPicker = false
	m.modelPickerSelected = 0
	m.status = m.mode()
}

func (m *model) moveModelPicker(delta int) {
	if len(m.modelOptions) == 0 {
		return
	}
	m.modelPickerSelected = (m.modelPickerSelected + delta + len(m.modelOptions)) % len(m.modelOptions)
	m.status = "model picker"
}

func (m *model) setModelPickerIndex(index int) {
	if len(m.modelOptions) == 0 {
		return
	}
	m.modelPickerSelected = clampIndex(index, len(m.modelOptions))
	m.status = "model picker"
}

func (m model) acceptModelPicker() (tea.Model, tea.Cmd) {
	if len(m.modelOptions) == 0 || m.selectModel == nil {
		m.closeModelPicker()
		return m, nil
	}
	if m.modelPickerSelected < 0 || m.modelPickerSelected >= len(m.modelOptions) {
		m.modelPickerSelected = 0
	}
	selected := m.modelOptions[m.modelPickerSelected]
	m.modelPicker = false
	m.currentModel = selected
	m.status = "selecting model"
	return m, runModelSelectCommand(m.ctx, m.selectModel, selected)
}

func (m *model) applyRuntimeControlResult(result RuntimeControlResult) {
	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = "Runtime Control"
	}
	status := strings.TrimSpace(result.Status)
	if status == "" {
		status = strings.ToLower(title)
	}
	lines := []string{title}
	for _, line := range result.Lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 1 {
		lines = append(lines, status)
	}
	for _, line := range result.Lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && strings.EqualFold(strings.TrimRight(fields[0], ":"), "model") {
			m.currentModel = fields[1]
			break
		}
	}
	if result.VimEnabled != nil {
		m.vimEnabled = *result.VimEnabled
		m.vimNormal = false
		m.vimOperator = ""
	}
	m.runtimeBadges = mergeRuntimeBadges(m.runtimeBadges, runtimeBadgesFromResult(result))
	m.status = status
	if m.updateCommandViewValue(result.Setting, result.Value) {
		return
	}
	m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: strings.Join(lines, "\n")})
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) updateCommandViewValue(setting string, value string) bool {
	if m.commandView == nil {
		return false
	}
	setting = strings.ToLower(strings.TrimSpace(setting))
	if setting == "" {
		return false
	}
	tabIndex := clampIndex(m.commandViewTab, len(m.commandView.Tabs))
	if tabIndex < 0 || tabIndex >= len(m.commandView.Tabs) {
		return false
	}
	items := m.commandView.Tabs[tabIndex].Items
	itemIndex := clampIndex(m.commandViewItem, len(items))
	if itemIndex < 0 || itemIndex >= len(items) || !strings.EqualFold(strings.TrimSpace(items[itemIndex].Action), setting) {
		return false
	}
	m.commandView.Tabs[tabIndex].Items[itemIndex].Value = strings.TrimSpace(value)
	return true
}

func (m model) runtimeStatusBadges() []string {
	badges := []string{}
	if current := strings.TrimSpace(m.currentModel); current != "" {
		badges = append(badges, "model: "+current)
	}
	badges = append(badges, m.runtimeBadges...)
	if m.vimEnabled {
		mode := "insert"
		if m.vimNormal {
			mode = "normal"
		}
		badges = append(badges, "vim: "+mode)
	}
	return normalizeRuntimeBadges(badges)
}

func runtimeBadgesFromResult(result RuntimeControlResult) []string {
	badges := normalizeRuntimeBadges(result.Badges)
	if len(badges) > 0 {
		return badges
	}
	out := []string{}
	for _, line := range result.Lines {
		key, value, ok := splitRuntimeStatusLine(line)
		if !ok {
			continue
		}
		switch key {
		case "fast mode":
			out = append(out, "fast: "+value)
		case "reasoning":
			out = append(out, "thinking: "+value)
		case "model":
			out = append(out, "model: "+value)
		}
	}
	return normalizeRuntimeBadges(out)
}

func splitRuntimeStatusLine(line string) (string, string, bool) {
	before, after, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(before))
	value := strings.TrimSpace(after)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func mergeRuntimeBadges(existing []string, updates []string) []string {
	out := normalizeRuntimeBadges(existing)
	for _, update := range normalizeRuntimeBadges(updates) {
		key := runtimeBadgeKey(update)
		replaced := false
		for index, badge := range out {
			if runtimeBadgeKey(badge) == key {
				out[index] = update
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, update)
		}
	}
	return out
}

func normalizeRuntimeBadges(badges []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, badge := range badges {
		badge = strings.Join(strings.Fields(strings.TrimSpace(badge)), " ")
		if badge == "" {
			continue
		}
		key := runtimeBadgeKey(badge)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, badge)
	}
	return out
}

func runtimeBadgeKey(badge string) string {
	badge = strings.TrimSpace(badge)
	before, _, ok := strings.Cut(badge, ":")
	if ok && strings.TrimSpace(before) != "" {
		return strings.ToLower(strings.TrimSpace(before))
	}
	return strings.ToLower(badge)
}

func normalizeModelOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		key := strings.ToLower(option)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, option)
	}
	return out
}

func indexOfModelOption(options []string, current string) int {
	current = strings.TrimSpace(current)
	if current == "" {
		return 0
	}
	lower := strings.ToLower(current)
	for index, option := range options {
		if strings.EqualFold(option, current) || strings.EqualFold(option, lower) {
			return index
		}
	}
	return 0
}

func renderModelPicker(options []string, current string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" model picker ")}
	if len(options) == 0 {
		return strings.Join(append(lines, styles.completion().Render("  no models configured")), "\n")
	}
	if selected < 0 || selected >= len(options) {
		selected = 0
	}
	for index, option := range options {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		suffix := ""
		if strings.EqualFold(option, current) {
			suffix = "  current"
		}
		lines = append(lines, style.Render(prefix+truncateForComposer(option+suffix, limit)))
	}
	lines = append(lines, styles.completion().Render("  Enter select · Up/Down move · Esc cancel"))
	return strings.Join(lines, "\n")
}

func renderThemePicker(current string, selected int, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	options := ThemeNames()
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	lines := []string{styles.completionTitle().Render(" theme ")}
	for index, option := range options {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "❯ "
			style = styles.selectedCompletion()
		}
		suffix := ""
		if option == current {
			suffix = "  preview"
		}
		labelLimit := max(1, limit-lipgloss.Width(prefix))
		lines = append(lines, style.Render(prefix+truncateForComposer(themeLabel(option)+suffix, labelLimit)))
	}
	lines = append(lines, styles.completion().Render("  Enter save · Up/Down preview · Esc restore"))
	if width > 0 {
		for index := range lines {
			lines[index] = truncateFooterLine(lines[index], max(12, width))
		}
	}
	return strings.Join(lines, "\n")
}

var messageActionLabels = []string{
	"copy to composer",
	"copy to clipboard",
	"quote in composer",
	"stash message",
	"restore before turn",
	"fork before turn",
	"summarize from turn",
	"summarize up to turn",
}

func (m *model) openMessageActions() {
	target := m.lastTranscriptIndex()
	if target < 0 {
		m.status = "no messages"
		return
	}
	if m.helpOpen {
		m.helpOpen = false
		m.refreshViewport()
	}
	m.matches = nil
	m.selected = 0
	m.messageActions = true
	m.messageActionTarget = target
	m.messageActionSelected = 0
	m.status = "message actions"
}

func (m model) hasUserTranscriptEntry() bool {
	return len(m.messageActionRoleTargets("user")) > 0
}

func (m *model) openLatestUserMessageActions() {
	targets := m.messageActionRoleTargets("user")
	if len(targets) == 0 {
		return
	}
	m.openMessageActions()
	m.messageActionTarget = targets[len(targets)-1]
}

func (m *model) discardLatestSlashInput() {
	if len(m.transcript) == 0 {
		return
	}
	last := m.transcript[len(m.transcript)-1]
	if strings.EqualFold(last.Role, "user") && strings.HasPrefix(strings.TrimSpace(last.Text), "/") {
		m.transcript = m.transcript[:len(m.transcript)-1]
	}
}

func (m *model) openPermissionSettings(settings PermissionSettings) {
	settings.Modes = append([]PermissionModeOption(nil), settings.Modes...)
	settings.Allow = append([]string(nil), settings.Allow...)
	settings.Ask = append([]string(nil), settings.Ask...)
	settings.Deny = append([]string(nil), settings.Deny...)
	m.closeInteractivePanels()
	m.permissionSettings = &settings
	m.permissionModeSelected = 0
	for index, option := range settings.Modes {
		if option.Current {
			m.permissionModeSelected = index
			break
		}
	}
	m.status = "permissions"
}

func (m *model) closePermissionSettings() {
	m.permissionSettings = nil
	m.permissionModeSelected = 0
	m.status = m.mode()
}

func (m *model) movePermissionSettings(delta int) {
	if m.permissionSettings == nil || len(m.permissionSettings.Modes) == 0 {
		return
	}
	m.permissionModeSelected = (m.permissionModeSelected + delta + len(m.permissionSettings.Modes)) % len(m.permissionSettings.Modes)
	m.status = "permissions"
}

func (m model) acceptPermissionSettings() (tea.Model, tea.Cmd) {
	if m.permissionSettings == nil || len(m.permissionSettings.Modes) == 0 || m.selectPermissionMode == nil {
		m.closePermissionSettings()
		return m, nil
	}
	selected := m.permissionSettings.Modes[clampIndex(m.permissionModeSelected, len(m.permissionSettings.Modes))]
	m.closePermissionSettings()
	m.status = "updating permissions"
	return m, runPermissionModeSelectCommand(m.ctx, m.selectPermissionMode, selected.Name)
}

func (m *model) openInformation(view InformationView) {
	view.Title = strings.TrimSpace(view.Title)
	view.Lines = append([]string(nil), view.Lines...)
	m.closeInteractivePanels()
	m.information = &view
	m.informationOffset = 0
	m.status = strings.ToLower(view.Title)
	if m.status == "" {
		m.status = "information"
	}
}

func (m *model) closeInformation() {
	m.information = nil
	m.informationOffset = 0
	m.status = m.mode()
}

func (m *model) moveInformation(delta int) {
	if m.information == nil {
		return
	}
	visible := informationVisibleLines(m.height)
	maximum := max(0, len(m.information.Lines)-visible)
	m.informationOffset = min(max(0, m.informationOffset+delta), maximum)
}

func (m *model) openCommandView(view CommandView) {
	view.Title = strings.TrimSpace(view.Title)
	view.Tabs = cloneCommandViewTabs(view.Tabs)
	m.closeInteractivePanels()
	m.commandView = &view
	m.commandViewTab = clampIndex(view.SelectedTab, len(view.Tabs))
	m.commandViewItem = 0
	if len(view.Tabs) > 0 {
		m.commandViewItem = clampIndex(view.SelectedItem, len(view.Tabs[m.commandViewTab].Items))
	}
	m.commandViewOffset = 0
	m.status = strings.ToLower(view.Title)
	if m.status == "" {
		m.status = "settings"
	}
}

func cloneCommandViewTabs(tabs []CommandViewTab) []CommandViewTab {
	cloned := make([]CommandViewTab, len(tabs))
	for index, tab := range tabs {
		cloned[index] = CommandViewTab{
			Title:          strings.TrimSpace(tab.Title),
			Lines:          append([]string(nil), tab.Lines...),
			Items:          append([]CommandViewItem(nil), tab.Items...),
			RefreshCommand: strings.TrimSpace(tab.RefreshCommand),
		}
	}
	return cloned
}

func (m *model) closeCommandView() {
	m.commandView = nil
	m.commandViewTab = 0
	m.commandViewItem = 0
	m.commandViewOffset = 0
	m.status = m.mode()
}

func (m *model) moveCommandViewTab(delta int) {
	if m.commandView == nil || len(m.commandView.Tabs) == 0 {
		return
	}
	m.commandViewTab = (m.commandViewTab + delta + len(m.commandView.Tabs)) % len(m.commandView.Tabs)
	m.commandViewItem = 0
	m.commandViewOffset = 0
}

func (m *model) moveCommandViewSelection(delta int) {
	tab, ok := m.currentCommandViewTab()
	if !ok {
		return
	}
	if len(tab.Items) > 0 {
		m.commandViewItem = (m.commandViewItem + delta + len(tab.Items)) % len(tab.Items)
		visible := commandViewItemCapacity(tab, m.height)
		if m.commandViewItem < m.commandViewOffset {
			m.commandViewOffset = m.commandViewItem
		}
		if m.commandViewItem >= m.commandViewOffset+visible {
			m.commandViewOffset = m.commandViewItem - visible + 1
		}
		m.commandViewOffset = min(max(0, m.commandViewOffset), max(0, len(tab.Items)-visible))
		return
	}
	visible := commandViewVisibleLines(m.height)
	maximum := max(0, len(tab.Lines)-visible)
	m.commandViewOffset = min(max(0, m.commandViewOffset+delta), maximum)
}

func (m *model) currentCommandViewTab() (CommandViewTab, bool) {
	if m.commandView == nil || len(m.commandView.Tabs) == 0 {
		return CommandViewTab{}, false
	}
	return m.commandView.Tabs[clampIndex(m.commandViewTab, len(m.commandView.Tabs))], true
}

func (m model) updateCommandView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	secondaryKey := key
	if secondaryKey == " " {
		secondaryKey = "space"
	}
	if tab, ok := m.currentCommandViewTab(); ok && len(tab.Items) > 0 {
		item := tab.Items[clampIndex(m.commandViewItem, len(tab.Items))]
		if commandViewSecondaryKey(item) == secondaryKey && strings.TrimSpace(item.SecondaryCommand) != "" {
			return m.acceptCommandViewItem(true)
		}
	}
	switch key {
	case "ctrl+c", "esc":
		m.closeCommandView()
		return m, nil
	case "left", "shift+tab":
		m.moveCommandViewTab(-1)
		return m, nil
	case "right", "tab":
		m.moveCommandViewTab(1)
		return m, nil
	case "up", "ctrl+p", "k":
		m.moveCommandViewSelection(-1)
		return m, nil
	case "down", "ctrl+n", "j":
		m.moveCommandViewSelection(1)
		return m, nil
	case "pgup":
		m.moveCommandViewSelection(-commandViewVisibleLines(m.height))
		return m, nil
	case "pgdown":
		m.moveCommandViewSelection(commandViewVisibleLines(m.height))
		return m, nil
	case " ", "space":
		m.moveCommandViewSelection(commandViewVisibleLines(m.height))
		return m, nil
	case "r":
		tab, ok := m.currentCommandViewTab()
		if !ok || strings.TrimSpace(tab.RefreshCommand) == "" {
			return m, nil
		}
		return m.runCommandViewCommand(tab.RefreshCommand)
	case "home":
		m.commandViewItem = 0
		m.commandViewOffset = 0
		return m, nil
	case "end":
		tab, ok := m.currentCommandViewTab()
		if ok && len(tab.Items) > 0 {
			m.commandViewItem = len(tab.Items) - 1
			m.commandViewOffset = max(0, len(tab.Items)-commandViewItemCapacity(tab, m.height))
		} else if ok {
			m.commandViewOffset = max(0, len(tab.Lines)-commandViewVisibleLines(m.height))
		}
		return m, nil
	case "enter":
		return m.acceptCommandViewItem(false)
	default:
		return m, nil
	}
}

func (m model) acceptCommandViewItem(secondary bool) (tea.Model, tea.Cmd) {
	tab, ok := m.currentCommandViewTab()
	if !ok || len(tab.Items) == 0 {
		return m, nil
	}
	item := tab.Items[clampIndex(m.commandViewItem, len(tab.Items))]
	action := strings.ToLower(strings.TrimSpace(item.Action))
	rawCommand := item.Command
	if secondary {
		rawCommand = item.SecondaryCommand
		if strings.TrimSpace(rawCommand) == "" {
			return m, nil
		}
		action = strings.ToLower(strings.TrimSpace(item.SecondaryAction))
	}
	command := strings.TrimSpace(rawCommand)
	switch action {
	case "prefill":
		m.closeCommandView()
		m.textarea.SetValue(strings.TrimLeft(rawCommand, " \t\r\n"))
		m.textarea.CursorEnd()
		m.textarea.Focus()
		return m, nil
	case "model":
		m.closeCommandView()
		m.openModelPicker()
		return m, nil
	case "theme":
		m.closeCommandView()
		m.openThemePicker()
		return m, nil
	case "fast":
		if m.toggleFast == nil {
			return m, nil
		}
		m.status = "updating fast mode"
		return m, runRuntimeControlCommand(m.ctx, m.toggleFast)
	case "thinking":
		if m.toggleThinking == nil {
			return m, nil
		}
		m.status = "updating thinking"
		return m, runRuntimeControlCommand(m.ctx, m.toggleThinking)
	case "vim":
		if m.toggleVim == nil {
			return m, nil
		}
		m.status = "updating vim mode"
		return m, runRuntimeControlCommand(m.ctx, m.toggleVim)
	}
	if command == "" {
		return m, nil
	}
	return m.runCommandViewCommand(command)
}

func (m model) runCommandViewCommand(command string) (tea.Model, tea.Cmd) {
	command = strings.TrimSpace(command)
	if command == "" {
		return m, nil
	}
	m.closeCommandView()
	m.textarea.SetValue(command)
	return m.startInput(command)
}

func commandViewSecondaryKey(item CommandViewItem) string {
	key := strings.ToLower(strings.TrimSpace(item.SecondaryKey))
	if key == "" || key == " " {
		return "space"
	}
	return key
}

func commandViewSecondaryKeyLabel(item CommandViewItem) string {
	key := commandViewSecondaryKey(item)
	if key == "space" {
		return "Space"
	}
	return strings.ToUpper(key)
}

func (m *model) openExportDialog(dialog ExportDialog) {
	m.closeInteractivePanels()
	dialog.DefaultFilename = strings.TrimSpace(dialog.DefaultFilename)
	if dialog.DefaultFilename == "" {
		dialog.DefaultFilename = "conversation.md"
	}
	m.exportComposerDraft = m.textarea.Value()
	m.textarea.SetValue("")
	m.textarea.Focus()
	m.exportDialog = &dialog
	m.exportDialogSelected = 0
	m.exportFilenameInput = false
	m.status = "export"
}

func (m *model) closeExportDialog() {
	draft := m.exportComposerDraft
	m.exportDialog = nil
	m.exportDialogSelected = 0
	m.exportFilenameInput = false
	m.exportComposerDraft = ""
	m.textarea.SetValue(draft)
	m.textarea.CursorEnd()
	m.textarea.Focus()
	m.status = m.mode()
}

func (m *model) openTextInputDialog(dialog TextInputDialog) {
	dialog.Title = strings.TrimSpace(dialog.Title)
	dialog.Prompt = strings.TrimSpace(dialog.Prompt)
	dialog.Action = strings.TrimSpace(dialog.Action)
	m.closeInteractivePanels()
	m.textInputComposerDraft = m.textarea.Value()
	m.textarea.SetValue(dialog.InitialValue)
	m.textarea.CursorEnd()
	m.textarea.Focus()
	m.textInputDialog = &dialog
	m.status = strings.ToLower(dialog.Title)
	if m.status == "" {
		m.status = "input"
	}
}

func (m *model) closeTextInputDialog() {
	draft := m.textInputComposerDraft
	m.textInputDialog = nil
	m.textInputComposerDraft = ""
	m.textarea.SetValue(draft)
	m.textarea.CursorEnd()
	m.textarea.Focus()
	m.status = m.mode()
}

func (m model) updateTextInputDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.textInputDialog == nil {
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		m.closeTextInputDialog()
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.textarea.Value())
		if value == "" {
			m.status = "value required"
			return m, nil
		}
		if m.submitTextInput == nil {
			m.closeTextInputDialog()
			m.status = "input action unavailable"
			return m, nil
		}
		action := m.textInputDialog.Action
		m.closeTextInputDialog()
		m.status = "updating"
		return m, runTextInputSubmitCommand(m.ctx, m.submitTextInput, action, value)
	default:
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}
}

func (m model) updateExportDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.exportDialog == nil {
		return m, nil
	}
	if m.exportFilenameInput {
		switch msg.String() {
		case "ctrl+c":
			m.closeExportDialog()
			return m, nil
		case "esc":
			m.exportFilenameInput = false
			m.textarea.SetValue("")
			m.status = "export"
			return m, nil
		case "enter":
			filename := strings.TrimSpace(m.textarea.Value())
			if filename == "" {
				m.status = "filename required"
				return m, nil
			}
			if m.exportConversationTo == nil {
				m.closeExportDialog()
				m.status = "export unavailable"
				return m, nil
			}
			m.closeExportDialog()
			m.status = "exporting"
			return m, runConversationExportCommand(m.ctx, m.exportConversationTo, filename)
		default:
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c", "esc":
		m.closeExportDialog()
		return m, nil
	case "up", "ctrl+p", "k", "shift+tab":
		m.exportDialogSelected = (m.exportDialogSelected + 1) % 2
		return m, nil
	case "down", "ctrl+n", "j", "tab":
		m.exportDialogSelected = (m.exportDialogSelected + 1) % 2
		return m, nil
	case "home":
		m.exportDialogSelected = 0
		return m, nil
	case "end":
		m.exportDialogSelected = 1
		return m, nil
	case "enter":
		if m.exportDialogSelected == 0 {
			if m.copyConversation == nil {
				m.closeExportDialog()
				m.status = "copy unavailable"
				return m, nil
			}
			m.closeExportDialog()
			m.status = "copying"
			return m, runRuntimeControlCommand(m.ctx, m.copyConversation)
		}
		m.exportFilenameInput = true
		m.textarea.SetValue(m.exportDialog.DefaultFilename)
		m.textarea.CursorEnd()
		m.textarea.Focus()
		m.status = "export filename"
		return m, nil
	default:
		return m, nil
	}
}

func (m model) runSlashRuntimeAction(action string) (tea.Model, tea.Cmd) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "compact":
		if m.compactSession == nil {
			m.status = "compact unavailable"
			return m, nil
		}
		m.status = "compacting"
		return m, runRuntimeControlCommand(m.ctx, m.compactSession)
	case "copy":
		if m.copyConversation == nil {
			m.status = "copy unavailable"
			return m, nil
		}
		m.status = "copying"
		return m, runRuntimeControlCommand(m.ctx, m.copyConversation)
	default:
		m.status = "unsupported action"
		return m, nil
	}
}

func (m *model) closeInteractivePanels() {
	if m.exportDialog != nil {
		m.textarea.SetValue(m.exportComposerDraft)
		m.textarea.CursorEnd()
	}
	if m.textInputDialog != nil {
		m.textarea.SetValue(m.textInputComposerDraft)
		m.textarea.CursorEnd()
	}
	m.helpOpen = false
	m.searchOpen = false
	m.quickOpen = false
	m.globalSearch = false
	m.todosOpen = false
	m.modelPicker = false
	m.themePicker = false
	m.messageActions = false
	m.attachmentsOpen = false
	m.diffDialog = false
	m.diffSources = nil
	m.diffSourceSelected = 0
	m.diffFileSelected = 0
	m.diffDetail = false
	m.sessionPicker = nil
	m.permissionSettings = nil
	m.permissionModeSelected = 0
	m.information = nil
	m.informationOffset = 0
	m.commandView = nil
	m.commandViewTab = 0
	m.commandViewItem = 0
	m.commandViewOffset = 0
	m.exportDialog = nil
	m.exportDialogSelected = 0
	m.exportFilenameInput = false
	m.exportComposerDraft = ""
	m.textInputDialog = nil
	m.textInputComposerDraft = ""
	m.matches = nil
	m.selected = 0
}

func (m *model) openSessionPicker(choices []SessionChoice) {
	picker := newSessionPickerModelWithTheme(choices, m.theme)
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	picker.width = max(12, width)
	picker.height = max(6, height)
	m.sessionPicker = &picker
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.status = "resume"
}

func (m model) updateSessionPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessionPicker == nil {
		return m, nil
	}
	updated, _ := m.sessionPicker.Update(msg)
	picker, ok := updated.(sessionPickerModel)
	if !ok {
		return m, nil
	}
	if picker.canceled {
		m.sessionPicker = nil
		m.status = "resume cancelled"
		return m, nil
	}
	if picker.selectedID == "" {
		m.sessionPicker = &picker
		return m, nil
	}

	m.sessionPicker = nil
	if m.slash == nil {
		m.status = "resume error"
		m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: "resume is unavailable"})
		m.refreshViewport()
		return m, m.flushInlineTranscript()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.busy = true
	m.status = "resuming"
	return m, runSlashCommand(ctx, m.slash, "/resume "+picker.selectedID)
}

func (m *model) applySessionState(state SessionState) {
	entries := transcriptEntries(state.Entries)
	if len(entries) == 0 && strings.TrimSpace(state.ID) != "" {
		entries = []transcriptEntry{{Role: "system", Text: "Session " + strings.TrimSpace(state.ID)}}
	}
	if len(entries) == 0 {
		entries = defaultTranscriptEntries()
	}
	m.transcript = entries
	m.streamingIndex = -1
	m.messageActions = false
	m.messageActionTarget = 0
	m.messageActionSelected = 0
	m.setHistory(state.History)
	if state.Candidates != nil {
		m.candidates = append([]string(nil), state.Candidates...)
	}
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	if m.inline {
		m.printedEntries = 0
		m.initialPrint = ""
	}
	m.refreshCompletionMenu()
	m.refreshViewport()
	m.viewport.GotoBottom()
}

func (m *model) closeMessageActions() {
	m.messageActions = false
	m.messageActionTarget = 0
	m.messageActionSelected = 0
	m.status = m.mode()
}

func (m *model) moveMessageAction(delta int) {
	if len(messageActionLabels) == 0 {
		return
	}
	m.messageActionSelected = (m.messageActionSelected + delta + len(messageActionLabels)) % len(messageActionLabels)
	m.status = "message actions"
}

func (m *model) setMessageActionIndex(index int) {
	if len(messageActionLabels) == 0 {
		return
	}
	m.messageActionSelected = clampIndex(index, len(messageActionLabels))
	m.status = "message actions"
}

func (m *model) moveMessageActionTarget(delta int) {
	targets := m.messageActionTargets()
	if len(targets) == 0 {
		m.status = "no messages"
		return
	}
	position := 0
	for index, target := range targets {
		if target == m.messageActionTarget {
			position = index
			break
		}
	}
	next := (position + delta + len(targets)) % len(targets)
	m.messageActionTarget = targets[next]
	m.status = "message actions"
}

func (m *model) moveMessageActionUserTarget(delta int) {
	targets := m.messageActionRoleTargets("user")
	if len(targets) == 0 {
		m.status = "no user messages"
		return
	}
	current := m.messageActionTarget
	next := targets[0]
	if delta < 0 {
		next = targets[len(targets)-1]
		for index := len(targets) - 1; index >= 0; index-- {
			if targets[index] < current {
				next = targets[index]
				break
			}
		}
	} else {
		for _, target := range targets {
			if target > current {
				next = target
				break
			}
		}
	}
	m.messageActionTarget = next
	m.status = "message actions"
}

func (m model) applyMessageAction() (tea.Model, tea.Cmd) {
	entry := m.messageActionEntry()
	text := strings.TrimSpace(entry.Text)
	if text == "" {
		m.closeMessageActions()
		return m, nil
	}
	switch m.messageActionSelected {
	case 1:
		if m.copyMessage == nil {
			m.status = "copy unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "copying message"
		return m, runMessageCopyCommand(m.ctx, m.copyMessage, text)
	case 2:
		m.pushComposerUndo()
		m.textarea.SetValue(insertWithComposerSpacing(m.textarea.Value(), quoteMessageText(text)))
		m.textarea.CursorEnd()
		m.status = "message quoted"
	case 3:
		m.stashedPrompt = &composerStash{Text: text}
		m.status = "message stashed"
	case 4:
		if m.restoreConversation == nil {
			m.status = "restore unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "restore unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "restoring"
		return m, runConversationRestoreCommand(m.ctx, m.restoreConversation, keepMessages)
	case 5:
		if m.forkConversation == nil {
			m.status = "fork unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "fork unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "forking"
		return m, runConversationForkCommand(m.ctx, m.forkConversation, keepMessages)
	case 6:
		if m.summarizeConversation == nil {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "summarizing"
		return m, runConversationSummarizeCommand(m.ctx, m.summarizeConversation, keepMessages)
	case 7:
		if m.summarizeUpToConversation == nil {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		keepMessages := m.restoreMessageKeepCount()
		if keepMessages < 0 {
			m.status = "summarize unavailable"
			m.messageActions = false
			m.messageActionSelected = 0
			return m, nil
		}
		m.messageActions = false
		m.messageActionSelected = 0
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "summarizing"
		return m, runConversationSummarizeCommand(m.ctx, m.summarizeUpToConversation, keepMessages)
	default:
		m.pushComposerUndo()
		m.textarea.SetValue(text)
		m.textarea.CursorEnd()
		m.status = "message copied"
	}
	m.messageActions = false
	m.messageActionSelected = 0
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.refreshCompletionMenu()
	return m, nil
}

func (m model) lastTranscriptIndex() int {
	for index := len(m.transcript) - 1; index >= 0; index-- {
		if strings.TrimSpace(m.transcript[index].Text) != "" {
			return index
		}
	}
	return -1
}

func (m model) messageActionEntry() transcriptEntry {
	if m.messageActionTarget >= 0 && m.messageActionTarget < len(m.transcript) {
		return m.transcript[m.messageActionTarget]
	}
	return transcriptEntry{}
}

func (m model) messageActionTargetPosition() (int, int) {
	targets := m.messageActionTargets()
	for index, target := range targets {
		if target == m.messageActionTarget {
			return index + 1, len(targets)
		}
	}
	if len(targets) == 0 {
		return 0, 0
	}
	return 1, len(targets)
}

func (m model) messageActionTargets() []int {
	targets := []int{}
	for index, entry := range m.transcript {
		if strings.TrimSpace(entry.Text) != "" {
			targets = append(targets, index)
		}
	}
	return targets
}

func (m model) messageActionRoleTargets(role string) []int {
	role = strings.TrimSpace(role)
	targets := []int{}
	for index, entry := range m.transcript {
		if strings.TrimSpace(entry.Text) != "" && strings.EqualFold(entry.Role, role) {
			targets = append(targets, index)
		}
	}
	return targets
}

func (m model) restoreMessageKeepCount() int {
	if m.messageActionTarget < 0 || m.messageActionTarget >= len(m.transcript) {
		return -1
	}
	restoreTarget := m.messageActionTarget
	if strings.EqualFold(m.transcript[restoreTarget].Role, "assistant") {
		for index := restoreTarget - 1; index >= 0; index-- {
			if strings.EqualFold(m.transcript[index].Role, "user") {
				restoreTarget = index
				break
			}
		}
	}
	keep := 0
	for index := 0; index < restoreTarget && index < len(m.transcript); index++ {
		if transcriptEntryCountsAsSessionMessage(m.transcript[index]) {
			keep++
		}
	}
	return keep
}

func transcriptEntryCountsAsSessionMessage(entry transcriptEntry) bool {
	return strings.EqualFold(entry.Role, "user") || strings.EqualFold(entry.Role, "assistant")
}

func quoteMessageText(text string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	for index, line := range lines {
		lines[index] = "> " + strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func renderMessageActions(entry transcriptEntry, selected int, width int, targetPos int, targetCount int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := 120
	if width > 0 {
		limit = max(12, width-8)
	}
	role := strings.TrimSpace(entry.Role)
	if role == "" {
		role = "message"
	}
	title := " message actions "
	if targetCount > 1 && targetPos > 0 {
		title = fmt.Sprintf(" message actions %d/%d ", targetPos, targetCount)
	}
	lines := []string{styles.completionTitle().Render(title)}
	summary := strings.Join(strings.Fields(entry.Text), " ")
	if summary == "" {
		summary = "(empty message)"
	}
	lines = append(lines, styles.completion().Render("  "+truncateForComposer(role+": "+summary, limit)))
	if selected < 0 || selected >= len(messageActionLabels) {
		selected = 0
	}
	for index, action := range messageActionLabels {
		prefix := "  "
		style := styles.completion()
		if index == selected {
			prefix = "> "
			style = styles.selectedCompletion()
		}
		lines = append(lines, style.Render(prefix+action))
	}
	hint := "  Enter apply · c copy · Up/Down choose · Esc cancel"
	if targetCount > 1 {
		hint = "  Enter apply · c copy · Up/Down choose · Left/Right message · Esc cancel"
	}
	lines = append(lines, styles.completion().Render(hint))
	return strings.Join(lines, "\n")
}
