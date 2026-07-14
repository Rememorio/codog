package tui

import tea "github.com/charmbracelet/bubbletea"

func (m model) updatePermissionSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateInformationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateThemePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateDiffDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateAttachmentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateMessageActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateGlobalSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) updateQuickOpenKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
