package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newPromptTextarea(input string) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask codog..."
	ta.Prompt = "❯ "
	ta.ShowLineNumbers = false
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.CharLimit = 16000
	ta.SetValue(input)
	return ta
}

func defaultTranscriptEntries() []transcriptEntry {
	return []transcriptEntry{
		{Role: "welcome"},
	}
}

func (m model) Init() tea.Cmd {
	commands := []tea.Cmd{}
	if strings.TrimSpace(m.initialPrint) != "" {
		commands = append(commands, tea.Println(m.initialPrint))
	}
	if m.initialPrompt != "" {
		commands = append(commands, func() tea.Msg {
			return initialPromptMsg{Value: m.initialPrompt}
		})
	}
	return tea.Batch(textarea.Blink, sequenceCommands(commands...))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case initialPromptMsg:
		return m.updateInitialPrompt(msg)
	case exitPendingExpiredMsg:
		return m.updateExitPendingExpired(msg)
	case turnDoneMsg:
		return m.updateTurnDone(msg)
	case externalEditorDoneMsg:
		return m.updateExternalEditorDone(msg)
	case pasteDoneMsg:
		return m.updatePasteDone(msg)
	case backgroundDoneMsg:
		return m.updateBackgroundDone(msg)
	case taskBoardDoneMsg:
		return m.updateTaskBoardDone(msg)
	case todoListDoneMsg:
		return m.updateTodoListDone(msg)
	case runtimeControlDoneMsg:
		return m.updateRuntimeControlDone(msg)
	case permissionModeSelectDoneMsg:
		return m.updatePermissionModeSelectDone(msg)
	case themeSelectDoneMsg:
		return m.updateThemeSelectDone(msg)
	case turnStreamMsg:
		return m.updateTurnStream(msg)
	case tea.WindowSizeMsg:
		return m.updateWindowSize(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	default:
		return m.updateComponents(msg)
	}
}

func (m model) handlePastedInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	text := string(msg.Runes)
	if text == "" {
		return m, nil
	}
	return m.insertPastedText(text)
}

func (m model) insertPasteContent(content PasteContent) (tea.Model, tea.Cmd) {
	if content.AttachmentPath != "" {
		return m.stagePastedAttachment(content)
	}
	return m.insertPastedText(content.Text)
}

func (m model) insertPastedText(text string) (tea.Model, tea.Cmd) {
	if m.awaitingPermission {
		if !m.permissionInput {
			m.status = "permission"
			return m, nil
		}
		m.textarea.InsertString(text)
		m.status = "permission"
		return m, nil
	}
	if m.awaitingQuestion && !m.questionCustom {
		m.beginQuestionCustomInput()
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	if !m.searchOpen && !m.quickOpen && !m.globalSearch {
		m.pushComposerUndo()
	}
	m.textarea.InsertString(text)
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	if m.searchOpen {
		m.updateHistorySearch()
		return m, nil
	}
	if m.quickOpen {
		m.updateQuickOpen()
		return m, nil
	}
	if m.globalSearch {
		m.updateGlobalSearch()
		return m, nil
	}
	m.refreshCompletionMenu()
	if m.awaitingPermission {
		m.status = "permission"
	} else if m.awaitingQuestion {
		m.status = "question"
	} else if m.busy {
		m.status = "running"
	} else {
		lines := pastedLineCount(text)
		m.status = fmt.Sprintf("pasted %d %s", lines, plural("line", lines))
	}
	return m, nil
}

func (m model) stagePastedAttachment(content PasteContent) (tea.Model, tea.Cmd) {
	if m.helpOpen {
		m.helpOpen = false
	}
	added := addUniqueAttachment(&m.attachments, content.AttachmentPath)
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	label := "clipboard attachment"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(content.MediaType)), "image/") {
		label = "clipboard image"
	}
	if added {
		m.status = label + " attached"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: fmt.Sprintf("Added %s for the next prompt.\n%s", label, renderAttachmentSummary(m.attachments))})
	} else {
		m.status = "attachment already staged"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: renderAttachmentSummary(m.attachments)})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m, nil
}

