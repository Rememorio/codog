package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceTrustDefaultsToTrustSelection(t *testing.T) {
	m := newWorkspaceTrustModel("/workspace/project")
	require.Equal(t, 0, m.selected)
	require.Contains(t, m.View(), "Accessing workspace:")
	require.Contains(t, m.View(), "/workspace/project")
	require.Contains(t, m.View(), "Quick safety check")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(workspaceTrustModel)
	require.True(t, m.decided)
	require.True(t, m.trusted)
	require.NotNil(t, cmd)
	require.Empty(t, m.View())
}

func TestWorkspaceTrustApprovesWithSelectionOrShortcut(t *testing.T) {
	m := newWorkspaceTrustModel("/workspace/project")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(workspaceTrustModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(workspaceTrustModel)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(workspaceTrustModel)
	require.True(t, m.trusted)
	require.NotNil(t, cmd)

	m = newWorkspaceTrustModel("/workspace/project")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(workspaceTrustModel)
	require.True(t, m.trusted)
	require.NotNil(t, cmd)
}

func TestWorkspaceTrustRejectsAndRequiresSecondControlC(t *testing.T) {
	m := newWorkspaceTrustModel("/workspace/project")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(workspaceTrustModel)
	require.Nil(t, cmd)
	require.True(t, m.exitPending)
	require.Contains(t, m.View(), "Ctrl+C again")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(workspaceTrustModel)
	require.True(t, m.decided)
	require.False(t, m.trusted)
	require.NotNil(t, cmd)

	m = newWorkspaceTrustModel("/workspace/project")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(workspaceTrustModel)
	require.True(t, m.decided)
	require.False(t, m.trusted)
	require.NotNil(t, cmd)
}

func TestWorkspaceTrustViewFitsNarrowTerminal(t *testing.T) {
	m := newWorkspaceTrustModel("/workspace/with/a/very/long/project/path")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	m = updated.(workspaceTrustModel)

	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 12, line)
	}
}

func TestWorkspaceTrustIgnoresUnknownTerminalSize(t *testing.T) {
	m := newWorkspaceTrustModel("/workspace/project")
	updated, _ := m.Update(tea.WindowSizeMsg{})
	m = updated.(workspaceTrustModel)

	require.Equal(t, 80, m.width)
	require.Contains(t, m.View(), "Accessing workspace:")
}

func TestPreviewWorkspaceTrustUsesStableDefault(t *testing.T) {
	preview := PreviewWorkspaceTrust("/workspace/project", 80)

	require.Equal(t, 0, preview.SelectedChoice)
	require.Contains(t, preview.View, "Yes, I trust this folder")
	require.Contains(t, preview.View, "No, exit")
}
