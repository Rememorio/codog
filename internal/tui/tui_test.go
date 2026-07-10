package tui

import (
	"context"
	"errors"
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
	require.Contains(t, view, "Show or switch the current model.")
	require.Contains(t, view, "List, search, show")
}

func TestCompletionDisplayLineFallsBackForCustomCandidate(t *testing.T) {
	require.Equal(t, "/custom thing", completionDisplayLine("/custom thing"))
}

func TestQueuedPromptsRenderBelowComposer(t *testing.T) {
	view := renderQueuedPrompts([]string{"first queued prompt", "second queued prompt"})

	require.Contains(t, view, "queued prompts: 2")
	require.Contains(t, view, "1. first queued prompt")
	require.Contains(t, view, "2. second queued prompt")
	require.Empty(t, renderQueuedPrompts(nil))
}

func TestQueuedPromptPreviewTruncatesOlderItems(t *testing.T) {
	view := renderQueuedPrompts([]string{"one", "two", "three", "four"})

	require.Contains(t, view, "... 1 earlier")
	require.NotContains(t, view, "1. one")
	require.Contains(t, view, "2. two")
	require.Contains(t, view, "4. four")
}

func TestPreviewWithQueuedRendersQueuedPrompts(t *testing.T) {
	preview := PreviewWithQueued("", []string{"review auth", "write tests"}, 96, 24)

	require.Contains(t, preview.View, "queued prompts: 2")
	require.Contains(t, preview.View, "review auth")
	require.Contains(t, preview.View, "write tests")
	require.Contains(t, preview.View, "2 queued")
}

func TestPreviewWithAttachmentsRendersPendingAttachments(t *testing.T) {
	preview := PreviewWithAttachments("describe", []string{"notes.txt", "pixel.png"}, 96, 24)

	require.Equal(t, []string{"notes.txt", "pixel.png"}, preview.Attachments)
	require.Contains(t, preview.View, "attachments: 2")
	require.Contains(t, preview.View, "notes.txt")
	require.Contains(t, preview.View, "2 attached")
}

func TestSlashMenuOpensAndFiltersWhileTyping(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, []string{"/memory list", "/model claude-test", "/status"}, nil)

	updated, _ := m.Update(teaKey("/"))
	m = updated.(model)
	require.NotEmpty(t, m.matches)
	require.Contains(t, m.View(), "suggestions")

	updated, _ = m.Update(teaKey("m"))
	m = updated.(model)
	require.ElementsMatch(t, []string{"/memory list", "/model claude-test"}, m.matches)
	require.Equal(t, "2 completions", m.status)
}

func TestSlashMenuEscapeClosesSuggestionsBeforeQuitting(t *testing.T) {
	ta := newPromptTextarea("/m")
	m := newModel(context.Background(), ta, []string{"/memory list", "/model claude-test"}, nil)
	m.refreshSlashMenu()
	require.NotEmpty(t, m.matches)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.matches)
	require.Equal(t, "slash", m.status)
	require.False(t, m.result.Submitted)
}

func TestSlashMenuDoesNotInterceptExactLocalHelp(t *testing.T) {
	ta := newPromptTextarea("/help")
	m := newModel(context.Background(), ta, []string{"/help"}, nil)
	m.refreshSlashMenu()
	require.Empty(t, m.matches)

	updated, _ := m.Update(teaKey("enter"))
	m = updated.(model)

	require.True(t, m.helpOpen)
	require.Empty(t, m.textarea.Value())
	require.Contains(t, m.View(), "Common commands")
}

func TestStatusBarUsesCompactHintsAtTerminalWidth(t *testing.T) {
	text := statusBarText("5 completions", 80)

	require.Contains(t, text, "Enter send")
	require.Contains(t, text, "Shift+Enter newline")
	require.Contains(t, text, "Tab")
	require.Contains(t, text, "Ctrl-R")
	require.Contains(t, text, "Ctrl-D")
	require.NotContains(t, text, "Tab complete")
	require.NotContains(t, text, "Alt+Enter")
	require.LessOrEqual(t, len([]rune(text)), 80)
}