func (m *model) convertTrailingBackslashToNewline() bool {
	value := m.textarea.Value()
	trimmed := strings.TrimRight(value, " \t")
	if !endsWithOddBackslashes(trimmed) {
		return false
	}
	m.pushComposerUndoValue(value)
	suffix := value[len(trimmed):]
	m.textarea.SetValue(trimmed[:len(trimmed)-1] + "\n" + suffix)
	m.textarea.CursorEnd()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "newline"
	return true
}

func endsWithOddBackslashes(value string) bool {
	count := 0
	for i := len(value) - 1; i >= 0 && value[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

func pastedLineCount(text string) int {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func plural(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func (m model) View() string {
	if m.width == 0 {
		m.layout(80, 24)
	}
	styles := stylesForTheme(m.theme)
	barWidth := max(3, m.width)
	barContentWidth := barWidth - 2
	title := styles.header().Width(barWidth).Render(truncateFooterLine(m.headerText(), barContentWidth))
	if m.inline {
		title = styles.inlineHeader().Render(truncateFooterLine(m.headerText(), barContentWidth))
	}
	body := m.viewport.View()
	if m.inline {
		body = compactViewportView(body)
	}
	composerTextarea := m.textarea
	if m.inline {
		composerTextarea.SetHeight(m.inlineComposerHeight())
	}
	composer := composerTextarea.View()
	if !m.inline {
		composer = styles.panelTitle().Render(" composer ") + "\n" + composer
	}
	composer = m.renderComposerPanels(composer, styles)
	composer = m.renderActiveComposerDialog(composer, composerTextarea, styles)
	statusText := fitFooterText(m.promptFooterText(barWidth), barContentWidth)
	status := styles.status().Width(barWidth).Render(statusText)
	if m.inline {
		statusText = fitFooterText(m.inlineFooterText(barWidth), barContentWidth)
		status = styles.inlineStatus().Render(statusText)
	}
	parts := []string{title}
	if body != "" {
		parts = append(parts, body)
	}
	parts = append(parts, composer)
	if m.sessionPicker == nil {
		parts = append(parts, status)
	}
	return strings.Join(parts, "\n")
}

func (m model) renderComposerPanels(composer string, styles themeStyles) string {
	if m.commandArgumentHint != "" || m.inlineGhostText != "" {
		composer += "\n" + renderCommandAssist(m.commandArgumentHint, m.inlineGhostText, styles)
	}
	if len(m.matches) > 0 {
		composer += "\n" + renderCompletions(m.matches, m.selected, styles)
	}
	if m.searchOpen {
		composer += "\n" + renderHistorySearch(m.searchHits, m.searchPos, m.textarea.Value(), styles)
	}
	if m.quickOpen {
		composer += "\n" + renderQuickOpen(m.quickOpenMatches, m.quickOpenSelected, m.textarea.Value(), m.width, m.quickOpenPreviewPath, m.quickOpenPreviewLines, styles)
	}
	if m.globalSearch {
		composer += "\n" + renderGlobalSearch(m.globalSearchMatches, m.globalSearchSelected, m.textarea.Value(), m.width, m.globalSearchPreviewPath, m.globalSearchPreviewLine, m.globalSearchPreviewLines, styles)
	}
	if m.todosOpen {
		composer += "\n" + renderTodosPanel(m.todoItems, m.todosLoading, m.todoErr, m.width, styles)
	}
	if m.modelPicker {
		composer += "\n" + renderModelPicker(m.modelOptions, m.currentModel, m.modelPickerSelected, m.width, styles)
	}
	if m.themePicker {
		composer += "\n" + renderThemePicker(m.theme, m.themePickerSelected, m.width, styles)
	}
	if m.permissionSettings != nil {
		composer += "\n" + renderPermissionSettings(*m.permissionSettings, m.permissionModeSelected, m.width, styles)
	}
	if m.commandView != nil {
		composer += "\n" + renderCommandView(*m.commandView, m.commandViewTab, m.commandViewItem, m.commandViewOffset, m.width, m.height, styles)
	}
	if m.information != nil {
		composer += "\n" + renderInformation(*m.information, m.informationOffset, m.width, m.height, styles)
	}
	if m.messageActions {
		targetPos, targetCount := m.messageActionTargetPosition()
		composer += "\n" + renderMessageActions(m.messageActionEntry(), m.messageActionSelected, m.width, targetPos, targetCount, styles)
	}
	if m.diffDialog {
		composer += "\n" + renderDiffDialog(m.diffSources, m.diffSourceSelected, m.diffFileSelected, m.diffDetail, m.width, styles)
	}
	if len(m.queuedPrompts) > 0 {
		composer += "\n" + renderQueuedPrompts(m.queuedPrompts, styles)
	}
	if m.attachmentsOpen {
		composer += "\n" + renderAttachmentPanel(m.attachments, m.attachmentSelected, m.width, styles)
	} else if len(m.attachments) > 0 {
		composer += "\n" + renderPendingAttachments(m.attachments, styles)
	}
	if m.stashedPrompt != nil {
		composer += "\n" + renderStashNotice(m.stashedPrompt, styles)
	}
	return composer
}

func (m model) renderActiveComposerDialog(composer string, composerTextarea textarea.Model, styles themeStyles) string {
	if m.exportDialog != nil {
		composer = renderExportDialog(*m.exportDialog, m.exportDialogSelected, m.exportFilenameInput, composerTextarea.View(), m.width, styles)
	}
	if m.textInputDialog != nil {
		composer = renderTextInputDialog(*m.textInputDialog, composerTextarea.View(), m.width, styles)
	}
	if m.sessionPicker != nil {
		composer = m.sessionPicker.View()
	} else if m.awaitingPermission && m.permissionRequest != nil {
		composer = renderPermissionRequest(*m.permissionRequest, m.permissionSelected, m.permissionInput, m.permissionInputAnswer, m.width, styles)
		if m.permissionInput {
			composer += "\n" + composerTextarea.View()
		}
	} else if m.awaitingQuestion && m.questionRequest != nil {
		composer = renderQuestionRequest(*m.questionRequest, m.questionIndex, m.questionSelected, m.questionCustom, m.questionSelections, m.questionCustomValues, m.width, styles)
		if m.questionCustom {
			composer += "\n" + composerTextarea.View()
		}
	}
	return composer
}

func (m model) headerText() string {
	prefix := "Codog TUI"
	if m.inline {
		prefix = "codog"
	}
	badges := m.runtimeStatusBadges()
	if len(badges) == 0 {
		return prefix
	}
	title := prefix + " · " + strings.Join(badges, " · ")
	width := m.width
	if width <= 0 || len([]rune(title)) <= width {
		return title
	}
	runes := []rune(title)
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func (m model) inlineComposerHeight() int {
	value := m.textarea.Value()
	if value == "" {
		return 1
	}
	width := max(1, m.textarea.Width()-lipgloss.Width(m.textarea.Prompt))
	height := 0
	for _, line := range strings.Split(value, "\n") {
		height += max(1, (lipgloss.Width(line)+width-1)/width)
	}
	return min(max(height, 1), 6)
}

func compactViewportView(view string) string {
	return strings.TrimRight(view, " \n\r\t")
}

func (m model) inlineFooterText(width int) string {
	status := strings.TrimSpace(m.status)
	hints := m.promptFooterHints(width)
	if m.exitPending {
		return strings.Join(hints, " · ")
	}
	if status == "" || strings.EqualFold(status, "ready") {
		if len(hints) == 0 {
			return "ready"
		}
		return strings.Join(hints, " · ")
	}
	if mode := permissionModeFooterLabel(m.modeLabel); mode != "" && !footerHintsContain(hints, mode) {
		status += " · " + mode
	}
	if len(hints) > 0 {
		status += " · " + strings.Join(hints, " · ")
	}
	return status
}

func (m *model) cyclePermissionMode() {
	if m.cycleMode == nil {
		return
	}
	if label := strings.TrimSpace(m.cycleMode()); label != "" {
		m.modeLabel = label
		m.status = "ready"
	}
}

func (m *model) refreshModeLabel() {
	if m.readModeLabel == nil {
		return
	}
	if label := strings.TrimSpace(m.readModeLabel()); label != "" {
		m.modeLabel = label
	}
}

type turnDoneMsg struct {
	Role               string
	Output             string
	Query              string
	Err                error
	Interrupted        bool
	Session            *SessionState
	SessionChoices     []SessionChoice
	OpenModelPicker    bool
	OpenThemePicker    bool
	OpenTodos          bool
	OpenMessageActions bool
	RuntimeAction      string
	Diff               *DiffView
	PermissionSettings *PermissionSettings
	Information        *InformationView
	CommandView        *CommandView
	ExportDialog       *ExportDialog
	TextInputDialog    *TextInputDialog
}

type initialPromptMsg struct {
	Value string
}

type exitPendingExpiredMsg struct {
	Key        string
	Generation uint64
}

func (m model) submitCurrentInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.textarea.Value())
	return m.startInput(value)
}

func (m model) startInput(value string) (tea.Model, tea.Cmd) {
	if value == "" && len(m.attachments) == 0 {
		return m, nil
	}
	if isREPLExitInput(value) {
		m.matches = nil
		m.selected = 0
		return m, tea.Quit
	}
	if isLocalHelpInput(value) {
		m.helpOpen = true
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.status = "help"
		m.refreshViewport()
		return m, nil
	}
	if isThemePickerInput(value) && m.selectTheme != nil {
		m.appendHistory(value)
		m.textarea.SetValue("")
		m.historyPos = -1
		m.openThemePicker()
		return m, nil
	}
	if m.handleAttachmentInput(value) {
		return m, nil
	}
	if isBashModeInput(value) {
		return m.startBashInput(value)
	}
	if isLocalPasteInput(value) && m.paste != nil {
		if m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
			m.status = "paste unavailable"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Finish the current turn before pasting clipboard content."})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, nil
		}
		m.appendHistory(value)
		m.textarea.SetValue("")
		m.undoStack = nil
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "pasting"
		return m, runPasteCommand(m.ctx, m.paste)
	}
	if strings.HasPrefix(value, "/") && m.slash != nil {
		ctx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		m.appendHistory(value)
		m.vimNormal = false
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.busy = true
		m.status = "running slash"
		m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, runSlashCommand(ctx, m.slash, value)
	}
	attachments := append([]string(nil), m.attachments...)
	if m.submit == nil && m.submitStream == nil && m.submitAttachments == nil && m.submitStreamAttachments == nil {
		m.vimNormal = false
		m.result = Result{Submitted: true, Prompt: value, Attachments: attachments}
		return m, tea.Quit
	}
	m.appendHistory(value)
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.vimNormal = false
	m.textarea.SetValue("")
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.attachments = nil
	m.closeAttachmentsPanel()
	m.busy = true
	m.status = "running"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: renderSubmittedInput(value, attachments)})
	m.refreshViewport()
	m.viewport.GotoBottom()
	if m.submitStreamAttachments != nil {
		messages := make(chan tea.Msg, 32)
		m.turnMessages = messages
		return m, runStreamSubmitAttachmentsCommand(ctx, m.submitStreamAttachments, value, attachments, messages)
	}
	if m.submitStream != nil {
		messages := make(chan tea.Msg, 32)
		m.turnMessages = messages
		return m, runStreamSubmitCommand(ctx, m.submitStream, value, messages)
	}
	if m.submitAttachments != nil {
		return m, runSubmitAttachmentsCommand(ctx, m.submitAttachments, value, attachments)
	}
	return m, runSubmitCommand(ctx, m.submit, value)
}

