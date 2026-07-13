package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ConfirmWorkspaceTrust asks whether project files may participate in the
// interactive agent runtime. Escape, explicit rejection, and a confirmed
// Ctrl-C exit deny access.
func ConfirmWorkspaceTrust(ctx context.Context, workspace string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	model := newWorkspaceTrustModel(workspace)
	final, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if err != nil {
		return false, err
	}
	decision, ok := final.(workspaceTrustModel)
	return ok && decision.decided && decision.trusted, nil
}

type workspaceTrustModel struct {
	workspace   string
	selected    int
	trusted     bool
	decided     bool
	exitPending bool
	width       int
}

func newWorkspaceTrustModel(workspace string) workspaceTrustModel {
	return workspaceTrustModel{
		workspace: strings.TrimSpace(workspace),
		selected:  0,
		width:     80,
	}
}

func (m workspaceTrustModel) Init() tea.Cmd {
	return nil
}

func (m workspaceTrustModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = max(12, msg.Width)
		}
	case tea.KeyMsg:
		key := msg.String()
		if m.exitPending && key != "ctrl+c" {
			m.exitPending = false
		}
		switch key {
		case "up", "left", "shift+tab":
			m.selected = 0
		case "down", "right", "tab":
			m.selected = 1
		case "y", "Y":
			m.trusted = true
			m.decided = true
			return m, tea.Quit
		case "n", "N", "esc":
			m.trusted = false
			m.decided = true
			return m, tea.Quit
		case "ctrl+c":
			if m.exitPending {
				m.trusted = false
				m.decided = true
				return m, tea.Quit
			}
			m.exitPending = true
		case "enter":
			m.trusted = m.selected == 0
			m.decided = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m workspaceTrustModel) View() string {
	if m.decided {
		return ""
	}
	width := max(12, m.width)
	contentWidth := max(1, width-2)
	workspace := truncateFooterLine(m.workspace, contentWidth)
	lines := []string{
		inlineHeaderStyle().Render(truncateFooterLine("Accessing workspace:", contentWidth)),
		inlineStatusStyle().Render(workspace),
		"",
	}
	lines = append(lines, wrapTranscriptLine("Quick safety check: Is this a project you created or one you trust? If not, review this folder first.", width)...)
	lines = append(lines, wrapTranscriptLine("Codog can read, edit, and execute files here. Project settings may also configure hooks, plugins, and MCP servers.", width)...)
	lines = append(lines,
		"",
		renderTrustChoice("Yes, I trust this folder", m.selected == 0, width),
		renderTrustChoice("No, exit", m.selected == 1, width),
	)
	footer := "↑/↓ select · Enter confirm · Esc cancel"
	if m.exitPending {
		footer = "Press Ctrl+C again to exit"
	}
	lines = append(lines, inlineStatusStyle().Render(truncateFooterLine(footer, contentWidth)))
	for index := range lines {
		lines[index] = truncateFooterLine(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

// WorkspaceTrustPreview is a deterministic trust prompt snapshot used by
// capability checks without taking control of a terminal.
type WorkspaceTrustPreview struct {
	View           string
	SelectedChoice int
}

// PreviewWorkspaceTrust renders the workspace trust prompt at a fixed width.
func PreviewWorkspaceTrust(workspace string, width int) WorkspaceTrustPreview {
	m := newWorkspaceTrustModel(workspace)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	m = updated.(workspaceTrustModel)
	return WorkspaceTrustPreview{View: m.View(), SelectedChoice: m.selected}
}

func renderTrustChoice(label string, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "❯ "
	}
	line := truncateFooterLine(marker+label, max(1, width))
	if selected {
		return selectedCompletionStyle().Render(line)
	}
	return line
}
