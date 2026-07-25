package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/companion"
	"github.com/stretchr/testify/require"
)

func TestCompanionTracksRuntimeStateAndVisibility(t *testing.T) {
	manifest, err := companion.Load(t.TempDir(), companion.BuiltinID)
	require.NoError(t, err)
	m := model{width: 100, companion: manifest}

	require.True(t, m.companionVisible())
	require.Equal(t, "ready", m.companionState())
	m.busy = true
	require.Equal(t, "running", m.companionState())
	m.awaitingPermission = true
	require.Equal(t, "waiting", m.companionState())
	m.awaitingPermission = false
	m.busy = false
	m.status = "provider error"
	require.Equal(t, "failed", m.companionState())

	m.rawOutput = true
	require.False(t, m.companionVisible())
	m.rawOutput = false
	m.width = companionMinimumWidth - 1
	require.False(t, m.companionVisible())
}

func TestJoinComposerCompanionKeepsStableLeftWidth(t *testing.T) {
	joined := joinComposerCompanion("prompt", "pet\nready", 40)
	lines := splitLines(joined)
	require.Len(t, lines, 2)
	require.GreaterOrEqual(t, len([]rune(lines[1])), 40)
}

func TestInlineLayoutReservesCompanionRowsAndRestoresThemForRawOutput(t *testing.T) {
	manifest, err := companion.Load(t.TempDir(), companion.BuiltinID)
	require.NoError(t, err)
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.inline = true
	m.companion = manifest
	m.layout(100, 24)
	withCompanion := m.viewport.Height

	m.setRawOutput(true)
	require.Equal(t, withCompanion+3, m.viewport.Height)
}

func splitLines(value string) []string {
	return append([]string(nil), strings.Split(value, "\n")...)
}