func (m model) startQueuedInput(queued queuedPrompt) (tea.Model, tea.Cmd) {
	draft := m.textarea.Value()
	draftAttachments := append([]string(nil), m.attachments...)
	draftAttachmentsOpen := m.attachmentsOpen
	draftAttachmentSelected := m.attachmentSelected
	draftUndo := append([]string(nil), m.undoStack...)
	draftHistoryPos := m.historyPos

	m.textarea.SetValue(queued.Text)
	m.attachments = append([]string(nil), queued.Attachments...)
	next, cmd := m.startInput(queued.Text)
	nextModel, ok := next.(model)
	if !ok {
		return next, cmd
	}

	nextModel.textarea.SetValue(draft)
	nextModel.textarea.CursorEnd()
	nextModel.attachments = draftAttachments
	nextModel.attachmentsOpen = draftAttachmentsOpen && len(draftAttachments) > 0
	nextModel.attachmentSelected = draftAttachmentSelected
	nextModel.normalizeAttachmentSelection()
	nextModel.undoStack = draftUndo
	nextModel.historyPos = draftHistoryPos
	nextModel.refreshCompletionMenu()
	return nextModel, cmd
}

func isThemePickerInput(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "/theme", "/color":
		return true
	default:
		return false
	}
}

func (m model) startBashInput(value string) (tea.Model, tea.Cmd) {
	command := bashModeCommand(value)
	if command == "" {
		m.status = "bash"
		return m, nil
	}
	if m.slash == nil {
		m.vimNormal = false
		m.result = Result{Submitted: true, Prompt: value, Attachments: append([]string(nil), m.attachments...)}
		return m, tea.Quit
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	m.appendHistory(value)
	m.vimNormal = false
	m.textarea.SetValue("")
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.commandArgumentHint = ""
	m.inlineGhostText = ""
	m.historyPos = -1
	m.busy = true
	m.status = "running bash"
	m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: "!" + command})
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m, runSlashCommand(ctx, m.slash, "/run "+command)
}

