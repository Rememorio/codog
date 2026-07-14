package tui

import (
	"fmt"
	"strings"
)

func (m model) promptFooterHints(width int) []string {
	status := strings.ToLower(strings.TrimSpace(m.status))
	if hints, ok := m.interactionFooterHints(); ok {
		return trimFooterHints(hints, width)
	}
	if hints, ok := m.exitAndEditorFooterHints(status); ok {
		return trimFooterHints(hints, width)
	}
	if hints, ok := m.panelFooterHints(status); ok {
		return trimFooterHints(hints, width)
	}
	return trimFooterHints(m.defaultFooterHints(), width)
}

func (m model) interactionFooterHints() (footerHints, bool) {
	hints := footerHints{}
	if m.awaitingPermission {
		if m.permissionInput {
			hints.add("Enter submit")
			hints.add("Tab/Esc collapse")
		} else {
			hints.add("Up/Down choose")
			hints.add("Enter select")
			hints.add("Tab amend")
			hints.add("y/n/a shortcuts")
		}
		return hints, true
	}
	if m.awaitingQuestion {
		hints.add("Up/Down choose")
		hints.add("Enter select")
		hints.add("type custom response")
		return hints, true
	}
	if m.busy {
		hints.add("Esc interrupt")
		if len(m.queuedPrompts) > 0 {
			hints.add(fmt.Sprintf("%d queued", len(m.queuedPrompts)))
			hints.add("Up edit queue")
		} else {
			hints.add("type next prompt to queue")
		}
		if m.background != nil {
			hints.add("Ctrl+B background")
		}
		return hints, true
	}
	if m.backgrounding {
		hints.add("background starting")
		if m.stopBackground != nil {
			hints.add("Ctrl+X Ctrl+K stop")
		}
		return hints, true
	}
	if m.helpOpen {
		hints.add("Esc close help")
		hints.add("/ for commands")
		hints.add("@ for files")
		return hints, true
	}
	return nil, false
}

func (m model) exitAndEditorFooterHints(status string) (footerHints, bool) {
	if hints, ok := m.pendingExitFooterHints(); ok {
		return hints, true
	}
	hints := footerHints{}
	if status == "bash" || isBashModeInput(m.textarea.Value()) {
		hints.add("! for bash mode")
		hints.add("Enter run local command")
		hints.add("Esc clear")
		return hints, true
	}
	if m.vimEnabled && m.vimNormal {
		hints.add("vim NORMAL")
		hints.add("i/a insert")
		hints.add("h/l/w/b move")
		hints.add("x/D/dd delete")
		hints.add("C/cc change")
		hints.add("Enter send")
		return hints, true
	}
	return nil, false
}

func (m model) pendingExitFooterHints() (footerHints, bool) {
	if !m.exitPending {
		return nil, false
	}
	hints := footerHints{}
	switch m.exitKey {
	case "ctrl+c":
		hints.add("Ctrl+C again to exit")
	case "ctrl+d":
		hints.add("Ctrl+D again to exit")
	case "esc":
		if strings.TrimSpace(m.textarea.Value()) != "" || len(m.attachments) != 0 {
			hints.add("Esc again to clear")
		}
	}
	if len(hints) == 0 {
		return nil, false
	}
	hints.add("type to continue")
	hints.add("Ctrl+_ undo")
	return hints, true
}

func (m model) panelFooterHints(status string) (footerHints, bool) {
	hints := footerHints{}
	switch {
	case m.searchOpen:
		hints.add("Enter restore")
		hints.add("Esc close")
	case m.quickOpen:
		hints.add("Enter insert @file")
		hints.add("Shift+Tab path only")
		hints.add("Esc close")
	case m.globalSearch:
		hints.add("Enter insert @line")
		hints.add("Shift+Tab path:line")
		hints.add("Esc close")
	case m.todosOpen:
		hints.add("Ctrl+T close tasks")
		hints.add("/todos manage")
		if m.taskBoard != nil {
			hints.add("Ctrl+Shift+T background tasks")
		}
	case m.modelPicker:
		hints.add("Enter select model")
		hints.add("Esc close")
	case m.messageActions:
		hints.add("Enter apply")
		hints.add("Left/Right target")
		hints.add("Esc close")
	case m.attachmentsOpen:
		hints.add("Left/Right select")
		hints.add("Backspace remove")
		hints.add("Esc close")
	case m.diffDialog:
		hints.add("Enter details")
		hints.add("Left/Right sources")
		hints.add("Esc close")
	case status == "ctrl+x":
		hints.add("Ctrl+E editor")
		hints.add("Ctrl+C compact")
		hints.add("Ctrl+U undo")
		hints.add("Esc cancel")
	default:
		return nil, false
	}
	return hints, true
}

func (m model) defaultFooterHints() footerHints {
	hints := footerHints{}
	if mode := permissionModeFooterLabel(m.modeLabel); mode != "" {
		hints.add(mode + " (Shift+Tab cycle)")
	}
	hints.add("? for shortcuts")
	hints.add("/ commands")
	hints.add("@ files")
	for _, badge := range m.runtimeStatusBadges() {
		hints.add(badge)
	}
	if len(m.attachments) > 0 {
		hints.add(fmt.Sprintf("%d attached", len(m.attachments)))
	}
	if len(m.queuedPrompts) > 0 {
		hints.add(fmt.Sprintf("%d queued", len(m.queuedPrompts)))
	}
	hints.add("Ctrl+R history")
	hints.add("Ctrl+T tasks")
	if m.transcriptMode {
		hints.add("Ctrl+O compact transcript")
	} else {
		hints.add("Ctrl+O transcript")
	}
	if m.vimEnabled {
		if m.vimNormal {
			hints.add("vim: normal")
		} else {
			hints.add("vim: insert")
		}
	}
	if permissionModeFooterLabel(m.modeLabel) == "" && (m.cycleMode != nil || strings.TrimSpace(m.modeLabel) != "") {
		hints.add("Shift+Tab mode")
	}
	if m.stashedPrompt != nil {
		hints.add("Ctrl+S restore stash")
	} else {
		hints.add("Ctrl+S stash")
	}
	if len(m.modelOptions) > 0 {
		hints.add("Alt+P model")
	}
	if m.background != nil {
		hints.add("Ctrl+B background")
	}
	return hints
}