func TestStatusBarShowsCancelHintWhileRunning(t *testing.T) {
	text := statusBarText("running", 80)

	require.Contains(t, text, "cancel")
	require.Contains(t, text, "Esc")
	require.NotContains(t, text, "Enter send")
	require.LessOrEqual(t, len([]rune(text)), 80)
}

func TestShiftTabCyclesTUILocalMode(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "default"
	m.cycleMode = func() string { return "accept edits" }

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(model)

	require.Equal(t, "accept edits", m.modeLabel)
	require.Equal(t, "system", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "Mode: accept edits", m.transcript[len(m.transcript)-1].Text)
	require.Contains(t, m.View(), "accept edits")
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

func TestBusyEnterQueuesPromptsAndRunsAfterTurnDone(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	prompts := []string{}
	m.submit = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "done: " + prompt, nil
	}

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, firstCmd)

	m.textarea.SetValue("second prompt")
	updated, queueCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"second prompt"}, m.queuedPrompts)
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "queued", m.status)
	require.Contains(t, m.View(), "Queued prompt 1")
	require.Contains(t, m.View(), "1 queued")

	m.textarea.SetValue("third prompt")
	updated, queueCmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"second prompt", "third prompt"}, m.queuedPrompts)
	require.Contains(t, m.View(), "Queued prompt 2")
	require.Contains(t, m.View(), "2 queued")

	firstDone := firstCmd().(turnDoneMsg)
	updated, secondCmd := m.Update(firstDone)
	m = updated.(model)
	require.True(t, m.busy)
	require.Equal(t, []string{"third prompt"}, m.queuedPrompts)
	require.NotNil(t, secondCmd)
	require.Equal(t, []string{"first prompt"}, prompts)
	require.Equal(t, "user", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "second prompt", m.transcript[len(m.transcript)-1].Text)

	secondDone := secondCmd().(turnDoneMsg)
	updated, thirdCmd := m.Update(secondDone)
	m = updated.(model)
	require.True(t, m.busy)
	require.Empty(t, m.queuedPrompts)
	require.NotNil(t, thirdCmd)
	require.Equal(t, []string{"first prompt", "second prompt"}, prompts)
	require.Equal(t, "user", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "third prompt", m.transcript[len(m.transcript)-1].Text)

	thirdDone := thirdCmd().(turnDoneMsg)
	updated, _ = m.Update(thirdDone)
	m = updated.(model)
	require.False(t, m.busy)
	require.Equal(t, []string{"first prompt", "second prompt", "third prompt"}, prompts)
	require.Contains(t, m.View(), "done: third prompt")
}

func TestUpEditsQueuedPromptsWhileBusy(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	prompts := []string{}
	m.submit = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "done: " + prompt, nil
	}

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, firstCmd)

	m.textarea.SetValue("second prompt")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	m.textarea.SetValue("third prompt")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, []string{"second prompt", "third prompt"}, m.queuedPrompts)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "second prompt\nthird prompt", m.textarea.Value())
	require.Equal(t, "editing queued prompts", m.status)
	require.Contains(t, m.View(), "Editing 2 queued prompts.")
	require.NotContains(t, m.View(), "queued prompts:")

	m.textarea.SetValue("second prompt\nthird prompt edited")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Equal(t, []string{"second prompt\nthird prompt edited"}, m.queuedPrompts)
	require.Empty(t, m.textarea.Value())

	firstDone := firstCmd().(turnDoneMsg)
	updated, secondCmd := m.Update(firstDone)
	m = updated.(model)
	require.NotNil(t, secondCmd)
	queuedDone := secondCmd().(turnDoneMsg)
	updated, _ = m.Update(queuedDone)
	m = updated.(model)

	require.Equal(t, []string{"first prompt", "second prompt\nthird prompt edited"}, prompts)
	require.False(t, m.busy)
	require.Empty(t, m.queuedPrompts)
}

