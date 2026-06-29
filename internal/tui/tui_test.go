package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/stretchr/testify/require"
)

func TestCompleteSlashCommand(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("/doctor")
	m := model{textarea: ta}

	m = m.completeSlashCommand()
	require.Equal(t, "/doctor ", m.textarea.Value())
	require.Empty(t, m.matches)
}

func TestCompleteSlashCommandShowsMultipleMatches(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("/co")
	m := model{textarea: ta}

	m = m.completeSlashCommand()
	require.NotEmpty(t, m.matches)
	require.Contains(t, m.matches, "/compact")
	require.Contains(t, m.matches, "/completion")
}
