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
	ta.SetValue("/comp")
	m := model{textarea: ta}

	m = m.completeSlashCommand()
	require.NotEmpty(t, m.matches)
	require.Contains(t, m.matches, "/compact")
	require.Contains(t, m.matches, "/completion")
}

func TestCompleteSlashCommandUsesInjectedCandidates(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("/resume rec")
	m := model{textarea: ta, candidates: []string{"/resume recent-session"}}

	m = m.completeSlashCommand()
	require.Equal(t, "/resume recent-session ", m.textarea.Value())
	require.Empty(t, m.matches)
}

func TestCompleteSlashCommandPreservesCandidateTrailingSpace(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("/model")
	m := model{textarea: ta, candidates: []string{"/model "}}

	m = m.completeSlashCommand()
	require.Equal(t, "/model ", m.textarea.Value())
}

func TestCompleteSlashCommandMatchesAfterTrailingSpace(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("/model ")
	m := model{textarea: ta, candidates: []string{"/model claude-test"}}

	m = m.completeSlashCommand()
	require.Equal(t, "/model claude-test ", m.textarea.Value())
}

func TestPreviewWithCandidatesCompletesAndSubmits(t *testing.T) {
	preview := PreviewWithCandidates("/mo", []string{"/model claude-test"}, 100, 24, true, true)

	require.Contains(t, preview.View, "Codog TUI")
	require.Contains(t, preview.View, "Ctrl+S submit")
	require.Equal(t, "/model claude-test", preview.Prompt)
	require.Equal(t, "/model claude-test ", preview.Value)
	require.True(t, preview.Submitted)
	require.Empty(t, preview.Matches)
}

func TestPromptTextareaUsesPrefill(t *testing.T) {
	ta := newPromptTextarea("review this diff")

	require.Equal(t, "review this diff", ta.Value())
	require.Equal(t, 16000, ta.CharLimit)
	require.Equal(t, "Ask Codog to work on this repository...", ta.Placeholder)
}

func TestPreviewWithCandidatesRendersMultipleMatches(t *testing.T) {
	preview := PreviewWithCandidates("/m", []string{"/model claude-test", "/memory list"}, 90, 20, true, false)

	require.Contains(t, preview.View, "/memory list")
	require.Contains(t, preview.View, "/model claude-test")
	require.ElementsMatch(t, []string{"/model claude-test", "/memory list"}, preview.Matches)
	require.False(t, preview.Submitted)
}