func (m *model) queueCurrentInput() {
	value := strings.TrimSpace(m.textarea.Value())
	if (value == "" && len(m.attachments) == 0) || m.awaitingPermission || m.awaitingQuestion {
		return
	}
	m.queuedPrompts = append(m.queuedPrompts, queuedPrompt{
		Text:        value,
		Attachments: append([]string(nil), m.attachments...),
	})
	m.textarea.SetValue("")
	m.attachments = nil
	m.closeAttachmentsPanel()
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.status = "queued"
}

func (m *model) quitFromBusyInput() tea.Cmd {
	if !isREPLExitInput(m.textarea.Value()) {
		return nil
	}
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
	if m.backgroundCancel != nil {
		m.backgroundCancel()
		m.backgroundCancel = nil
	}
	m.queuedPrompts = nil
	m.textarea.SetValue("")
	m.matches = nil
	m.selected = 0
	m.status = "exiting"
	return tea.Quit
}

func (m *model) restoreQueuedPrompts() int {
	count := len(m.queuedPrompts)
	if count == 0 {
		return 0
	}
	parts := make([]string, 0, count+1)
	attachments := make([]string, 0)
	for _, queued := range m.queuedPrompts {
		if text := strings.TrimSpace(queued.Text); text != "" {
			parts = append(parts, text)
		}
		attachments = appendUniqueAttachments(attachments, queued.Attachments)
	}
	if current := strings.TrimSpace(m.textarea.Value()); current != "" {
		parts = append(parts, current)
	}
	attachments = appendUniqueAttachments(attachments, m.attachments)
	m.queuedPrompts = nil
	m.textarea.SetValue(strings.Join(parts, "\n\n"))
	m.textarea.CursorEnd()
	m.attachments = attachments
	m.closeAttachmentsPanel()
	m.undoStack = nil
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.refreshCompletionMenu()
	return count
}

