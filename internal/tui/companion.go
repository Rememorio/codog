package tui

import (
	"strings"

	"github.com/Rememorio/codog/internal/companion"
	"github.com/charmbracelet/lipgloss"
)

const companionMinimumWidth = 72

func cloneCompanion(value *companion.Manifest) *companion.Manifest {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Frames.Ready = append([]string(nil), value.Frames.Ready...)
	clone.Frames.Running = append([]string(nil), value.Frames.Running...)
	clone.Frames.Waiting = append([]string(nil), value.Frames.Waiting...)
	clone.Frames.Failed = append([]string(nil), value.Frames.Failed...)
	return &clone
}

func (m model) companionVisible() bool {
	if m.companion == nil || m.width < companionMinimumWidth || m.rawOutput || m.transcriptMode {
		return false
	}
	return !m.companionBlockedByPanel()
}

func (m model) companionBlockedByPanel() bool {
	return m.helpOpen || m.searchOpen || m.quickOpen || m.globalSearch || m.todosOpen ||
		m.modelPicker || m.themePicker || m.messageActions || m.attachmentsOpen ||
		m.diffDialog || m.sessionPicker != nil || m.permissionSettings != nil ||
		m.information != nil || m.commandView != nil || m.exportDialog != nil ||
		m.textInputDialog != nil || len(m.queuedPrompts) > 0 || m.stashedPrompt != nil
}

func (m model) companionState() string {
	switch {
	case m.awaitingPermission || m.awaitingQuestion:
		return "waiting"
	case m.busy || m.backgrounding:
		return "running"
	case companionFailureStatus(m.status):
		return "failed"
	default:
		return "ready"
	}
}

func companionFailureStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return strings.Contains(status, "error") || strings.Contains(status, "fail") ||
		strings.Contains(status, "interrupt")
}

func (m model) renderCompanion(styles themeStyles) string {
	if !m.companionVisible() {
		return ""
	}
	state := m.companionState()
	lines := m.companion.Frame(state)
	lines = append(lines, state)
	return styles.completion().Render(strings.Join(lines, "\n"))
}

func (m model) companionRows() int {
	if !m.companionVisible() {
		return 0
	}
	return len(m.companion.Frame(m.companionState())) + 1
}

func joinComposerCompanion(composerView string, companionView string, contentWidth int) string {
	if companionView == "" {
		return composerView
	}
	left := lipgloss.NewStyle().Width(max(20, contentWidth)).Render(composerView)
	return lipgloss.JoinHorizontal(lipgloss.Bottom, left, "  ", companionView)
}

func (m *model) reflowTerminalExperience() {
	if m.width > 0 && m.height > 0 {
		m.layout(m.width, m.height)
		return
	}
	m.refreshViewport()
}

func companionSelectionStatus(selected *companion.Manifest) string {
	if selected == nil {
		return "companion off"
	}
	return "companion " + selected.ID
}

func companionSelectionNotice(selected *companion.Manifest) string {
	if selected == nil {
		return "Terminal companion off."
	}
	return "Terminal companion: " + selected.Name + "."
}
