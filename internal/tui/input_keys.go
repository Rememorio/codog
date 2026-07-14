package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Paste {
		return m.handlePastedInput(msg)
	}
	if handle := m.activeModalKeyHandler(); handle != nil {
		return handle(msg)
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
		return m.updateControlCKey(msg)
	case "esc":
		return m.updateEscapeKey(msg)
	case "ctrl+d", "ctrl+l", "ctrl+v":
		return m.updateLifecycleKey(msg)
	case "ctrl+_", "ctrl+shift+-", "ctrl+u", "ctrl+k", "home", "ctrl+a", "end", "ctrl+x", "shift+up":
		return m.updateComposerKey(msg)
	case "ctrl+b":
		return m.updateBackgroundKey(msg)
	case "ctrl+t", "ctrl+shift+t":
		return m.updateTaskKey(msg)
	case "ctrl+shift+p", "ctrl+p", "ctrl+shift+f", "ctrl+f", "alt+p", "meta+p", "alt+o", "meta+o", "alt+t", "meta+t":
		return m.updateActionsKey(msg)
	case "shift+enter", "alt+enter", "ctrl+j", "ctrl+s", "ctrl+o", "ctrl+g", "pgup", "pgdown":
		return m.updateViewKey(msg)
	case "up", "down":
		return m.updateDirectionalKey(msg)
	case "tab", "shift+tab", "alt+m", "meta+m", "ctrl+r", "y", "Y", "a", "A", "n", "N", "?":
		return m.updateNavigationKey(msg)
	case "enter":
		return m.updateSubmitKey(msg)
	}
	return m.updateAfterDefaultKey(msg)
}

type tuiKeyHandler func(tea.KeyMsg) (tea.Model, tea.Cmd)

func (m model) activeModalKeyHandler() tuiKeyHandler {
	switch {
	case m.sessionPicker != nil:
		return m.updateSessionPicker
	case m.exportDialog != nil:
		return m.updateExportDialog
	case m.textInputDialog != nil:
		return m.updateTextInputDialog
	case m.awaitingPermission:
		return m.updatePermissionRequest
	case m.awaitingQuestion:
		return m.updateQuestionRequest
	case m.permissionSettings != nil:
		return m.updatePermissionSettingsKey
	case m.commandView != nil:
		return m.updateCommandView
	case m.information != nil:
		return m.updateInformationKey
	case m.themePicker:
		return m.updateThemePickerKey
	case m.diffDialog:
		return m.updateDiffDialogKey
	case m.attachmentsOpen:
		return m.updateAttachmentsKey
	case m.modelPicker:
		return m.updateModelPickerKey
	case m.messageActions:
		return m.updateMessageActionsKey
	case m.globalSearch:
		return m.updateGlobalSearchKey
	case m.quickOpen:
		return m.updateQuickOpenKey
	default:
		return nil
	}
}

func (m model) updateAfterDefaultKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if next, handled, cmd := m.handleVimNormalKey(msg); handled {
		return next, cmd
	}
	return m.updateComponents(msg)
}

func (m model) updateControlCKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateEscapeKey(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.handleVimEscape() || m.handleTransientEscape() {
		return m, nil
	}
	return m.updateIdleEscape()
}

func (m *model) handleVimEscape() bool {
	if m.shouldEnterVimNormalMode() {
		m.vimNormal = true
		m.matches = nil
		m.selected = 0
		m.commandArgumentHint = ""
		m.inlineGhostText = ""
		m.clearExitPending()
		m.status = "vim normal"
		return true
	}
	if m.vimEnabled && m.vimNormal && m.vimKeybindingsAvailable() && strings.TrimSpace(m.textarea.Value()) != "" {
		m.status = "vim normal"
		return true
	}
	return false
}