func (m model) canEditQueuedPrompts() bool {
	return m.busy &&
		!m.awaitingPermission &&
		!m.awaitingQuestion &&
		!m.searchOpen &&
		m.textarea.Line() == 0 &&
		len(m.matches) == 0 &&
		len(m.queuedPrompts) > 0
}

func (m *model) editQueuedPrompts() {
	if len(m.queuedPrompts) == 0 {
		return
	}
	count := len(m.queuedPrompts)
	parts := make([]string, 0, count+1)
	attachments := make([]string, 0)
	for _, queued := range m.queuedPrompts {
		if text := strings.TrimSpace(queued.Text); text != "" {
			parts = append(parts, text)
		}
		attachments = appendUniqueAttachments(attachments, queued.Attachments)
	}
	if current := strings.TrimSpace(m.textarea.Value()); current != "" {
		parts = append(parts, current)
	}
	attachments = appendUniqueAttachments(attachments, m.attachments)
	value := strings.Join(parts, "\n")
	m.queuedPrompts = nil
	m.textarea.SetValue(value)
	m.textarea.CursorEnd()
	m.attachments = attachments
	m.closeAttachmentsPanel()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "editing queued prompts"
	m.refreshCompletionMenu()
}

func (m *model) togglePromptStash() {
	if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
		return
	}
	if m.helpOpen {
		m.helpOpen = false
	}
	value := m.textarea.Value()
	hasDraft := strings.TrimSpace(value) != "" || len(m.attachments) > 0
	if !hasDraft {
		if m.stashedPrompt == nil {
			m.status = "nothing to stash"
			return
		}
		m.textarea.SetValue(m.stashedPrompt.Text)
		m.textarea.CursorEnd()
		m.attachments = append([]string(nil), m.stashedPrompt.Attachments...)
		m.normalizeAttachmentSelection()
		m.stashedPrompt = nil
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "stash restored"
		m.refreshCompletionMenu()
		return
	}
	m.stashedPrompt = &composerStash{
		Text:        value,
		Attachments: append([]string(nil), m.attachments...),
	}
	m.textarea.SetValue("")
	m.attachments = nil
	m.closeAttachmentsPanel()
	m.matches = nil
	m.selected = 0
	m.historyPos = -1
	m.status = "prompt stashed"
}

