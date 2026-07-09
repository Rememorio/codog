package tui

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
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
	require.Equal(t, 0, m.selected)
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

func TestCompletionSelectionUsesArrowKeysAndEnter(t *testing.T) {
	ta := newPromptTextarea("/m")
	m := newModel(context.Background(), ta, []string{"/model claude-test", "/memory list"}, nil)
	m = m.completeSlashCommand()

	require.Equal(t, 0, m.selected)
	require.Len(t, m.matches, 2)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Equal(t, 1, m.selected)
	selected := m.matches[m.selected]

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, completeValue(selected), m.textarea.Value())
	require.Empty(t, m.matches)
	require.Equal(t, 0, m.selected)
}

func TestCompletionListRendersSelectedSuggestion(t *testing.T) {
	view := renderCompletions([]string{"/model claude-test", "/memory list"}, 1)

	require.Contains(t, view, "suggestions")
	require.Contains(t, view, "  /model claude-test")
	require.Contains(t, view, "> /memory list")
}

func TestStatusBarUsesCompactHintsAtTerminalWidth(t *testing.T) {
	text := statusBarText("5 completions", 80)

	require.Contains(t, text, "Enter send")
	require.Contains(t, text, "Tab")
	require.Contains(t, text, "Esc")
	require.Contains(t, text, "Ctrl-R")
	require.NotContains(t, text, "Tab complete")
	require.LessOrEqual(t, len([]rune(text)), 80)
}

func TestPreviewWithCandidatesCompletesAndSubmits(t *testing.T) {
	preview := PreviewWithCandidates("/mo", []string{"/model claude-test"}, 100, 24, true, true)

	require.Contains(t, preview.View, "Codog TUI")
	require.Contains(t, preview.View, "Enter send")
	require.Contains(t, preview.View, "composer")
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
	require.Contains(t, preview.View, "suggestions")
	require.ElementsMatch(t, []string{"/model claude-test", "/memory list"}, preview.Matches)
	require.False(t, preview.Submitted)
}

func TestPreviewTogglesHelpPanel(t *testing.T) {
	preview := PreviewWithCandidates("/help", []string{"/status", "/context"}, 100, 24, false, false)
	require.True(t, preview.HelpOpen)
	require.Contains(t, preview.View, "Common commands")

	ta := newPromptTextarea("/help")
	m := newModel(context.Background(), ta, []string{"/status", "/context"}, nil)
	updated, _ := m.Update(teaKey("enter"))
	next := updated.(model)

	require.True(t, next.helpOpen)
	require.Empty(t, next.textarea.Value())
	require.Contains(t, next.View(), "Common commands")
	require.Contains(t, next.View(), "/status")
}

func TestSlashCommandWithEmptyOutputShowsDone(t *testing.T) {
	cmd := runSlashCommand(context.Background(), func(context.Context, string) (string, bool, error) {
		return "", true, nil
	}, "/clear")
	msg := cmd().(turnDoneMsg)

	require.Equal(t, "Done.", msg.Output)
	require.NoError(t, msg.Err)
}

func TestHistoryNavigationFromEmptyComposer(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.setHistory([]string{"first prompt", "second prompt"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Equal(t, "second prompt", m.textarea.Value())
	require.Equal(t, "history 2/2", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Equal(t, "first prompt", m.textarea.Value())

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Equal(t, "second prompt", m.textarea.Value())

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Empty(t, m.textarea.Value())
}

func TestHistoryKeepsLatestDuplicatePrompt(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.setHistory([]string{"repeat prompt", "middle prompt", "repeat prompt"})

	require.Equal(t, []string{"middle prompt", "repeat prompt"}, m.history)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Equal(t, "repeat prompt", m.textarea.Value())
}

func TestHistorySearchAcceptsSelectedPrompt(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.setHistory([]string{"review auth flow", "write tests", "review tui state"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = updated.(model)
	require.True(t, m.searchOpen)
	require.Empty(t, m.textarea.Value())
	require.Contains(t, m.View(), "history")

	m.textarea.SetValue("review")
	m.updateHistorySearch()
	require.Len(t, m.searchHits, 2)
	require.Equal(t, "review tui state", m.searchHits[0])

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.False(t, m.searchOpen)
	require.Equal(t, "review auth flow", m.textarea.Value())
	require.Equal(t, "history selected", m.status)
}

func TestHistorySearchEscapeRestoresDraft(t *testing.T) {
	ta := newPromptTextarea("unfinished draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.setHistory([]string{"previous prompt"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = updated.(model)
	m.textarea.SetValue("previous")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.searchOpen)
	require.Equal(t, "unfinished draft", m.textarea.Value())
}

func TestSubmittingPromptAppendsTUIHistory(t *testing.T) {
	ta := newPromptTextarea("new prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.submit = func(context.Context, string) (string, error) { return "ok", nil }

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Contains(t, m.history, "new prompt")
	require.Equal(t, -1, m.historyPos)
}

func TestPreviewQuestionMarkOpensHelpWhenComposerEmpty(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, []string{"/status"}, nil)
	updated, _ := m.Update(teaKey("?"))
	next := updated.(model)

	require.True(t, next.helpOpen)
	require.Contains(t, next.View(), "Keys")
	require.Contains(t, next.View(), "/status")
	require.Contains(t, next.View(), "Ctrl+R")
}

func TestEnterSubmitsAndCtrlJInsertsNewline(t *testing.T) {
	ta := newPromptTextarea("first")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(teaKey("ctrl+j"))
	next := updated.(model)
	require.Equal(t, "first\n", next.textarea.Value())

	updated, _ = next.Update(teaKey("enter"))
	next = updated.(model)
	require.True(t, next.result.Submitted)
	require.Equal(t, "first", next.result.Prompt)
}

func TestLongErrorTranscriptWrapsInViewport(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	message := "openai-compatible request failed: 400 Bad Request: provider returned an empty error body; verify the model name, base URL, and credentials with `codog models show MODEL` and `codog providers status`."
	updated, _ = m.Update(turnDoneMsg{Role: "assistant", Err: assertErr{message: message}})
	m = updated.(model)
	view := m.View()

	require.Contains(t, view, "provider returned an empty")
	require.Contains(t, view, "error body")
	require.Contains(t, view, "models show MODEL")
	require.Contains(t, view, "codog providers status")
}

type assertErr struct {
	message string
}

func (e assertErr) Error() string {
	return e.message
}

func teaKey(value string) tea.KeyMsg {
	if value == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	if value == "ctrl+j" {
		return tea.KeyMsg{Type: tea.KeyCtrlJ}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}