func TestUpEditsQueuedPromptsWithCurrentComposer(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.status = "running"
	m.queuedPrompts = []string{"queued one", "queued two"}
	m.textarea.SetValue("draft")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "queued one\nqueued two\ndraft", m.textarea.Value())
	require.Equal(t, "editing queued prompts", m.status)
}

func TestEscapeCancelsBusyTurnWithoutQuitting(t *testing.T) {
	ta := newPromptTextarea("long running prompt")
	m := newModel(context.Background(), ta, nil, nil)
	var seen context.Context
	m.submit = func(ctx context.Context, prompt string) (string, error) {
		seen = ctx
		<-ctx.Done()
		return "", ctx.Err()
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, m.turnCancel)
	require.NotNil(t, cmd)

	updated, quit := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, quit)
	require.True(t, m.busy)
	require.Equal(t, "interrupting", m.status)

	msg := cmd().(turnDoneMsg)
	require.True(t, msg.Interrupted)
	require.ErrorIs(t, msg.Err, context.Canceled)
	require.ErrorIs(t, seen.Err(), context.Canceled)

	updated, _ = m.Update(msg)
	m = updated.(model)
	require.False(t, m.busy)
	require.Nil(t, m.turnCancel)
	require.Equal(t, "interrupted", m.status)
	require.Contains(t, m.View(), "Interrupted by user.")
}

func TestInterruptedTurnDropsQueuedPrompts(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.submit = func(ctx context.Context, prompt string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	m.textarea.SetValue("queued prompt")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Len(t, m.queuedPrompts, 1)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	done := firstCmd().(turnDoneMsg)
	updated, _ = m.Update(done)
	m = updated.(model)

	require.False(t, m.busy)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "interrupted", m.status)
}

func TestStreamedTurnDeltasRenderBeforeDone(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(turnStreamMsg{Role: "assistant", Delta: "streaming "})
	m = updated.(model)
	require.Equal(t, "streaming", m.status)
	require.Contains(t, m.View(), "streaming")

	updated, _ = m.Update(turnStreamMsg{Role: "assistant", Delta: "answer"})
	m = updated.(model)
	require.Contains(t, m.View(), "streaming answer")

	updated, _ = m.Update(turnDoneMsg{Role: "assistant", Output: "streaming answer"})
	m = updated.(model)
	require.Equal(t, "ready", m.status)
	require.Contains(t, m.View(), "streaming answer")
	require.NotContains(t, m.View(), "streaming answer\nstreaming answer")
}

func TestRunStreamSubmitCommandEmitsDeltasBeforeDone(t *testing.T) {
	ctx := context.Background()
	messages := make(chan tea.Msg, 4)
	cmd := runStreamSubmitCommand(ctx, func(_ context.Context, prompt string, emit func(Entry)) (string, error) {
		emit(Entry{Role: "assistant", Text: "first "})
		emit(Entry{Role: "assistant", Text: prompt})
		return "", nil
	}, "chunk", messages)

	first := cmd()
	require.Equal(t, turnStreamMsg{Role: "assistant", Delta: "first "}, first)
	second := waitTurnMessage(messages)()
	require.Equal(t, turnStreamMsg{Role: "assistant", Delta: "chunk"}, second)
	done := waitTurnMessage(messages)()
	require.IsType(t, turnDoneMsg{}, done)
}