func (m model) shouldEnterVimNormalMode() bool {
	if !m.vimEnabled || m.vimNormal {
		return false
	}
	return m.vimKeybindingsAvailable() && strings.TrimSpace(m.textarea.Value()) != ""
}

func (m model) vimKeybindingsAvailable() bool {
	return !m.busy &&
		!m.backgrounding &&
		!m.searchOpen &&
		!m.quickOpen &&
		!m.globalSearch &&
		!m.todosOpen &&
		!m.modelPicker &&
		!m.messageActions &&
		!m.attachmentsOpen &&
		!m.diffDialog &&
		!m.awaitingPermission &&
		!m.awaitingQuestion &&
		!m.helpOpen
}

func (m model) handleVimNormalKey(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	if !m.vimEnabled || !m.vimNormal || !m.vimKeybindingsAvailable() {
		return m, false, nil
	}
	key := msg.String()
	if m.vimOperator != "" {
		return m.handleVimOperatorKey(key)
	}
	switch key {
	case "i":
		m.vimOperator = ""
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "a":
		m.vimOperator = ""
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRight})
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "I":
		m.vimOperator = ""
		m.textarea.CursorStart()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "A":
		m.vimOperator = ""
		m.textarea.CursorEnd()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "h":
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m.status = "vim normal"
		return m, true, nil
	case "l":
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRight})
		m.status = "vim normal"
		return m, true, nil
	case "w":
		m.moveVimWordForward()
		m.status = "vim normal"
		return m, true, nil
	case "b":
		m.moveVimWordBackward()
		m.status = "vim normal"
		return m, true, nil
	case "0":
		m.textarea.CursorStart()
		m.status = "vim normal"
		return m, true, nil
	case "$":
		m.textarea.CursorEnd()
		m.status = "vim normal"
		return m, true, nil
	case "x":
		m.pushComposerUndo()
		m.textarea, _ = m.textarea.Update(tea.KeyMsg{Type: tea.KeyDelete})
		m.matches = nil
		m.selected = 0
		m.refreshCompletionMenu()
		m.status = "vim normal"
		return m, true, nil
	case "D":
		m.deleteVimToLineEnd()
		m.status = "vim normal"
		return m, true, nil
	case "C":
		m.deleteVimToLineEnd()
		m.vimNormal = false
		m.status = "vim insert"
		return m, true, nil
	case "d", "c":
		m.vimOperator = key
		m.status = "vim " + key
		return m, true, nil
	case "u":
		m.undoComposer()
		m.vimNormal = true
		m.status = "vim normal"
		return m, true, nil
	default:
		return m, false, nil
	}
}

func (m model) handleBoundTUIAction(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	key := normalizeTUIKey(msg.String())
	if key == "" || len(m.keybindings) == 0 {
		return m, false, nil
	}
	if next, handled, cmd := m.handleBoundTUIActionKey(key); handled {
		return next, true, cmd
	}
	if m.isBoundTUIChordPrefix(key) {
		m.keyChordPrefix = key
		m.status = key
		return m, true, nil
	}
	return m, false, nil
}