func (m *model) handleTransientEscape() bool {
	switch {
	case m.busy:
		m.interruptTurn()
	case m.backgrounding:
		m.interruptBackground()
	case len(m.matches) > 0:
		m.matches = nil
		m.selected = 0
		m.commandArgumentHint = ""
		m.inlineGhostText = ""
		m.clearExitPending()
		m.status = m.mode()
	case m.searchOpen:
		m.clearExitPending()
		m.closeHistorySearch(false)
	case m.todosOpen:
		m.clearExitPending()
		m.closeTodos()
	case m.helpOpen:
		m.clearExitPending()
		m.helpOpen = false
		m.status = "ready"
		m.refreshViewport()
	default:
		return false
	}
	return true
}

func (m model) updateIdleEscape() (tea.Model, tea.Cmd) {
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
}

func (m model) updateLifecycleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateComposerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+_", "ctrl+shift+-":
		if m.busy || m.composerEditingBlocked() {
			return m, nil
		}
		m.undoComposer()
		return m, nil
	case "ctrl+u":
		if m.composerEditingBlocked() {
			return m, nil
		}
		m.deleteComposerBeforeCursor()
		return m, nil
	case "ctrl+k":
		if m.composerEditingBlocked() {
			return m, nil
		}
		m.deleteComposerAfterCursor()
		return m, nil
	case "home", "ctrl+a":
		if m.composerEditingBlocked() {
			return m, nil
		}
		m.moveComposerLineStart()
		return m, nil
	case "end":
		if m.composerEditingBlocked() {
			return m, nil
		}
		m.moveComposerLineEnd()
		return m, nil
	case "ctrl+x":
		if m.busy || m.composerEditingBlocked() {
			return m, nil
		}
		m.ctrlXChord = true
		m.status = "ctrl+x"
		return m, nil
	case "shift+up":
		if m.actionBlocked() || m.modelPicker {
			return m, nil
		}
		m.openMessageActions()
		return m, nil
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) composerEditingBlocked() bool {
	return m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen || m.awaitingPermission || m.awaitingQuestion
}

func (m model) actionBlocked() bool {
	return m.busy || m.backgrounding || m.composerEditingBlocked()
}

func (m model) updateBackgroundKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+b":
		if m.backgrounding || m.background == nil || m.composerEditingBlocked() {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateTaskKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+t":
		if m.searchOrPromptOpen() {
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
		if m.taskBoard == nil || m.composerEditingBlocked() {
			return m, nil
		}
		return m.openTaskBoard()
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+shift+p", "ctrl+p":
		if m.actionBlocked() || len(m.fileCandidates) == 0 {
			return m, nil
		}
		m.openQuickOpen()
		return m, nil
	case "ctrl+shift+f", "ctrl+f":
		if m.actionBlocked() || len(m.fileCandidates) == 0 {
			return m, nil
		}
		m.openGlobalSearch()
		return m, nil
	case "alt+p", "meta+p":
		if m.actionBlocked() {
			return m, nil
		}
		m.openModelPicker()
		return m, nil
	case "alt+o", "meta+o":
		if m.actionBlocked() || m.modelPicker || m.toggleFast == nil {
			return m, nil
		}
		m.status = "fast mode"
		return m, runRuntimeControlCommand(m.ctx, m.toggleFast)
	case "alt+t", "meta+t":
		if m.actionBlocked() || m.modelPicker || m.toggleThinking == nil {
			return m, nil
		}
		m.status = "thinking"
		return m, runRuntimeControlCommand(m.ctx, m.toggleThinking)
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) searchOrPromptOpen() bool {
	return m.searchOpen || m.quickOpen || m.globalSearch || m.awaitingPermission || m.awaitingQuestion
}

func (m model) updateViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateDirectionalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateNavigationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	}
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateSubmitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
	return m.updateAfterDefaultKey(msg)
}

func (m model) updateComponents(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	switch {
	case isLocalHelpInput(m.textarea.Value()):
		m.status = "help ready"
	case m.awaitingQuestion:
		m.status = "question"
	case m.backgrounding:
		m.status = "backgrounding"
	case m.busy:
		m.status = "running"
	default:
		m.status = m.mode()
	}
	return m, tea.Batch(cmd, viewportCmd)
}