func TestToolStreamEntryDoesNotMergeIntoAssistantText(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(turnStreamMsg{Role: "assistant", Delta: "thinking"})
	m = updated.(model)
	updated, _ = m.Update(turnStreamMsg{Role: "tool", Delta: "Tools\n- bash ok"})
	m = updated.(model)
	updated, _ = m.Update(turnStreamMsg{Role: "tool", Delta: "Tools\n- grep ok"})
	m = updated.(model)
	updated, _ = m.Update(turnStreamMsg{Role: "assistant", Delta: "done"})
	m = updated.(model)

	require.Len(t, m.transcript, 5)
	require.Equal(t, "assistant", m.transcript[1].Role)
	require.Equal(t, "thinking", m.transcript[1].Text)
	require.Equal(t, "tool", m.transcript[2].Role)
	require.Contains(t, m.transcript[2].Text, "bash ok")
	require.Equal(t, "tool", m.transcript[3].Role)
	require.Contains(t, m.transcript[3].Text, "grep ok")
	require.Equal(t, "assistant", m.transcript[4].Role)
	require.Equal(t, "done", m.transcript[4].Text)
}

func TestPermissionStreamEntryAcceptsKeyboardAnswer(t *testing.T) {
	ta := newPromptTextarea("")
	answers := []string{}
	m := newModel(context.Background(), ta, nil, nil)
	m.permissionAnswer = func(answer string) {
		answers = append(answers, answer)
	}

	updated, _ := m.Update(turnStreamMsg{Role: "permission", Delta: "Permission\n- bash requires danger-full-access"})
	m = updated.(model)
	require.True(t, m.awaitingPermission)
	require.Equal(t, "permission", m.status)
	require.Contains(t, statusBarText(m.status, 80), "y approve")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(model)

	require.False(t, m.awaitingPermission)
	require.Equal(t, []string{"y"}, answers)
	require.Equal(t, "permission answered", m.status)

	updated, _ = m.Update(turnStreamMsg{Role: "permission", Delta: "Permission\n- bash approved: user_approved"})
	m = updated.(model)
	require.False(t, m.awaitingPermission)
	require.Equal(t, "permission answered", m.status)
}

func TestQuestionStreamEntryAcceptsComposerAnswerWhileBusy(t *testing.T) {
	ta := newPromptTextarea("")
	answers := []string{}
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) {
		answers = append(answers, answer)
	}

	updated, _ := m.Update(turnStreamMsg{Role: "question", Delta: "Pick a TUI lane\n  1. alpha\n  2. beta\nAnswer:"})
	m = updated.(model)
	require.True(t, m.awaitingQuestion)
	require.Equal(t, "question", m.status)
	require.Contains(t, statusBarText(m.status, 80), "Enter reply")

	m.textarea.SetValue("2")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.awaitingQuestion)
	require.Equal(t, []string{"2"}, answers)
	require.Equal(t, "question answered", m.status)
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "user", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "2", m.transcript[len(m.transcript)-1].Text)
}

func TestCanceledSlashCommandRendersInterrupted(t *testing.T) {
	cmd := runSlashCommand(context.Background(), func(context.Context, string) (string, bool, error) {
		return "", true, context.Canceled
	}, "/status")
	msg := cmd().(turnDoneMsg)

	require.True(t, msg.Interrupted)
	require.True(t, errors.Is(msg.Err, context.Canceled))
}

func TestPreviewQuestionMarkOpensHelpWhenComposerEmpty(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, []string{"/status"}, nil)
	updated, _ := m.Update(teaKey("?"))
	next := updated.(model)

	require.True(t, next.helpOpen)
	require.Contains(t, next.View(), "Keys")
	require.Contains(t, next.View(), "/status")
	require.Contains(t, next.View(), "Shift+Enter")
	require.Contains(t, next.View(), "Alt+Enter")
	require.Contains(t, helpPanel([]string{"/status"}, 100), "\\+Enter")
}

