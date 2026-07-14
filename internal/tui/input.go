package tui

import (
	"context"
	"errors"
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
		{
			Role: "system",
			Text: strings.Join([]string{
				"Interactive coding agent ready.",
				"Mention @files, run !shell commands, or type /help.",
			}, "\n"),
		},
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
		m.initialPrompt = ""
		return m.startInput(msg.Value)
	case exitPendingExpiredMsg:
		if m.exitPending && m.exitKey == msg.Key && m.exitPendingGeneration == msg.Generation {
			m.clearExitPending()
			m.status = m.mode()
		}
		return m, nil
	case turnDoneMsg:
		m.busy = false
		m.refreshModeLabel()
		m.clearInteractionPrompts()
		m.turnMessages = nil
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		if msg.Err == nil && !msg.Interrupted && len(msg.SessionChoices) > 0 {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openSessionPicker(msg.SessionChoices)
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.Session != nil {
			m.applySessionState(*msg.Session)
		}
		if msg.Err == nil && !msg.Interrupted && strings.TrimSpace(msg.Query) != "" {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			if output := strings.TrimSpace(msg.Output); output != "" {
				m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: output})
			}
			return m.startInput(strings.TrimSpace(msg.Query))
		}
		if msg.Err == nil && !msg.Interrupted && msg.OpenModelPicker {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openModelPicker()
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.OpenThemePicker {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openThemePicker()
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.OpenTodos {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			if m.todos == nil {
				m.status = "todos unavailable"
				m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: "todo list is unavailable"})
				m.refreshViewport()
				return m, m.flushInlineTranscript()
			}
			next, cmd := m.toggleTodos()
			return next, sequenceCommands(m.flushInlineTranscript(), cmd)
		}
		if msg.Err == nil && !msg.Interrupted && msg.OpenMessageActions {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openLatestUserMessageActions()
			m.refreshViewport()
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.ExportDialog != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openExportDialog(*msg.ExportDialog)
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.TextInputDialog != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openTextInputDialog(*msg.TextInputDialog)
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && strings.TrimSpace(msg.RuntimeAction) != "" {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			next, cmd := m.runSlashRuntimeAction(msg.RuntimeAction)
			return next, sequenceCommands(m.flushInlineTranscript(), cmd)
		}
		if msg.Err == nil && !msg.Interrupted && msg.Diff != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openDiffDialog(msg.Diff.Sources)
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.PermissionSettings != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openPermissionSettings(*msg.PermissionSettings)
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.Information != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openInformation(*msg.Information)
			return m, m.flushInlineTranscript()
		}
		if msg.Err == nil && !msg.Interrupted && msg.CommandView != nil {
			m.streamingIndex = -1
			m.discardLatestSlashInput()
			m.openCommandView(*msg.CommandView)
			return m, m.flushInlineTranscript()
		}
		if msg.Interrupted || errors.Is(msg.Err, context.Canceled) {
			m.streamingIndex = -1
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Interrupted by user."})
			m.status = "interrupted"
			if restored := m.restoreQueuedPrompts(); restored > 0 {
				m.status = fmt.Sprintf("interrupted · %d queued restored", restored)
			}
		} else if msg.Err != nil {
			m.streamingIndex = -1
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.status = "error"
			if restored := m.restoreQueuedPrompts(); restored > 0 {
				m.status = fmt.Sprintf("error · %d queued restored", restored)
			}
		} else if strings.TrimSpace(msg.Output) != "" {
			m.finishStreamingOutput(msg.Role, msg.Output)
			m.status = "ready"
		} else {
			m.status = "ready"
		}
		m.streamingIndex = -1
		if m.backgrounding {
			m.status = "backgrounding"
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		flushCmd := m.flushInlineTranscript()
		if len(m.queuedPrompts) > 0 && msg.Err == nil && !msg.Interrupted {
			next := m.queuedPrompts[0]
			m.queuedPrompts = append([]queuedPrompt(nil), m.queuedPrompts[1:]...)
			nextModel, nextCmd := m.startQueuedInput(next)
			return nextModel, sequenceCommands(flushCmd, nextCmd)
		}
		return m, flushCmd
	case externalEditorDoneMsg:
		if msg.Err != nil {
			m.status = "editor error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.pushComposerUndo()
		m.textarea.SetValue(msg.Text)
		m.textarea.CursorEnd()
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "editor updated"
		m.refreshCompletionMenu()
		return m, nil
	case pasteDoneMsg:
		if msg.Err != nil {
			m.status = "paste error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		if msg.Content.Text == "" && msg.Content.AttachmentPath == "" {
			m.status = "paste empty"
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Clipboard is empty."})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		return m.insertPasteContent(msg.Content)
	case backgroundDoneMsg:
		m.backgrounding = false
		if m.backgroundCancel != nil {
			m.backgroundCancel()
			m.backgroundCancel = nil
		}
		if msg.Err != nil {
			if errors.Is(msg.Err, context.Canceled) {
				m.status = "background canceled"
				m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Background prompt canceled."})
			} else {
				m.status = "background error"
				m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			}
			if m.busy {
				m.status = "running"
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.textarea.SetValue("")
		m.matches = nil
		m.selected = 0
		m.historyPos = -1
		m.status = "backgrounded"
		if m.busy {
			m.status = "running"
		}
		if strings.TrimSpace(msg.Output) == "" {
			msg.Output = "Background task started."
		}
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: msg.Output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, m.flushInlineTranscript()
	case taskBoardDoneMsg:
		if msg.Err != nil {
			m.status = "tasks error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		output := strings.TrimSpace(msg.Output)
		if output == "" {
			output = "No background tasks."
		}
		m.status = "tasks"
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: output})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, m.flushInlineTranscript()
	case todoListDoneMsg:
		m.todosLoading = false
		if msg.Err != nil {
			m.todoErr = msg.Err.Error()
			m.status = "todos error"
			return m, nil
		}
		m.todoErr = ""
		m.todoItems = normalizeTUITodoItems(msg.Items)
		m.status = fmt.Sprintf("todos %d", len(m.todoItems))
		return m, nil
	case runtimeControlDoneMsg:
		if msg.Err != nil {
			m.status = "control error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.applyRuntimeControlResult(msg.Result)
		return m, m.flushInlineTranscript()
	case permissionModeSelectDoneMsg:
		if msg.Err != nil {
			m.status = "permissions error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.refreshModeLabel()
		m.applyRuntimeControlResult(msg.Result)
		return m, m.flushInlineTranscript()
	case themeSelectDoneMsg:
		if msg.Err != nil {
			m.theme = msg.Previous
			m.applyTheme()
			m.status = "theme error"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, m.flushInlineTranscript()
		}
		m.theme = msg.Selected
		m.applyTheme()
		m.applyRuntimeControlResult(msg.Result)
		return m, m.flushInlineTranscript()
	case turnStreamMsg:
		m.appendStreamEntry(msg)
		switch {
		case msg.Permission != nil:
			m.openPermissionRequest(*msg.Permission)
		case msg.Question != nil:
			m.openQuestionRequest(*msg.Question)
		case msg.Tool != nil:
			if strings.EqualFold(msg.Tool.Status, "running") {
				m.status = "running " + strings.ToLower(toolActivityDisplayName(msg.Tool.Name))
			} else {
				m.status = "streaming"
			}
		case strings.EqualFold(msg.Role, "permission"):
			m.awaitingPermission = isPermissionRequestDelta(msg.Delta)
			if m.awaitingPermission {
				m.openPermissionRequest(PermissionRequest{Input: msg.Delta})
				m.status = "permission"
			} else {
				m.closePermissionRequest()
				m.status = "permission answered"
			}
		case strings.EqualFold(msg.Role, "question"):
			m.openQuestionRequest(QuestionRequest{Question: msg.Delta})
		default:
			m.status = "streaming"
		}
		m.refreshViewport()
		m.viewport.GotoBottom()
		if m.turnMessages != nil {
			return m, waitTurnMessage(m.turnMessages)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout(msg.Width, msg.Height)
		if m.sessionPicker != nil {
			m.sessionPicker.width = msg.Width
			m.sessionPicker.height = msg.Height
		}
	case tea.KeyMsg:
		if m.sessionPicker != nil {
			return m.updateSessionPicker(msg)
		}
		if m.exportDialog != nil {
			return m.updateExportDialog(msg)
		}
		if m.textInputDialog != nil {
			return m.updateTextInputDialog(msg)
		}
		if msg.Paste {
			return m.handlePastedInput(msg)
		}
		if m.awaitingPermission {
			return m.updatePermissionRequest(msg)
		}
		if m.awaitingQuestion {
			return m.updateQuestionRequest(msg)
		}
		if m.permissionSettings != nil {
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closePermissionSettings()
				return m, nil
			case "up", "ctrl+p", "k", "shift+tab":
				m.movePermissionSettings(-1)
				return m, nil
			case "down", "ctrl+n", "j", "tab":
				m.movePermissionSettings(1)
				return m, nil
			case "home":
				m.permissionModeSelected = 0
				return m, nil
			case "end":
				m.permissionModeSelected = max(0, len(m.permissionSettings.Modes)-1)
				return m, nil
			case "enter":
				return m.acceptPermissionSettings()
			default:
				return m, nil
			}
		}
		if m.commandView != nil {
			return m.updateCommandView(msg)
		}
		if m.information != nil {
			switch msg.String() {
			case "ctrl+c", "esc", "left":
				m.closeInformation()
				return m, nil
			case "enter", " ", "space":
				if m.information.DismissOnConfirm {
					m.closeInformation()
					return m, nil
				}
				if msg.String() == " " || msg.String() == "space" {
					m.moveInformation(informationVisibleLines(m.height))
				}
				return m, nil
			case "up", "ctrl+p", "k":
				m.moveInformation(-1)
				return m, nil
			case "down", "ctrl+n", "j":
				m.moveInformation(1)
				return m, nil
			case "pgup":
				m.moveInformation(-informationVisibleLines(m.height))
				return m, nil
			case "pgdown":
				m.moveInformation(informationVisibleLines(m.height))
				return m, nil
			case "home":
				m.informationOffset = 0
				return m, nil
			case "end":
				m.moveInformation(len(m.information.Lines))
				return m, nil
			default:
				return m, nil
			}
		}
		if m.themePicker {
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeThemePicker(true)
				return m, nil
			case "up", "left", "ctrl+p", "k", "shift+tab":
				m.moveThemePicker(-1)
				return m, nil
			case "down", "right", "ctrl+n", "j", "tab":
				m.moveThemePicker(1)
				return m, nil
			case "home":
				m.setThemePickerIndex(0)
				return m, nil
			case "end":
				m.setThemePickerIndex(len(ThemeNames()) - 1)
				return m, nil
			case "enter":
				return m.acceptThemePicker()
			default:
				return m, nil
			}
		}
		if m.diffDialog {
			if next, handled, cmd := m.handleBoundDiffAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeDiffDialog()
				return m, nil
			case "left":
				m.previousDiffSourceOrBack()
				return m, nil
			case "right":
				m.nextDiffSource()
				return m, nil
			case "up":
				m.moveDiffFile(-1)
				return m, nil
			case "down":
				m.moveDiffFile(1)
				return m, nil
			case "enter":
				m.openDiffDetail()
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.attachmentsOpen {
			if next, handled, cmd := m.handleBoundAttachmentAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc", "down":
				m.closeAttachmentsPanel()
				return m, nil
			case "right":
				m.moveAttachmentSelection(1)
				return m, nil
			case "left":
				m.moveAttachmentSelection(-1)
				return m, nil
			case "backspace", "delete":
				m.removeSelectedAttachment()
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.modelPicker {
			if next, handled, cmd := m.handleBoundModelPickerAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc", "alt+p", "meta+p":
				m.closeModelPicker()
				return m, nil
			case "up", "ctrl+p", "k":
				m.moveModelPicker(-1)
				return m, nil
			case "down", "ctrl+n", "j":
				m.moveModelPicker(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up", "K", "shift+k":
				m.setModelPickerIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down", "J", "shift+j":
				m.setModelPickerIndex(len(m.modelOptions) - 1)
				return m, nil
			case "enter", "tab":
				return m.acceptModelPicker()
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "alt+m", "meta+m", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.messageActions {
			if next, handled, cmd := m.handleBoundMessageActionMenuAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeMessageActions()
				return m, nil
			case "up", "ctrl+p", "k":
				m.moveMessageAction(-1)
				return m, nil
			case "down", "ctrl+n", "j":
				m.moveMessageAction(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up", "K", "shift+k":
				m.setMessageActionIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down", "J", "shift+j":
				m.setMessageActionIndex(len(messageActionLabels) - 1)
				return m, nil
			case "shift+up":
				m.moveMessageActionUserTarget(-1)
				return m, nil
			case "shift+down":
				m.moveMessageActionUserTarget(1)
				return m, nil
			case "left":
				m.moveMessageActionTarget(-1)
				return m, nil
			case "right":
				m.moveMessageActionTarget(1)
				return m, nil
			case "enter", "tab":
				return m.applyMessageAction()
			case "c":
				m.messageActionSelected = 1
				return m.applyMessageAction()
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			return m, nil
		}
		if m.globalSearch {
			if next, handled, cmd := m.handleBoundGlobalSearchAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeGlobalSearch(false, false)
				return m, nil
			case "up", "ctrl+p":
				m.moveGlobalSearch(-1)
				return m, nil
			case "down", "ctrl+n":
				m.moveGlobalSearch(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up":
				m.setGlobalSearchIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down":
				m.setGlobalSearchIndex(len(m.globalSearchMatches) - 1)
				return m, nil
			case "enter", "tab":
				m.closeGlobalSearch(true, true)
				return m, nil
			case "shift+tab":
				m.closeGlobalSearch(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			var cmd tea.Cmd
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			m.textarea, cmd = m.textarea.Update(msg)
			m.updateGlobalSearch()
			return m, tea.Batch(cmd, viewportCmd)
		}
		if m.quickOpen {
			if next, handled, cmd := m.handleBoundQuickOpenAction(msg); handled {
				return next, cmd
			}
			switch msg.String() {
			case "ctrl+c", "esc":
				m.closeQuickOpen(false, false)
				return m, nil
			case "up", "ctrl+p":
				m.moveQuickOpen(-1)
				return m, nil
			case "down", "ctrl+n":
				m.moveQuickOpen(1)
				return m, nil
			case "home", "ctrl+up", "meta+up", "alt+up":
				m.setQuickOpenIndex(0)
				return m, nil
			case "end", "ctrl+down", "meta+down", "alt+down":
				m.setQuickOpenIndex(len(m.quickOpenMatches) - 1)
				return m, nil
			case "enter", "tab":
				m.closeQuickOpen(true, true)
				return m, nil
			case "shift+tab":
				m.closeQuickOpen(true, false)
				return m, nil
			case "ctrl+r", "ctrl+s", "ctrl+_", "ctrl+shift+-", "ctrl+x", "ctrl+shift+f", "ctrl+f", "ctrl+shift+p", "ctrl+o", "ctrl+g", "ctrl+b", "ctrl+t", "ctrl+shift+t", "ctrl+v", "ctrl+l", "ctrl+d", "shift+up", "alt+m", "meta+m", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
				return m, nil
			}
			var cmd tea.Cmd
			var viewportCmd tea.Cmd
			m.viewport, viewportCmd = m.viewport.Update(msg)
			m.textarea, cmd = m.textarea.Update(msg)
			m.updateQuickOpen()
			return m, tea.Batch(cmd, viewportCmd)
		}
		if m.keyChordPrefix != "" {
			next, handled, cmd := m.handleBoundTUIChord(msg)
			if handled {
				return next, cmd
			}
		}
		if m.ctrlXChord {
			m.ctrlXChord = false
			return m.handleDefaultCtrlXChord(msg)
		}
		key := msg.String()
		if m.exitPending && key != m.exitKey {
			m.clearExitPending()
		}
		if next, handled, cmd := m.handleBoundTUIAction(msg); handled {
			return next, cmd
		}
		switch msg.String() {
		case "ctrl+c":
			if m.busy {
				m.interruptTurn()
				return m, nil
			}
			if m.backgrounding {
				m.interruptBackground()
				return m, nil
			}
			if m.exitPending && m.exitKey == "ctrl+c" {
				return m, tea.Quit
			}
			if strings.TrimSpace(m.textarea.Value()) != "" || len(m.attachments) > 0 {
				m.clearComposerInput(false)
				return m, m.armExit("ctrl+c", "input cleared · press ctrl+c again to exit")
			}
			return m, m.armExit("ctrl+c", "press ctrl+c again to exit")
		case "esc":
			if m.shouldEnterVimNormalMode() {
				m.vimNormal = true
				m.matches = nil
				m.selected = 0
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.clearExitPending()
				m.status = "vim normal"
				return m, nil
			}
			if m.vimEnabled && m.vimNormal && m.vimKeybindingsAvailable() && strings.TrimSpace(m.textarea.Value()) != "" {
				m.status = "vim normal"
				return m, nil
			}
			if m.busy {
				m.interruptTurn()
				return m, nil
			}
			if m.backgrounding {
				m.interruptBackground()
				return m, nil
			}
			if len(m.matches) > 0 {
				m.matches = nil
				m.selected = 0
				m.commandArgumentHint = ""
				m.inlineGhostText = ""
				m.clearExitPending()
				m.status = m.mode()
				return m, nil
			}
			if m.searchOpen {
				m.clearExitPending()
				m.closeHistorySearch(false)
				return m, nil
			}
			if m.todosOpen {
				m.clearExitPending()
				m.closeTodos()
				return m, nil
			}
			if m.helpOpen {
				m.clearExitPending()
				m.helpOpen = false
				m.status = "ready"
				m.refreshViewport()
				return m, nil
			}
			hasComposer := strings.TrimSpace(m.textarea.Value()) != "" || len(m.attachments) > 0
			if hasComposer {
				if m.exitPending && m.exitKey == "esc" {
					m.clearComposerInput(true)
					m.clearExitPending()
					m.status = "input cleared"
					return m, nil
				}
				return m, m.armExit("esc", "Esc again to clear")
			}
			if m.exitPending && m.exitKey == "esc" {
				m.clearExitPending()
				m.openLatestUserMessageActions()
				return m, nil
			}
			if m.hasUserTranscriptEntry() {
				return m, m.armExit("esc", m.status)
			}
			return m, nil
		case "ctrl+d":
			if !m.busy && !m.searchOpen && !m.quickOpen && !m.globalSearch && !m.todosOpen && !m.modelPicker && !m.messageActions && !m.helpOpen && strings.TrimSpace(m.textarea.Value()) == "" && len(m.attachments) == 0 {
				if m.exitPending && m.exitKey == "ctrl+d" {
					return m, tea.Quit
				}
				return m, m.armExit("ctrl+d", "press ctrl+d again to exit")
			}
		case "ctrl+l":
			if m.busy {
				return m, nil
			}
			m.clearScreen()
			if m.inline {
				return m, tea.ClearScreen
			}
			return m, nil
		case "ctrl+v":
			if m.paste == nil || m.busy || m.backgrounding || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			if m.helpOpen {
				m.helpOpen = false
				m.refreshViewport()
			}
			m.matches = nil
			m.selected = 0
			m.status = "pasting"
			return m, runPasteCommand(m.ctx, m.paste)
		case "ctrl+_", "ctrl+shift+-":
			if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.undoComposer()
			return m, nil
		case "ctrl+u":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.deleteComposerBeforeCursor()
			return m, nil
		case "ctrl+k":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.deleteComposerAfterCursor()
			return m, nil
		case "home", "ctrl+a":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.moveComposerLineStart()
			return m, nil
		case "end":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.moveComposerLineEnd()
			return m, nil
		case "ctrl+x":
			if m.busy || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.ctrlXChord = true
			m.status = "ctrl+x"
			return m, nil
		case "shift+up":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.openMessageActions()
			return m, nil
		case "ctrl+b":
			if m.backgrounding || m.background == nil || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			value := strings.TrimSpace(m.textarea.Value())
			if value == "" {
				return m, nil
			}
			m.appendHistory(value)
			m.matches = nil
			m.selected = 0
			m.historyPos = -1
			ctx, cancel := context.WithCancel(m.ctx)
			m.backgrounding = true
			m.backgroundCancel = cancel
			m.status = "backgrounding"
			m.transcript = append(m.transcript, transcriptEntry{Role: "user", Text: value})
			m.refreshViewport()
			m.viewport.GotoBottom()
			return m, runBackgroundCommand(ctx, m.background, value)
		case "ctrl+t":
			if m.searchOpen || m.quickOpen || m.globalSearch || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			if m.todos != nil {
				return m.toggleTodos()
			}
			if m.taskBoard == nil {
				return m, nil
			}
			return m.openTaskBoard()
		case "ctrl+shift+t":
			if m.taskBoard == nil || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			return m.openTaskBoard()
		case "ctrl+shift+p", "ctrl+p":
			if m.busy || m.backgrounding || m.searchOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
				return m, nil
			}
			m.openQuickOpen()
			return m, nil
		case "ctrl+shift+f", "ctrl+f":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.todosOpen || m.awaitingPermission || m.awaitingQuestion || len(m.fileCandidates) == 0 {
				return m, nil
			}
			m.openGlobalSearch()
			return m, nil
		case "alt+p", "meta+p":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
				return m, nil
			}
			m.openModelPicker()
			return m, nil
		case "alt+o", "meta+o":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleFast == nil {
				return m, nil
			}
			m.status = "fast mode"
			return m, runRuntimeControlCommand(m.ctx, m.toggleFast)
		case "alt+t", "meta+t":
			if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleThinking == nil {
				return m, nil
			}
			m.status = "thinking"
			return m, runRuntimeControlCommand(m.ctx, m.toggleThinking)
		case "shift+enter", "alt+enter", "ctrl+j":
			m.pushComposerUndo()
			m.textarea.InsertString("\n")
			return m, nil
		case "ctrl+s":
			m.togglePromptStash()
			return m, nil
		case "ctrl+o":
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
			return m, nil
		case "ctrl+g":
			return m.openExternalEditor()
		case "pgup":
			m.viewport.ScrollUp(max(1, m.viewport.Height/2))
			return m, nil
		case "pgdown":
			m.viewport.ScrollDown(max(1, m.viewport.Height/2))
			return m, nil
		case "up":
			if m.searchOpen {
				m.moveHistorySearch(-1)
				return m, nil
			}
			if m.canEditQueuedPrompts() {
				m.editQueuedPrompts()
				return m, nil
			}
			if len(m.matches) > 0 {
				m.selected = (m.selected - 1 + len(m.matches)) % len(m.matches)
				return m, nil
			}
			if m.canNavigateHistory() {
				m.navigateHistory(-1)
				return m, nil
			}
		case "down":
			if m.searchOpen {
				m.moveHistorySearch(1)
				return m, nil
			}
			if len(m.matches) > 0 {
				m.selected = (m.selected + 1) % len(m.matches)
				return m, nil
			}
			if m.canNavigateHistory() {
				m.navigateHistory(1)
				return m, nil
			}
		case "tab":
			if m.busy {
				return m, nil
			}
			if m.searchOpen {
				m.moveHistorySearch(1)
				return m, nil
			}
			m.pushComposerUndo()
			m = m.completeSlashCommand()
			return m, nil
		case "shift+tab", "alt+m", "meta+m":
			if m.busy || m.cycleMode == nil {
				return m, nil
			}
			m.cyclePermissionMode()
			return m, nil
		case "ctrl+r":
			if !m.todosOpen && len(m.history) > 0 {
				m.openHistorySearch()
				return m, nil
			}
		case "y", "Y", "a", "A", "n", "N":
			if m.awaitingPermission {
				m.answerPermission(msg.String())
				return m, nil
			}
		case "?":
			if strings.TrimSpace(m.textarea.Value()) == "" {
				m.helpOpen = !m.helpOpen
				m.status = m.mode()
				m.refreshViewport()
				return m, nil
			}
		case "enter":
			if m.busy && m.awaitingQuestion {
				m.answerQuestion()
				return m, nil
			}
			if m.busy {
				if cmd := m.quitFromBusyInput(); cmd != nil {
					return m, cmd
				}
				m.queueCurrentInput()
				return m, nil
			}
			if m.searchOpen {
				m.closeHistorySearch(true)
				return m, nil
			}
			if m.convertTrailingBackslashToNewline() {
				return m, nil
			}
			if len(m.matches) > 0 && shouldAcceptCompletionOnEnter(m.textarea.Value()) {
				m.pushComposerUndo()
				m = m.acceptSelectedCompletion()
				return m, nil
			}
			if isLocalHelpInput(m.textarea.Value()) {
				m.helpOpen = true
				m.textarea.SetValue("")
				m.matches = nil
				m.status = "help"
				m.refreshViewport()
				return m, nil
			}
			return m.submitCurrentInput()
		}
		if next, handled, cmd := m.handleVimNormalKey(msg); handled {
			return next, cmd
		}
	}
	var cmd tea.Cmd
	var viewportCmd tea.Cmd
	m.viewport, viewportCmd = m.viewport.Update(msg)
	if !m.searchOpen && !m.quickOpen && !m.globalSearch && !m.todosOpen {
		m.pushComposerUndo()
	}
	m.textarea, cmd = m.textarea.Update(msg)
	if m.searchOpen {
		m.updateHistorySearch()
		return m, tea.Batch(cmd, viewportCmd)
	}
	if m.quickOpen {
		m.updateQuickOpen()
		return m, tea.Batch(cmd, viewportCmd)
	}
	if m.globalSearch {
		m.updateGlobalSearch()
		return m, tea.Batch(cmd, viewportCmd)
	}
	m.refreshCompletionMenu()
	if isLocalHelpInput(m.textarea.Value()) {
		m.status = "help ready"
	} else if m.awaitingQuestion {
		m.status = "question"
	} else if m.backgrounding {
		m.status = "backgrounding"
	} else if m.busy {
		m.status = "running"
	} else {
		m.status = m.mode()
	}
	return m, tea.Batch(cmd, viewportCmd)
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
	case m.isBoundTUIAction("open model picker", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion {
			return m, true, nil
		}
		m.openModelPicker()
		return m, true, nil
	case m.isBoundTUIAction("toggle fast mode", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleFast == nil {
			return m, true, nil
		}
		m.status = "fast mode"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleFast)
	case m.isBoundTUIAction("cycle thinking effort", key):
		if m.busy || m.backgrounding || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.modelPicker || m.awaitingPermission || m.awaitingQuestion || m.toggleThinking == nil {
			return m, true, nil
		}
		m.status = "thinking"
		return m, true, runRuntimeControlCommand(m.ctx, m.toggleThinking)
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
	default:
		return m, false, nil
	}
}
