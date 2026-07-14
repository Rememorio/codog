package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) updateInitialPrompt(msg initialPromptMsg) (tea.Model, tea.Cmd) {
	m.initialPrompt = ""
	return m.startInput(msg.Value)
}

func (m model) updateExitPendingExpired(msg exitPendingExpiredMsg) (tea.Model, tea.Cmd) {
	if m.exitPending && m.exitKey == msg.Key && m.exitPendingGeneration == msg.Generation {
		m.clearExitPending()
		m.status = m.mode()
	}
	return m, nil
}

func (m model) updateTurnDone(msg turnDoneMsg) (tea.Model, tea.Cmd) {
	m.completeTurn()
	if msg.Err == nil && !msg.Interrupted {
		if next, cmd, handled := m.handleTurnSessionChoices(msg); handled {
			return next, cmd
		}
		if msg.Session != nil {
			m.applySessionState(*msg.Session)
		}
		handlers := []func(turnDoneMsg) (tea.Model, tea.Cmd, bool){
			m.handleTurnPrimaryAction,
			m.handleTurnSecondaryAction,
			m.handleTurnDialogAction,
		}
		for _, handle := range handlers {
			if next, cmd, handled := handle(msg); handled {
				return next, cmd
			}
		}
	}
	return m.finalizeTurnDone(msg)
}

func (m *model) completeTurn() {
	m.busy = false
	m.refreshModeLabel()
	m.clearInteractionPrompts()
	m.turnMessages = nil
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
}

func (m *model) prepareTurnAction() {
	m.streamingIndex = -1
	m.discardLatestSlashInput()
}

func (m model) handleTurnSessionChoices(msg turnDoneMsg) (tea.Model, tea.Cmd, bool) {
	if len(msg.SessionChoices) == 0 {
		return m, nil, false
	}
	m.prepareTurnAction()
	m.openSessionPicker(msg.SessionChoices)
	m.refreshViewport()
	m.viewport.GotoBottom()
	return m, m.flushInlineTranscript(), true
}

func (m model) handleTurnPrimaryAction(msg turnDoneMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case strings.TrimSpace(msg.Query) != "":
		m.prepareTurnAction()
		if output := strings.TrimSpace(msg.Output); output != "" {
			m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: output})
		}
		next, cmd := m.startInput(strings.TrimSpace(msg.Query))
		return next, cmd, true
	case msg.OpenModelPicker:
		m.prepareTurnAction()
		m.openModelPicker()
		return m, m.flushInlineTranscript(), true
	case msg.OpenThemePicker:
		m.prepareTurnAction()
		m.openThemePicker()
		return m, m.flushInlineTranscript(), true
	case msg.OpenTodos:
		m.prepareTurnAction()
		if m.todos == nil {
			m.status = "todos unavailable"
			m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: "todo list is unavailable"})
			m.refreshViewport()
			return m, m.flushInlineTranscript(), true
		}
		next, cmd := m.toggleTodos()
		return next, sequenceCommands(m.flushInlineTranscript(), cmd), true
	default:
		return m, nil, false
	}
}

func (m model) handleTurnSecondaryAction(msg turnDoneMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case msg.OpenMessageActions:
		m.prepareTurnAction()
		m.openLatestUserMessageActions()
		m.refreshViewport()
		return m, m.flushInlineTranscript(), true
	case msg.ExportDialog != nil:
		m.prepareTurnAction()
		m.openExportDialog(*msg.ExportDialog)
		return m, m.flushInlineTranscript(), true
	case msg.TextInputDialog != nil:
		m.prepareTurnAction()
		m.openTextInputDialog(*msg.TextInputDialog)
		return m, m.flushInlineTranscript(), true
	case strings.TrimSpace(msg.RuntimeAction) != "":
		m.prepareTurnAction()
		next, cmd := m.runSlashRuntimeAction(msg.RuntimeAction)
		return next, sequenceCommands(m.flushInlineTranscript(), cmd), true
	case msg.Diff != nil:
		m.prepareTurnAction()
		m.openDiffDialog(msg.Diff.Sources)
		return m, m.flushInlineTranscript(), true
	default:
		return m, nil, false
	}
}

func (m model) handleTurnDialogAction(msg turnDoneMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case msg.PermissionSettings != nil:
		m.prepareTurnAction()
		m.openPermissionSettings(*msg.PermissionSettings)
	case msg.Information != nil:
		m.prepareTurnAction()
		m.openInformation(*msg.Information)
	case msg.CommandView != nil:
		m.prepareTurnAction()
		m.openCommandView(*msg.CommandView)
	default:
		return m, nil, false
	}
	return m, m.flushInlineTranscript(), true
}

func (m model) finalizeTurnDone(msg turnDoneMsg) (tea.Model, tea.Cmd) {
	m.applyTurnCompletionStatus(msg)
	m.streamingIndex = -1
	if m.backgrounding {
		m.status = "backgrounding"
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	flushCmd := m.flushInlineTranscript()
	if len(m.queuedPrompts) == 0 || msg.Err != nil || msg.Interrupted {
		return m, flushCmd
	}
	next := m.queuedPrompts[0]
	m.queuedPrompts = append([]queuedPrompt(nil), m.queuedPrompts[1:]...)
	nextModel, nextCmd := m.startQueuedInput(next)
	return nextModel, sequenceCommands(flushCmd, nextCmd)
}

func (m *model) applyTurnCompletionStatus(msg turnDoneMsg) {
	switch {
	case msg.Interrupted || errors.Is(msg.Err, context.Canceled):
		m.streamingIndex = -1
		m.transcript = append(m.transcript, transcriptEntry{Role: "system", Text: "Interrupted by user."})
		m.status = restoredQueueStatus("interrupted", m.restoreQueuedPrompts())
	case msg.Err != nil:
		m.streamingIndex = -1
		m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
		m.status = restoredQueueStatus("error", m.restoreQueuedPrompts())
	case strings.TrimSpace(msg.Output) != "":
		m.finishStreamingOutput(msg.Role, msg.Output)
		m.status = "ready"
	default:
		m.status = "ready"
	}
}

func restoredQueueStatus(status string, restored int) string {
	if restored == 0 {
		return status
	}
	return fmt.Sprintf("%s · %d queued restored", status, restored)
}

func (m model) updateExternalEditorDone(msg externalEditorDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updatePasteDone(msg pasteDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateBackgroundDone(msg backgroundDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateTaskBoardDone(msg taskBoardDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateTodoListDone(msg todoListDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateRuntimeControlDone(msg runtimeControlDoneMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.status = "control error"
		m.transcript = append(m.transcript, transcriptEntry{Role: "error", Text: msg.Err.Error()})
		m.refreshViewport()
		m.viewport.GotoBottom()
		return m, m.flushInlineTranscript()
	}
	m.applyRuntimeControlResult(msg.Result)
	return m, m.flushInlineTranscript()
}

func (m model) updatePermissionModeSelectDone(msg permissionModeSelectDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateThemeSelectDone(msg themeSelectDoneMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateTurnStream(msg turnStreamMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.layout(msg.Width, msg.Height)
	if m.sessionPicker != nil {
		m.sessionPicker.width = msg.Width
		m.sessionPicker.height = msg.Height
	}
	return m.updateComponents(msg)
}