func TestEnterSubmitsAndNewlineShortcutsInsertNewline(t *testing.T) {
	ta := newPromptTextarea("first")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(teaKey("ctrl+j"))
	next := updated.(model)
	require.Equal(t, "first\n", next.textarea.Value())

	next.textarea.SetValue("first")
	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	next = updated.(model)
	require.Equal(t, "first\n", next.textarea.Value())

	updated, _ = next.Update(teaKey("enter"))
	next = updated.(model)
	require.True(t, next.result.Submitted)
	require.Equal(t, "first", next.result.Prompt)
}

func TestBackslashEnterInsertsNewlineInsteadOfSubmitting(t *testing.T) {
	ta := newPromptTextarea("first\\")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(teaKey("enter"))
	next := updated.(model)

	require.Nil(t, cmd)
	require.False(t, next.result.Submitted)
	require.Equal(t, "first\n", next.textarea.Value())
	require.Equal(t, "newline", next.status)

	next.textarea.InsertString("second")
	updated, _ = next.Update(teaKey("enter"))
	next = updated.(model)
	require.True(t, next.result.Submitted)
	require.Equal(t, "first\nsecond", next.result.Prompt)
}

func TestBackslashEnterHandlesWhitespaceAndEscapedBackslash(t *testing.T) {
	ta := newPromptTextarea("first\\  ")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(teaKey("enter"))
	next := updated.(model)
	require.Equal(t, "first\n  ", next.textarea.Value())
	require.False(t, next.result.Submitted)

	ta = newPromptTextarea("literal\\\\")
	m = newModel(context.Background(), ta, nil, nil)
	updated, _ = m.Update(teaKey("enter"))
	next = updated.(model)
	require.True(t, next.result.Submitted)
	require.Equal(t, "literal\\\\", next.result.Prompt)
}

func TestExternalEditorShortcutUpdatesComposer(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.externalEditor = func(_ context.Context, value string) (string, error) {
		require.Equal(t, "draft", value)
		return "edited\nvalue", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	next := updated.(model)
	require.Equal(t, "editing", next.status)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "edited\nvalue", next.textarea.Value())
	require.Equal(t, "editor updated", next.status)
}

func TestExternalEditorShortcutRendersErrors(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.externalEditor = func(context.Context, string) (string, error) {
		return "", errors.New("editor failed")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	next := updated.(model)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "draft", next.textarea.Value())
	require.Equal(t, "editor error", next.status)
	require.Equal(t, "error", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "editor failed")
}

func TestAttachCommandStagesAttachmentsForNextPrompt(t *testing.T) {
	ta := newPromptTextarea("/attach notes.txt pixel.png")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, []string{"notes.txt", "pixel.png"}, next.attachments)
	require.Empty(t, next.textarea.Value())
	require.Equal(t, "2 attached", next.status)
	require.Contains(t, next.View(), "attachments: 2")
	require.Contains(t, next.View(), "notes.txt")
	require.Contains(t, next.View(), "2 attached")
	require.Equal(t, "system", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "Pending attachments: 2")
}

func TestAttachmentCommandsListRemoveAndClear(t *testing.T) {
	ta := newPromptTextarea("/attach one.txt two.txt")
	m := newModel(context.Background(), ta, nil, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	m.textarea.SetValue("/attach remove 1")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, []string{"two.txt"}, m.attachments)
	require.Equal(t, "attachment removed", m.status)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "two.txt")

	m.textarea.SetValue("/attachments list")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, "attachments", m.status)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "Pending attachments: 1")

	m.textarea.SetValue("/attach clear")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Empty(t, m.attachments)
	require.Equal(t, "attachments cleared", m.status)
}

func TestSubmittingWithAttachmentsPassesThemToSubmitter(t *testing.T) {
	ta := newPromptTextarea("describe these")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"notes.txt", "pixel.png"}
	var gotPrompt string
	var gotAttachments []string
	m.submitStreamAttachments = func(_ context.Context, prompt string, attachments []string, emit func(Entry)) (string, error) {
		gotPrompt = prompt
		gotAttachments = append([]string(nil), attachments...)
		emit(Entry{Role: "assistant", Text: "working"})
		return "done", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(model)
	require.True(t, next.busy)
	require.Empty(t, next.attachments)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "Attachments:")
	require.NotNil(t, cmd)

	updated, cmd = next.Update(cmd())
	next = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "describe these", gotPrompt)
	require.Equal(t, []string{"notes.txt", "pixel.png"}, gotAttachments)
	require.False(t, next.busy)
	require.Contains(t, next.View(), "working")
}