func (m model) handleBoundTUIChord(msg tea.KeyMsg) (model, bool, tea.Cmd) {
	prefix := m.keyChordPrefix
	key := normalizeTUIKey(msg.String())
	m.keyChordPrefix = ""
	if prefix == "" {
		return m, false, nil
	}
	if key == "esc" || key == "" {
		m.status = m.mode()
		return m, true, nil
	}
	sequence := strings.TrimSpace(prefix + " " + key)
	if next, handled, cmd := m.handleBoundTUIActionKey(sequence); handled {
		return next, true, cmd
	}
	if prefix == "ctrl+x" {
		next, cmd := m.handleDefaultCtrlXChord(msg)
		return next, true, cmd
	}
	m.status = "compose"
	return m, true, nil
}

func (m model) handleBoundTUIActionKey(key string) (model, bool, tea.Cmd) {
	if key == "" {
		return m, false, nil
	}
	if next, handled, cmd := m.handleBoundTUIActionGroup1(key); handled {
		return next, true, cmd
	}
	if next, handled, cmd := m.handleBoundTUIActionGroup2(key); handled {
		return next, true, cmd
	}
	if next, handled, cmd := m.handleBoundTUIActionGroup3(key); handled {
		return next, true, cmd
	}
	if next, handled, cmd := m.handleBoundTUIActionGroup4(key); handled {
		return next, true, cmd
	}
	return m, false, nil
}
func (m model) handleBoundTUIActionGroup1(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("submit prompt", key):
		if m.busy && m.awaitingQuestion {
			m.answerQuestion()
			return m, true, nil
		}
		if m.busy {
			if cmd := m.quitFromBusyInput(); cmd != nil {
				return m, true, cmd
			}
			m.queueCurrentInput()
			return m, true, nil
		}
		if m.searchOpen {
			m.closeHistorySearch(true)
			return m, true, nil
		}
		if m.convertTrailingBackslashToNewline() {
			return m, true, nil
		}
		if len(m.matches) > 0 {
			m.pushComposerUndo()
			m = m.acceptSelectedCompletion()
			return m, true, nil
		}
		if isLocalHelpInput(m.textarea.Value()) {
			m.helpOpen = true
			m.textarea.SetValue("")
			m.matches = nil
			m.status = "help"
			m.refreshViewport()
			return m, true, nil
		}
		next, cmd := m.submitCurrentInput()
		if modelNext, ok := next.(model); ok {
			return modelNext, true, cmd
		}
		return m, true, cmd
	case m.isBoundTUIAction("insert newline", key) || m.isBoundTUIAction("insert newline fallback", key):
		if m.busy {
			return m, true, nil
		}
		m.pushComposerUndo()
		m.textarea.InsertString("\n")
		return m, true, nil
	case m.isBoundTUIAction("stash or restore composer", key):
		m.togglePromptStash()
		return m, true, nil
	case m.isBoundTUIAction("edit composer in $EDITOR", key):
		next, cmd := m.openExternalEditor()
		if modelNext, ok := next.(model); ok {
			return modelNext, true, cmd
		}
		return m, true, cmd
	case m.isBoundTUIAction("stop running background tasks and agents", key):
		if m.stopBackground == nil {
			m.status = "no background stop"
			return m, true, nil
		}
		m.status = "stopping background"
		return m, true, runRuntimeControlCommand(m.ctx, m.stopBackground)
	case m.isBoundTUIAction("compact current session", key):
		if m.compactSession == nil {
			m.status = "no compact"
			return m, true, nil
		}
		m.status = "compacting"
		return m, true, runRuntimeControlCommand(m.ctx, m.compactSession)
	}
	return m, false, nil
}
func (m model) handleBoundTUIActionGroup2(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("undo last file change", key):
		if m.undoLast == nil {
			m.status = "no undo"
			return m, true, nil
		}
		m.status = "undoing"
		return m, true, runRuntimeControlCommand(m.ctx, m.undoLast)
	case m.isBoundTUIAction("export current conversation", key):
		if m.exportConversation == nil {
			m.status = "no export"
			return m, true, nil
		}
		m.status = "exporting"
		return m, true, runRuntimeControlCommand(m.ctx, m.exportConversation)
	case m.isBoundTUIAction("copy current conversation", key):
		if m.copyConversation == nil {
			m.status = "no copy"
			return m, true, nil
		}
		m.status = "copying"
		return m, true, runRuntimeControlCommand(m.ctx, m.copyConversation)
	case m.isBoundTUIAction("remove last attachment", key):
		m.removeLastAttachment()
		return m, true, nil
	case m.isBoundTUIAction("undo composer edit", key):
		if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.undoComposer()
		return m, true, nil
	case m.isBoundTUIAction("delete before cursor", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.deleteComposerBeforeCursor()
		return m, true, nil
	}
	return m, false, nil
}
func (m model) handleBoundTUIActionGroup3(key string) (model, bool, tea.Cmd) {
	if next, handled, cmd := m.handleBoundTUIActionGroup3A(key); handled {
		return next, true, cmd
	}
	return m.handleBoundTUIActionGroup3B(key)
}
func (m model) handleBoundTUIActionGroup4(key string) (model, bool, tea.Cmd) {
	if next, handled, cmd := m.handleBoundTUIActionGroup4A(key); handled {
		return next, true, cmd
	}
	return m.handleBoundTUIActionGroup4B(key)
}

