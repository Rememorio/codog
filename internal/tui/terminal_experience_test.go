package tui

import (
	"context"
	"testing"

	"github.com/Rememorio/codog/internal/companion"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestAltRTogglesRawOutputThroughRuntimeControl(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	enabled := true
	m.toggleRaw = func(context.Context) (RuntimeControlResult, error) {
		return RuntimeControlResult{
			Title:     "Raw Output",
			Status:    "raw output on",
			Lines:     []string{"Raw output: on"},
			RawOutput: &enabled,
		}, nil
	}

	next, cmd := m.updateViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}, Alt: true})
	require.NotNil(t, cmd)
	message := cmd()
	updated, _ := next.Update(message)
	result := updated.(model)
	require.True(t, result.rawOutput)
	require.Contains(t, result.transcript[len(result.transcript)-1].Text, "Raw output")
}

func TestSlashResultAppliesRawOutputAndCompanion(t *testing.T) {
	manifest, err := companion.Load(t.TempDir(), companion.BuiltinID)
	require.NoError(t, err)
	enabled := true
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)

	next, _ := m.updateTurnDone(turnDoneMsg{RawOutput: &enabled})
	raw := next.(model)
	require.True(t, raw.rawOutput)
	require.Contains(t, raw.transcript[len(raw.transcript)-1].Text, "Raw output on")

	next, _ = raw.updateTurnDone(turnDoneMsg{Companion: manifest, CompanionChanged: true})
	withCompanion := next.(model)
	require.Equal(t, companion.BuiltinID, withCompanion.companion.ID)
	require.Contains(t, withCompanion.transcript[len(withCompanion.transcript)-1].Text, "Terminal companion")
}

func TestViewRendersCompanionWithoutExceedingWidth(t *testing.T) {
	manifest, err := companion.Load(t.TempDir(), companion.BuiltinID)
	require.NoError(t, err)
	m := newModel(context.Background(), newPromptTextarea("hello"), nil, nil)
	m.inline = true
	m.companion = manifest
	m.layout(100, 24)

	view := m.View()
	require.Contains(t, view, "Codog")
	require.Contains(t, view, "ready")
	for _, line := range splitLines(view) {
		require.LessOrEqual(t, ansi.StringWidth(line), 100)
	}
}