func TestPendingAttachmentsCannotBeQueuedWhileBusy(t *testing.T) {
	ta := newPromptTextarea("queued with file")
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.attachments = []string{"notes.txt"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, next.queuedPrompts)
	require.Equal(t, []string{"notes.txt"}, next.attachments)
	require.Equal(t, "attachments pending", next.status)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "Send or clear pending attachments")
}

func TestCtrlTShowsTaskBoard(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.taskBoard = func(context.Context) (string, error) {
		return "Background tasks\n  Active   1", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	next := updated.(model)
	require.Equal(t, "loading tasks", next.status)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "tasks", next.status)
	require.Equal(t, "system", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "Background tasks")
	require.Contains(t, next.View(), "Active")
}

func TestCtrlTRendersTaskBoardErrors(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.taskBoard = func(context.Context) (string, error) {
		return "", errors.New("task board failed")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	next := updated.(model)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "tasks error", next.status)
	require.Equal(t, "error", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "task board failed")
}

func TestCtrlDExitsWhenComposerIsEmpty(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next := updated.(model)

	require.NotNil(t, cmd)
	_, ok := cmd().(tea.QuitMsg)
	require.True(t, ok)
	require.False(t, next.result.Submitted)
}

func TestCtrlDDeletesForwardWhenComposerHasText(t *testing.T) {
	ta := newPromptTextarea("abc")
	ta.CursorStart()
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next := updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "bc", next.textarea.Value())
}

func TestCtrlLClearsVisibleTranscript(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, []transcriptEntry{
		{Role: "user", Text: "old prompt"},
		{Role: "assistant", Text: "old answer"},
	})
	m.matches = []string{"/model"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	next := updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "draft", next.textarea.Value())
	require.Empty(t, next.matches)
	require.Equal(t, "cleared", next.status)
	require.Equal(t, []transcriptEntry{{Role: "system", Text: "Screen cleared."}}, next.transcript)
	require.Contains(t, next.View(), "Screen cleared.")
	require.NotContains(t, next.View(), "old answer")
}

func TestCtrlUAndCtrlKEditComposer(t *testing.T) {
	ta := newPromptTextarea("first second")
	ta.CursorEnd()
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	next := updated.(model)
	require.Empty(t, next.textarea.Value())

	next.textarea.SetValue("first second")
	next.textarea.CursorStart()
	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	next = updated.(model)
	require.Empty(t, next.textarea.Value())
}