func (m model) handleBoundTUIActionGroup3A(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("delete after cursor", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.deleteComposerAfterCursor()
		return m, true, nil
	case m.isBoundTUIAction("move to line start", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.moveComposerLineStart()
		return m, true, nil
	case m.isBoundTUIAction("move to line end", key):
		if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.moveComposerLineEnd()
		return m, true, nil
	}
	return m, false, nil
}

func (m model) handleBoundTUIActionGroup3B(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("quick open files", key) || m.isBoundTUIAction("quick open fallback", key):
		if m.busy || m.backgrounding || m.searchOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
			return m, true, nil
		}
		m.openQuickOpen()
		return m, true, nil
	case m.isBoundTUIAction("search workspace", key) || m.isBoundTUIAction("search workspace fallback", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
			return m, true, nil
		}
		m.openGlobalSearch()
		return m, true, nil
	case m.isBoundTUIAction("cycle permission mode fallback", key):
		if m.busy || m.cycleMode == nil {
			return m, true, nil
		}
		m.cyclePermissionMode()
		return m, true, nil
	}
	return m, false, nil
}

func (m model) handleBoundTUIActionGroup4A(key string) (model, bool, tea.Cmd) {
	if next, handled, cmd := m.handleBoundTUIModelPickerAction(key); handled {
		return next, true, cmd
	}
	if next, handled, cmd := m.handleBoundTUIFastModeAction(key); handled {
		return next, true, cmd
	}
	return m.handleBoundTUIThinkingAction(key)
}

func (m model) handleBoundTUIActionGroup4B(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("toggle expanded transcript", key):
		if m.helpOpen {
			m.helpOpen = false
		}
		if m.todosOpen {
			m.closeTodos()
		}
		m.transcriptMode = !m.transcriptMode
		if m.transcriptMode {
			m.status = "transcript"
		} else {
			m.status = "ready"
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, true, nil
	case m.isBoundTUIAction("clear screen", key):
		if m.busy {
			return m, true, nil
		}
		m.clearScreen()
		return m, true, nil
	case m.isBoundTUIAction("paste clipboard text or image", key):
		if m.paste == nil || m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		if m.helpOpen {
			m.helpOpen = false
			m.refreshViewport()
		}
		m.matches = nil
		m.selected = 0
		m.status = "pasting"
		return m, true, runPasteCommand(m.ctx, m.paste)
	}
	return m, false, nil
}

func (m model) handleBoundTUIModelPickerAction(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("open model picker", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.openModelPicker()
		return m, true, nil
	}
	return m, false, nil
}

func (m model) handleBoundTUIFastModeAction(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("toggle fast mode", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleFast == nil {
			return m, true, nil
		}
		m.status = "fast mode"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleFast)
	}
	return m, false, nil
}

func (m model) handleBoundTUIThinkingAction(key string) (model, bool, tea.Cmd) {
	switch {
	case m.isBoundTUIAction("cycle thinking effort", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleThinking == nil {
			return m, true, nil
		}
		m.status = "thinking"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleThinking)
	}
	return m, false, nil
}
