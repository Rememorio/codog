package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestSessionPickerFiltersAndSelects(t *testing.T) {
	m := newSessionPickerModel([]SessionChoice{
		{ID: "session-alpha", Title: "Review auth flow", MessageCount: 4},
		{ID: "session-beta", Title: "Fix scheduler", MessageCount: 8},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("scheduler")})
	m = updated.(sessionPickerModel)
	require.Len(t, m.filtered, 1)
	require.Contains(t, m.View(), "Fix scheduler")
	require.NotContains(t, m.View(), "Review auth flow")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(sessionPickerModel)
	require.Equal(t, "session-beta", m.selectedID)
	require.NotNil(t, cmd)
	_, ok := cmd().(tea.QuitMsg)
	require.True(t, ok)
	require.Empty(t, m.View())
}

func TestSessionPickerEscapeClearsFilterBeforeCanceling(t *testing.T) {
	m := newSessionPickerModel([]SessionChoice{{ID: "session-alpha", Title: "Alpha"}})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("missing")})
	m = updated.(sessionPickerModel)
	require.Empty(t, m.filtered)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(sessionPickerModel)
	require.Nil(t, cmd)
	require.Empty(t, m.query)
	require.Len(t, m.filtered, 1)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(sessionPickerModel)
	require.True(t, m.canceled)
	require.NotNil(t, cmd)
}

func TestSessionPickerNavigationAndNarrowLayout(t *testing.T) {
	choices := []SessionChoice{
		{ID: "one", Title: "One", MessageCount: 1},
		{ID: "two", Title: "Two", MessageCount: 2},
		{ID: "three", Title: "Three", MessageCount: 3},
	}
	m := newSessionPickerModel(choices)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 28, Height: 8})
	m = updated.(sessionPickerModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(sessionPickerModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(sessionPickerModel)
	require.Equal(t, 2, m.selected)

	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 28, line)
	}

	m.query = "missing"
	m.applyFilter()
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 12, Height: 6})
	m = updated.(sessionPickerModel)
	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 12, line)
	}
}

func TestRelativeSessionTime(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "now", relativeSessionTime(now.Add(-30*time.Second), now))
	require.Equal(t, "12m", relativeSessionTime(now.Add(-12*time.Minute), now))
	require.Equal(t, "3h", relativeSessionTime(now.Add(-3*time.Hour), now))
	require.Equal(t, "4d", relativeSessionTime(now.Add(-4*24*time.Hour), now))
	require.Equal(t, "2026-05-01", relativeSessionTime(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), now))
}