func TestCtrlBStartsBackgroundPrompt(t *testing.T) {
	ta := newPromptTextarea("review this diff")
	m := newModel(context.Background(), ta, nil, nil)
	m.background = func(_ context.Context, prompt string) (string, error) {
		require.Equal(t, "review this diff", prompt)
		return "Background task started: task-1", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	next := updated.(model)
	require.False(t, next.busy)
	require.True(t, next.backgrounding)
	require.Equal(t, "backgrounding", next.status)
	require.NotNil(t, cmd)
	require.Equal(t, "review this diff", next.textarea.Value())
	require.Contains(t, next.history, "review this diff")

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.False(t, next.busy)
	require.False(t, next.backgrounding)
	require.Empty(t, next.textarea.Value())
	require.Equal(t, "backgrounded", next.status)
	require.Equal(t, "system", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "task-1")
}

func TestCtrlBBackgroundErrorKeepsComposer(t *testing.T) {
	ta := newPromptTextarea("review this diff")
	m := newModel(context.Background(), ta, nil, nil)
	m.background = func(context.Context, string) (string, error) {
		return "", errors.New("background failed")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	next := updated.(model)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.False(t, next.busy)
	require.False(t, next.backgrounding)
	require.Equal(t, "review this diff", next.textarea.Value())
	require.Equal(t, "background error", next.status)
	require.Equal(t, "error", next.transcript[len(next.transcript)-1].Role)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "background failed")
}

func TestCtrlBStartsBackgroundWhileTurnIsBusy(t *testing.T) {
	ta := newPromptTextarea("foreground prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.submit = func(context.Context, string) (string, error) {
		return "foreground done", nil
	}
	m.background = func(_ context.Context, prompt string) (string, error) {
		require.Equal(t, "background prompt", prompt)
		return "Background task started: task-2", nil
	}

	updated, foregroundCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, foregroundCmd)

	m.textarea.SetValue("background prompt")
	updated, backgroundCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = updated.(model)
	require.True(t, m.busy)
	require.True(t, m.backgrounding)
	require.Equal(t, "backgrounding", m.status)
	require.NotNil(t, backgroundCmd)

	updated, _ = m.Update(backgroundCmd())
	m = updated.(model)
	require.True(t, m.busy)
	require.False(t, m.backgrounding)
	require.Equal(t, "running", m.status)
	require.Empty(t, m.textarea.Value())

	updated, _ = m.Update(foregroundCmd())
	m = updated.(model)
	require.False(t, m.busy)
	require.Equal(t, "ready", m.status)
}

func TestEscapeCancelsBackgroundStart(t *testing.T) {
	ta := newPromptTextarea("background prompt")
	m := newModel(context.Background(), ta, nil, nil)
	var seen context.Context
	m.background = func(ctx context.Context, prompt string) (string, error) {
		seen = ctx
		<-ctx.Done()
		return "", ctx.Err()
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = updated.(model)
	require.True(t, m.backgrounding)
	require.NotNil(t, m.backgroundCancel)
	require.NotNil(t, cmd)

	updated, quit := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, quit)
	require.True(t, m.backgrounding)
	require.Equal(t, "canceling background", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.False(t, m.backgrounding)
	require.Nil(t, m.backgroundCancel)
	require.Equal(t, "background prompt", m.textarea.Value())
	require.Equal(t, "background canceled", m.status)
	require.ErrorIs(t, seen.Err(), context.Canceled)
	require.Contains(t, m.View(), "Background prompt canceled.")
}

func TestPastedMultilineInputDoesNotSubmitUntilEnter(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first\nsecond"), Paste: true})
	next := updated.(model)

	require.Nil(t, cmd)
	require.False(t, next.result.Submitted)
	require.Equal(t, "first\nsecond", next.textarea.Value())
	require.Equal(t, "pasted 2 lines", next.status)

	updated, _ = next.Update(teaKey("enter"))
	next = updated.(model)
	require.True(t, next.result.Submitted)
	require.Equal(t, "first\nsecond", next.result.Prompt)
}

func TestPastedShortcutTextDoesNotTriggerTUIActions(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, []string{"/status"}, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?"), Paste: true})
	next := updated.(model)

	require.False(t, next.helpOpen)
	require.Equal(t, "?", next.textarea.Value())
	require.Equal(t, "pasted 1 line", next.status)
}

func TestPastedPermissionAnswerDoesNotApprove(t *testing.T) {
	ta := newPromptTextarea("")
	answers := []string{}
	m := newModel(context.Background(), ta, nil, nil)
	m.permissionAnswer = func(answer string) {
		answers = append(answers, answer)
	}
	m.awaitingPermission = true
	m.status = "permission"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})
	next := updated.(model)

	require.True(t, next.awaitingPermission)
	require.Empty(t, answers)
	require.Equal(t, "y", next.textarea.Value())
	require.Equal(t, "permission", next.status)
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
