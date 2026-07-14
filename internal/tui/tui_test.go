package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func testQueuedPrompts(texts ...string) []queuedPrompt {
	queued := make([]queuedPrompt, 0, len(texts))
	for _, text := range texts {
		queued = append(queued, queuedPrompt{Text: text})
	}
	return queued
}

func queuedPromptTexts(queued []queuedPrompt) []string {
	texts := make([]string, 0, len(queued))
	for _, prompt := range queued {
		texts = append(texts, prompt.Text)
	}
	return texts
}

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
	ta.SetValue("/con")
	m := model{textarea: ta}

	m = m.completeSlashCommand()
	require.NotEmpty(t, m.matches)
	require.Contains(t, m.matches, "/config")
	require.Contains(t, m.matches, "/context")
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

func TestEnterSubmitsExactSlashCommandBeforeLongerCompletion(t *testing.T) {
	ta := newPromptTextarea("/status")
	m := newModel(context.Background(), ta, []string{"/status", "/statusline --json"}, nil)
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		require.Equal(t, "/status", line)
		return SlashResult{Output: "Tools ok", Handled: true}, nil
	}
	m.refreshCompletionMenu()
	require.Empty(t, m.matches)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "running slash", m.status)
	require.Equal(t, "", m.textarea.Value())

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Contains(t, m.View(), "Tools ok")
}

func TestEnterSubmitsLocalExitBeforeLongerCompletion(t *testing.T) {
	ta := newPromptTextarea("/exit")
	m := newModel(context.Background(), ta, []string{"/exit-plan"}, nil)
	m.refreshCompletionMenu()
	require.Empty(t, m.matches)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Empty(t, m.matches)
}

func TestExactSlashCommandKeepsOnlyItsArgumentCompletions(t *testing.T) {
	ta := newPromptTextarea("/model")
	m := newModel(context.Background(), ta, []string{"/model", "/model glm52", "/models"}, nil)
	m.refreshCompletionMenu()

	require.Equal(t, []string{"/model glm52"}, m.matches)
}

func TestCompletionListRendersSelectedSuggestion(t *testing.T) {
	view := renderCompletions([]string{"/model claude-test", "/memory list"}, 1)

	require.Contains(t, view, "suggestions")
	require.Contains(t, view, "  /model claude-test")
	require.Contains(t, view, "> /memory list")
	require.Contains(t, view, "Show or switch the current model.")
	require.Contains(t, view, "List, search, show")
	require.Contains(t, view, "Enter accept")
}

func TestCompletionListKeepsAllMatchesAndScrollsVisibleWindow(t *testing.T) {
	candidates := []string{
		"/command-01", "/command-02", "/command-03", "/command-04",
		"/command-05", "/command-06", "/command-07", "/command-08",
		"/command-09", "/command-10", "/command-11", "/command-12",
	}
	preview := PreviewWithCandidates("/", candidates, 96, 24, false, false)
	require.Equal(t, candidates, preview.Matches)
	require.Contains(t, preview.View, "suggestions · 1/12")
	require.Contains(t, preview.View, "/command-08")
	require.NotContains(t, preview.View, "/command-09")

	scrolled := renderCompletions(candidates, 10)
	require.Contains(t, scrolled, "suggestions · 11/12")
	require.Contains(t, scrolled, "> /command-11")
	require.NotContains(t, scrolled, "/command-01")
}

func TestCompletionMenuSuggestsMisspelledCommand(t *testing.T) {
	preview := PreviewWithCandidates("/statuz", []string{"/status", "/stats"}, 96, 24, false, false)

	require.Contains(t, preview.Matches, "/status")
	require.Contains(t, preview.View, "Show local workspace")
}

func TestCompletionDisplayLineFallsBackForCustomCandidate(t *testing.T) {
	require.Equal(t, "/custom thing", completionDisplayLine("/custom thing"))
}

func TestSlashCommandArgumentHintRendersAfterCommandSpace(t *testing.T) {
	preview := PreviewWithCandidates("/model ", []string{"/model claude-test"}, 96, 24, false, false)

	require.Contains(t, preview.CommandHint, "arguments: [name]")
	require.Contains(t, preview.CommandHint, "Show or switch the current model.")
	require.Contains(t, preview.View, "command args")
	require.Contains(t, preview.View, "arguments: [name]")
}

func TestSlashCommandArgumentHintOmitsNoArgumentCommands(t *testing.T) {
	preview := PreviewWithCandidates("/status ", []string{"/status"}, 96, 24, false, false)

	require.Empty(t, preview.CommandHint)
	require.NotContains(t, preview.View, "usage: /status")
}

func TestMidInputSlashCommandGhostCompletesWithTab(t *testing.T) {
	preview := PreviewWithCandidates("please /sta", []string{"/status"}, 96, 24, false, false)

	require.Equal(t, "/status", preview.InlineHint)
	require.Contains(t, preview.View, "ghost: /status")

	completed := PreviewWithCandidates("please /sta", []string{"/status"}, 96, 24, true, false)
	require.Equal(t, "please /status ", completed.Value)
	require.Empty(t, completed.InlineHint)
}

func TestQueuedPromptsRenderBelowComposer(t *testing.T) {
	view := renderQueuedPrompts(testQueuedPrompts("first queued prompt", "second queued prompt"))

	require.Contains(t, view, "queued prompts: 2")
	require.Contains(t, view, "1. first queued prompt")
	require.Contains(t, view, "2. second queued prompt")
	require.Empty(t, renderQueuedPrompts(nil))
}

func TestQueuedPromptsRenderBashMode(t *testing.T) {
	view := renderQueuedPrompts(testQueuedPrompts("!printf codog", "regular prompt"))

	require.Contains(t, view, "queued prompts: 2")
	require.Contains(t, view, "1. bash: printf codog")
	require.Contains(t, view, "2. regular prompt")
}

func TestQueuedPromptPreviewTruncatesOlderItems(t *testing.T) {
	view := renderQueuedPrompts(testQueuedPrompts("one", "two", "three", "four"))

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

func TestPreviewWithAttachmentRemovalDropsLastAttachment(t *testing.T) {
	preview := PreviewWithAttachmentRemoval("describe", []string{"notes.txt", "pixel.png"}, 96, 24)

	require.Equal(t, []string{"notes.txt"}, preview.Attachments)
	require.Contains(t, preview.View, "attachment removed")
	require.Contains(t, preview.View, "Removed attachment: pixel.png")
	require.Contains(t, preview.View, "notes.txt")
	require.NotContains(t, renderPendingAttachments(preview.Attachments), "pixel.png")
}

func TestPreviewWithPasteInsertsClipboardText(t *testing.T) {
	preview := PreviewWithPaste("prefix ", "clipboard\ntext", 96, 24)

	require.Equal(t, "prefix clipboard\ntext", preview.Value)
	require.Contains(t, preview.View, "pasted 2 lines")
}

func TestPreviewWithPasteAttachmentStagesAttachment(t *testing.T) {
	preview := PreviewWithPasteAttachment("", "clipboard.png", 96, 24)

	require.Equal(t, []string{"clipboard.png"}, preview.Attachments)
	require.Contains(t, preview.View, "attachments: 1")
	require.Contains(t, preview.View, "clipboard image attached")
}

func TestCtrlVPastesClipboardIntoComposer(t *testing.T) {
	ta := newPromptTextarea("prefix ")
	m := newModel(context.Background(), ta, nil, nil)
	m.paste = func(context.Context) (PasteContent, error) {
		return PasteContent{Text: "clipboard\ntext"}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "pasting", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, "prefix clipboard\ntext", m.textarea.Value())
	require.Equal(t, "pasted 2 lines", m.status)
}

func TestCtrlVPasteImageStagesAttachment(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.paste = func(context.Context) (PasteContent, error) {
		return PasteContent{AttachmentPath: "clipboard.png", MediaType: "image/png"}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m = updated.(model)
	require.NotNil(t, cmd)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, []string{"clipboard.png"}, m.attachments)
	require.Equal(t, "clipboard image attached", m.status)
	require.Contains(t, m.View(), "attachments: 1")
}

func TestSlashPasteInsertsClipboardIntoComposer(t *testing.T) {
	ta := newPromptTextarea("/paste")
	m := newModel(context.Background(), ta, nil, nil)
	m.paste = func(context.Context) (PasteContent, error) {
		return PasteContent{Text: "clipboard text"}, nil
	}
	m.slash = func(context.Context, string) (SlashResult, error) {
		t.Fatal("bare /paste should be handled by the TUI")
		return SlashResult{}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "pasting", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, "clipboard text", m.textarea.Value())
	require.Equal(t, "pasted 1 line", m.status)
}

func TestSlashPasteWithArgsFallsThroughToSlash(t *testing.T) {
	ta := newPromptTextarea("/paste --json")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.paste = func(context.Context) (PasteContent, error) {
		t.Fatal("/paste with args should stay a slash command")
		return PasteContent{}, nil
	}
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		called = true
		require.Equal(t, "/paste --json", line)
		return SlashResult{Output: "{}", Handled: true}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.Contains(t, m.View(), "{}")
}

func TestPasteErrorRendersTranscript(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(pasteDoneMsg{Err: errors.New("clipboard unavailable")})
	m = updated.(model)

	require.Equal(t, "paste error", m.status)
	require.Contains(t, m.View(), "clipboard unavailable")
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
	m.refreshCompletionMenu()
	require.NotEmpty(t, m.matches)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.matches)
	require.Equal(t, "slash", m.status)
	require.False(t, m.result.Submitted)
}

func TestBashModeSuppressesPromptCompletions(t *testing.T) {
	preview := PreviewWithCandidates("!echo @internal/tui", []string{"/status"}, 96, 24, false, false)

	require.Equal(t, "bash", preview.Mode)
	require.Empty(t, preview.Matches)
	require.Contains(t, preview.View, "! for bash mode")
	require.Contains(t, preview.View, "Enter run local command")
}

func TestBashModeRoutesThroughRunSlashCommand(t *testing.T) {
	preview := PreviewWithBashMode("!printf codog", 96, 24)

	require.Equal(t, "/run printf codog", preview.Prompt)
	require.Empty(t, preview.Value)
	require.Contains(t, preview.View, "bash ok: /run printf codog")
}

func TestBashModeHistoryGhostCompletesWithTab(t *testing.T) {
	preview := PreviewWithBashHistory("!go te", []string{"!go test ./internal/tui"}, nil, 96, 24, false)

	require.Equal(t, "bash", preview.Mode)
	require.Empty(t, preview.Matches)
	require.Equal(t, "!go test ./internal/tui", preview.InlineHint)
	require.Contains(t, preview.View, "ghost: !go test ./internal/tui")

	completed := PreviewWithBashHistory("!go te", []string{"!go test ./internal/tui"}, nil, 96, 24, true)
	require.Equal(t, "!go test ./internal/tui ", completed.Value)
}

func TestBashModePathCompletionBeatsHistoryGhost(t *testing.T) {
	preview := PreviewWithBashHistory(
		"!cat internal/t",
		[]string{"!cat internal/tmp.txt"},
		[]string{"internal/tui/tui.go", "internal/agent/agent.go"},
		96,
		24,
		false,
	)

	require.Equal(t, []string{"internal/tui/tui.go"}, preview.Matches)
	require.Empty(t, preview.InlineHint)

	completed := PreviewWithBashHistory(
		"!cat internal/t",
		[]string{"!cat internal/tmp.txt"},
		[]string{"internal/tui/tui.go", "internal/agent/agent.go"},
		96,
		24,
		true,
	)
	require.Equal(t, "!cat internal/tui/tui.go ", completed.Value)
}

func TestBashModeHistoryGhostIgnoresNonBashAndExactMatches(t *testing.T) {
	preview := PreviewWithBashHistory("!go te", []string{"go test ./...", "!go te"}, nil, 96, 24, false)

	require.Empty(t, preview.InlineHint)
	require.Empty(t, preview.Matches)
}

func TestBareBashModeStaysInComposer(t *testing.T) {
	ta := newPromptTextarea("!")
	m := newModel(context.Background(), ta, nil, nil)
	m.slash = func(context.Context, string) (SlashResult, error) {
		t.Fatal("bare bash mode should not run")
		return SlashResult{}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "!", m.textarea.Value())
	require.Equal(t, "bash", m.status)
	require.Contains(t, m.View(), "! for bash mode")
}

func TestEscapeRequiresDoublePressToClearComposer(t *testing.T) {
	ta := newPromptTextarea("draft prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"draft.txt"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.NotNil(t, cmd)
	require.Equal(t, "draft prompt", m.textarea.Value())
	require.Equal(t, []string{"draft.txt"}, m.attachments)
	require.Equal(t, "Esc again to clear", m.status)
	require.Contains(t, m.View(), "Esc again to clear")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Empty(t, m.textarea.Value())
	require.Empty(t, m.attachments)
	require.False(t, m.exitPending)
	require.Equal(t, "input cleared", m.status)
	require.Contains(t, m.history, "draft prompt")
}

func TestEscapeExitPendingResetsAfterTyping(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.True(t, m.exitPending)

	updated, _ = m.Update(teaKey("x"))
	m = updated.(model)
	require.False(t, m.exitPending)
	require.Equal(t, "draftx", m.textarea.Value())
}

func TestEscapePendingExpiresWithoutClearingComposer(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("draft"), nil, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	generation := m.exitPendingGeneration
	updated, _ = m.Update(exitPendingExpiredMsg{Key: "esc", Generation: generation})
	m = updated.(model)

	require.False(t, m.exitPending)
	require.Equal(t, "draft", m.textarea.Value())
	require.Equal(t, "compose", m.status)
}

func TestStaleExitPendingTimerDoesNotClearNewConfirmation(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("draft"), nil, nil)

	_ = m.armExit("esc", "first confirmation")
	staleGeneration := m.exitPendingGeneration
	_ = m.armExit("esc", "new confirmation")

	updated, _ := m.Update(exitPendingExpiredMsg{Key: "esc", Generation: staleGeneration})
	m = updated.(model)
	require.True(t, m.exitPending)
	require.Equal(t, "new confirmation", m.status)

	updated, _ = m.Update(exitPendingExpiredMsg{Key: "esc", Generation: m.exitPendingGeneration})
	m = updated.(model)
	require.False(t, m.exitPending)
}

func TestDoubleEscapeOnEmptyComposerOpensLatestUserMessageActions(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.transcript = append(m.transcript,
		transcriptEntry{Role: "user", Text: "first prompt"},
		transcriptEntry{Role: "assistant", Text: "answer"},
		transcriptEntry{Role: "user", Text: "latest prompt"},
	)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.True(t, m.exitPending)
	require.False(t, m.messageActions)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, cmd)
	require.False(t, m.exitPending)
	require.True(t, m.messageActions)
	require.Equal(t, "latest prompt", m.transcript[m.messageActionTarget].Text)
}

func TestControlCClearsComposerThenExitsOnSecondPress(t *testing.T) {
	ta := newPromptTextarea("draft prompt")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Empty(t, m.textarea.Value())
	require.True(t, m.exitPending)
	require.Equal(t, "ctrl+c", m.exitKey)
	require.Contains(t, m.View(), "Ctrl+C again to exit")

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	_, ok := cmd().(tea.QuitMsg)
	require.True(t, ok)
}

func TestSlashMenuDoesNotInterceptExactLocalHelp(t *testing.T) {
	ta := newPromptTextarea("/help")
	m := newModel(context.Background(), ta, []string{"/help"}, nil)
	m.refreshCompletionMenu()
	require.Empty(t, m.matches)

	updated, _ := m.Update(teaKey("enter"))
	m = updated.(model)

	require.True(t, m.helpOpen)
	require.Empty(t, m.textarea.Value())
	require.Contains(t, m.View(), "Core commands")
}

func TestDefaultTUIWelcomeMatchesInteractiveAgentWorkflow(t *testing.T) {
	preview := PreviewWithCandidates("", nil, 100, 24, false, false)

	require.Contains(t, preview.View, "codog")
	require.Contains(t, preview.View, "Interactive coding agent ready.")
	require.Contains(t, preview.View, "Mention @files")
	require.Contains(t, preview.View, "run !shell commands")
	require.Contains(t, preview.View, "Ask codog...")
}

func TestInlinePreviewUsesCompactComposerAndFooter(t *testing.T) {
	preview := PreviewInlineWithCandidates("", nil, 100, 24, false, false)

	require.Contains(t, preview.View, "codog")
	require.Contains(t, preview.View, "❯")
	require.NotContains(t, preview.View, " composer ")
	require.NotContains(t, preview.View, "Enter send")
	require.Less(t, strings.Count(preview.View, "\n"), 10)
}

func TestInlineViewFitsNarrowTerminalWidth(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.inline = true
	m.currentModel = "openai/model-with-a-long-name"
	m.runtimeBadges = []string{"thinking: extended"}
	m.layout(30, 12)

	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 30, line)
	}
}

func TestInlineShellFlushesCompletedTranscriptToScrollback(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, []transcriptEntry{{Role: "system", Text: "ready marker"}})
	m.inline = true
	m.prepareInlineTranscript()

	require.Contains(t, m.initialPrint, "ready marker")
	require.NotContains(t, m.View(), "ready marker")

	m.transcript = append(m.transcript,
		transcriptEntry{Role: "user", Text: "prompt marker"},
		transcriptEntry{Role: "assistant", Text: "answer marker"},
	)
	m.refreshViewport()
	require.Contains(t, m.View(), "prompt marker")
	require.Contains(t, m.View(), "answer marker")

	cmd := m.flushInlineTranscript()
	require.NotNil(t, cmd)
	require.Equal(t, len(m.transcript), m.printedEntries)
	require.NotContains(t, m.View(), "prompt marker")
	require.NotContains(t, m.View(), "answer marker")
}

func TestInlineTranscriptModeCanInspectPrintedHistory(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, []transcriptEntry{{Role: "assistant", Text: "printed answer"}})
	m.inline = true
	m.prepareInlineTranscript()
	require.NotContains(t, m.View(), "printed answer")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(model)

	require.True(t, m.transcriptMode)
	require.Contains(t, m.View(), "printed answer")
}

func TestHelpPanelDescribesCoreInteractiveInputs(t *testing.T) {
	help := helpPanel([]string{"/status", "/diff"}, 120)

	require.Contains(t, help, "interactive coding agent")
	require.Contains(t, help, "@path")
	require.Contains(t, help, "!command")
	require.Contains(t, help, "run a local shell command directly")
	require.NotContains(t, help, "through the permission flow")
	require.Contains(t, help, "/attach PATH")
	require.Contains(t, help, "/paste")
	require.Contains(t, help, "Enter sends the composer")
	require.Contains(t, help, "Shift+Enter")
	require.Contains(t, help, "/status")
	require.Contains(t, help, "/diff")
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

func TestPromptFooterConstrainsLongStatusAtTerminalWidth(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.status = "error · 12 queued restored"
	m.modeLabel = "accept edits"

	footer := fitFooterText(m.promptFooterText(80), 78)
	require.Contains(t, footer, "12 queued restored")
	require.True(t, strings.HasSuffix(strings.Split(footer, "\n")[0], "..."))
	rendered := stylesForTheme("auto").status().Width(80).Render(footer)
	for _, line := range strings.Split(rendered, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 80, line)
	}
	m.layout(80, 24)
	for _, line := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 80, line)
	}
	cjk := truncateFooterLine(strings.Repeat("界", 20), 20)
	require.True(t, strings.HasSuffix(cjk, "..."))
	require.LessOrEqual(t, lipgloss.Width(cjk), 20)
}

func TestPromptFooterShowsContextualIdleHints(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "accept edits"
	m.currentModel = "glm52"
	m.runtimeBadges = []string{"fast: off", "thinking: medium"}
	m.attachments = []string{"notes.txt"}
	m.modelOptions = []string{"glm52"}
	m.stashedPrompt = &composerStash{Text: "draft"}

	footer := m.promptFooterText(240)

	require.Contains(t, footer, "Enter send")
	require.Contains(t, footer, "? for shortcuts")
	require.Contains(t, footer, "/ commands")
	require.Contains(t, footer, "@ files")
	require.Contains(t, footer, "Ctrl+T tasks")
	require.Contains(t, footer, "Ctrl+S restore stash")
	require.Contains(t, footer, "1 attached")
	require.Contains(t, footer, "⏵⏵ accept edits on")
	require.Contains(t, footer, "model: glm52")
	require.Contains(t, footer, "fast: off")
	require.Contains(t, footer, "thinking: medium")
}

func TestHeaderShowsRuntimeBadges(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.currentModel = "glm52"
	m.runtimeBadges = []string{"fast: off", "thinking: medium"}
	m.layout(120, 24)

	view := m.View()

	require.Contains(t, view, "Codog TUI")
	require.Contains(t, view, "model: glm52")
	require.Contains(t, view, "fast: off")
	require.Contains(t, view, "thinking: medium")
}

func TestVimModeEscapeEntersNormalWithoutClearingInput(t *testing.T) {
	preview := PreviewWithVimMode("abc", []string{"esc"}, 96, 24)

	require.Equal(t, "abc", preview.Value)
	require.Equal(t, "vim normal", preview.Mode)
	require.Contains(t, preview.View, "vim: normal")
	require.Contains(t, preview.View, "i/a insert")
}

func TestVimModeNormalEditingAndInsertReturn(t *testing.T) {
	preview := PreviewWithVimMode("abc", []string{"esc", "0", "x", "A", "!"}, 96, 24)

	require.Equal(t, "bc!", preview.Value)
	require.Equal(t, "compose", preview.Mode)
	require.Contains(t, preview.View, "vim: insert")
}

func TestVimModeWordMoveAndDeleteToEnd(t *testing.T) {
	word := PreviewWithVimMode("one two three", []string{"esc", "0", "w", "x", "A", "!"}, 96, 24)
	require.Equal(t, "one wo three!", word.Value)

	deleted := PreviewWithVimMode("prefix suffix", []string{"esc", "0", "w", "D", "A", "!"}, 96, 24)
	require.Equal(t, "prefix !", deleted.Value)
}

func TestVimModeOperatorsClearComposer(t *testing.T) {
	deleted := PreviewWithVimMode("abc", []string{"esc", "d", "d"}, 96, 24)
	require.Equal(t, "", deleted.Value)
	require.Equal(t, "vim normal", deleted.Mode)

	changed := PreviewWithVimMode("abc", []string{"esc", "c", "c", "n"}, 96, 24)
	require.Equal(t, "n", changed.Value)
	require.Equal(t, "compose", changed.Mode)
}

func TestPromptFooterShowsRunningQueueHints(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.status = "running"
	m.queuedPrompts = testQueuedPrompts("next")

	footer := m.promptFooterText(90)

	require.Contains(t, footer, "Esc/Ctrl-C cancel current turn")
	require.Contains(t, footer, "Esc interrupt")
	require.Contains(t, footer, "1 queued")
	require.Contains(t, footer, "Up edit queue")
	require.NotContains(t, footer, "Enter send")
}

func TestShiftTabCyclesTUILocalMode(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "default"
	m.cycleMode = func() string { return "accept edits" }
	transcriptCount := len(m.transcript)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(model)

	require.Equal(t, "accept edits", m.modeLabel)
	require.Len(t, m.transcript, transcriptCount)
	require.Equal(t, "ready", m.status)
	require.Contains(t, m.View(), "⏵⏵ accept edits on")
}

func TestAltMCyclesTUILocalModeFallback(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "default"
	m.cycleMode = func() string { return "plan" }
	transcriptCount := len(m.transcript)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(model)

	require.Equal(t, "plan", m.modeLabel)
	require.Empty(t, m.textarea.Value())
	require.Len(t, m.transcript, transcriptCount)
	require.Contains(t, m.View(), "⏸ plan mode on")
	require.Contains(t, helpPanel(nil, 100), "Alt/Meta+M")
}

func TestTurnCompletionRefreshesExternalPermissionMode(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "accept edits"
	m.readModeLabel = func() string { return "plan" }
	m.busy = true

	updated, _ := m.Update(turnDoneMsg{Role: "system"})
	m = updated.(model)

	require.Equal(t, "plan", m.modeLabel)
	require.Contains(t, m.View(), "⏸ plan mode on")
}

func TestInlineFooterPrioritizesActivePermissionMode(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "accept edits"
	m.currentModel = "glm52"
	m.runtimeBadges = []string{"model: glm52"}
	m.cycleMode = func() string { return "plan" }

	footer := m.inlineFooterText(80)

	require.Contains(t, footer, "⏵⏵ accept edits on")
	require.Contains(t, footer, "Shift+Tab cycle")
	require.Contains(t, footer, "? for shortcuts")

	m.status = "slash"
	footer = m.inlineFooterText(80)
	require.Equal(t, 1, strings.Count(footer, "accept edits on"))
}

func TestCtrlOTogglesExpandedTranscript(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, []transcriptEntry{
		{Role: "user", Text: "first line\nsecond line"},
		{Role: "assistant", Text: "result"},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 128, Height: 24})
	m = updated.(model)

	require.False(t, m.transcriptMode)
	require.NotContains(t, m.View(), "001/002 user")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(model)

	require.True(t, m.transcriptMode)
	require.Equal(t, "transcript", m.status)
	require.Contains(t, m.View(), "001/002 user")
	require.Contains(t, m.View(), "2 lines")
	require.Contains(t, m.View(), "Ctrl+O")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(model)

	require.False(t, m.transcriptMode)
	require.Equal(t, "ready", m.status)
	require.NotContains(t, m.View(), "001/002 user")
}

func TestPreviewWithTranscriptRendersExpandedEntries(t *testing.T) {
	preview := PreviewWithTranscript([]Entry{
		{Role: "tool", Text: "stdout\nstderr"},
	}, 96, 24)

	require.True(t, preview.Transcript)
	require.Contains(t, preview.View, "001/001 tool")
	require.Contains(t, preview.View, "2 lines")
	require.Contains(t, preview.View, "stdout")
	require.Contains(t, preview.View, "stderr")
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
	require.Equal(t, "Ask codog...", ta.Placeholder)
	require.False(t, ta.ShowLineNumbers)
}

func TestInitialPromptStartsFirstTurnWithAttachments(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.initialPrompt = "inspect this repository"
	m.attachments = []string{"screenshot.png"}
	m.submitAttachments = func(_ context.Context, prompt string, attachments []string) (string, error) {
		require.Equal(t, "inspect this repository", prompt)
		require.Equal(t, []string{"screenshot.png"}, attachments)
		return "done", nil
	}

	updated, cmd := m.Update(initialPromptMsg{Value: m.initialPrompt})
	next := updated.(model)
	require.True(t, next.busy)
	require.Empty(t, next.initialPrompt)
	require.Empty(t, next.attachments)
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "inspect this repository")
	require.Contains(t, next.transcript[len(next.transcript)-1].Text, "screenshot.png")
	require.NotNil(t, cmd)

	result := cmd()
	done, ok := result.(turnDoneMsg)
	require.True(t, ok)
	require.Equal(t, "done", done.Output)
	require.NoError(t, done.Err)
}

func TestPreviewWithCandidatesRendersMultipleMatches(t *testing.T) {
	preview := PreviewWithCandidates("/m", []string{"/model claude-test", "/memory list"}, 90, 20, true, false)

	require.Contains(t, preview.View, "/memory list")
	require.Contains(t, preview.View, "/model claude-test")
	require.Contains(t, preview.View, "suggestions")
	require.ElementsMatch(t, []string{"/model claude-test", "/memory list"}, preview.Matches)
	require.False(t, preview.Submitted)
}

func TestFileReferenceCompletionFiltersAndCompletesAtMention(t *testing.T) {
	preview := PreviewWithFileCandidates("review @internal/t", []string{
		"internal/tui/tui.go",
		"internal/agent/agent.go",
		"README.md",
	}, 96, 24, false)

	require.Equal(t, []string{"@internal/tui/tui.go"}, preview.Matches)
	require.Contains(t, preview.View, "@internal/tui/tui.go")
	require.Contains(t, preview.View, "file reference")

	completed := PreviewWithFileCandidates("review @internal/t", []string{
		"internal/tui/tui.go",
		"internal/agent/agent.go",
	}, 96, 24, true)
	require.Equal(t, "review @internal/tui/tui.go ", completed.Value)
}

func TestFileReferenceCompletionIgnoresEmailLikeAtSigns(t *testing.T) {
	preview := PreviewWithFileCandidates("mail dev@example", []string{"example.go"}, 96, 24, false)

	require.Empty(t, preview.Matches)
}

func TestBashModeFilePathCompletionDoesNotUseAtPrefix(t *testing.T) {
	preview := PreviewWithFileCandidates("!cat internal/t", []string{
		"internal/tui/tui.go",
		"internal/agent/agent.go",
	}, 96, 24, false)

	require.Equal(t, "bash", preview.Mode)
	require.Equal(t, []string{"internal/tui/tui.go"}, preview.Matches)
	require.Contains(t, preview.View, "internal/tui/tui.go  -  file path")

	completed := PreviewWithFileCandidates("!cat internal/t", []string{
		"internal/tui/tui.go",
		"internal/agent/agent.go",
	}, 96, 24, true)
	require.Equal(t, "!cat internal/tui/tui.go ", completed.Value)
	require.Empty(t, completed.Matches)
}

func TestBashModeAtSymbolDoesNotTriggerFileReferenceCompletion(t *testing.T) {
	preview := PreviewWithFileCandidates("!echo @internal/t", []string{
		"internal/tui/tui.go",
	}, 96, 24, false)

	require.Equal(t, "bash", preview.Mode)
	require.Empty(t, preview.Matches)
	require.Contains(t, preview.View, "! for bash mode")
}

func TestQuickOpenFiltersAndInsertsFileReference(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("internal/tui", 0o755))
	require.NoError(t, os.WriteFile("internal/tui/tui.go", []byte("package tui\n\nfunc previewTarget() {}\n"), 0o644))

	ta := newPromptTextarea("review")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"internal/agent/agent.go", "internal/tui/tui.go", "README.md"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)

	require.True(t, m.quickOpen)
	require.Equal(t, "review", m.quickOpenDraft)
	require.Contains(t, m.View(), "start typing to search files")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tui")})
	m = updated.(model)

	require.Equal(t, []string{"internal/tui/tui.go"}, m.quickOpenMatches)
	require.Contains(t, m.View(), "quick open: tui")
	require.Contains(t, m.View(), "preview")
	require.Contains(t, m.View(), "package tui")
	require.Contains(t, m.View(), "func previewTarget")
	require.Contains(t, m.View(), "Enter/Tab insert @file")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.quickOpen)
	require.Equal(t, "review @internal/tui/tui.go ", m.textarea.Value())
	require.Equal(t, "file referenced", m.status)
}

func TestQuickOpenShiftTabInsertsBarePath(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"internal/tui/tui.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tui")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(model)

	require.False(t, m.quickOpen)
	require.Equal(t, "internal/tui/tui.go ", m.textarea.Value())
	require.Equal(t, "path inserted", m.status)
}

func TestQuickOpenControlNavigation(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"a.go", "b.go", "c.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".go")})
	m = updated.(model)

	require.Equal(t, []string{"a.go", "b.go", "c.go"}, m.quickOpenMatches)
	require.Equal(t, 0, m.quickOpenSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	require.Equal(t, 1, m.quickOpenSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	require.Equal(t, 0, m.quickOpenSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	require.Equal(t, 2, m.quickOpenSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	m = updated.(model)
	require.Equal(t, 0, m.quickOpenSelected)
}

func TestQuickOpenEscapeRestoresDraft(t *testing.T) {
	ta := newPromptTextarea("unfinished")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"internal/tui/tui.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	m.textarea.SetValue("tui")
	m.updateQuickOpen()

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.False(t, m.quickOpen)
	require.Equal(t, "unfinished", m.textarea.Value())
}

func TestQuickOpenTreatsQuestionMarkAsQuery(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"docs/question.md"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = updated.(model)

	require.True(t, m.quickOpen)
	require.False(t, m.helpOpen)
	require.Equal(t, "?", m.textarea.Value())
}

func TestQuickOpenPreviewHandlesBinaryFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("image.bin", []byte{0x89, 0x00, 0x01}, 0o644))

	preview := PreviewWithQuickOpen("", []string{"image.bin"}, "image", 96, 24, false)

	require.True(t, preview.QuickOpen)
	require.Contains(t, preview.View, "image.bin")
	require.Contains(t, preview.View, "(binary file)")
}

func TestPreviewWithQuickOpen(t *testing.T) {
	preview := PreviewWithQuickOpen("inspect", []string{"internal/tui/tui.go", "internal/agent/agent.go"}, "tui", 96, 24, false)

	require.True(t, preview.QuickOpen)
	require.Equal(t, []string{"internal/tui/tui.go"}, preview.Matches)
	require.Contains(t, preview.View, "quick open: tui")

	accepted := PreviewWithQuickOpen("inspect", []string{"internal/tui/tui.go"}, "tui", 96, 24, true)
	require.False(t, accepted.QuickOpen)
	require.Equal(t, "inspect @internal/tui/tui.go ", accepted.Value)
}

func TestGlobalSearchFindsContentAndInsertsLineReference(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("internal/tui", 0o755))
	require.NoError(t, os.WriteFile("internal/tui/tui.go", []byte("package tui\n\nfunc SearchTarget() {}\n"), 0o644))

	ta := newPromptTextarea("review")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"internal/agent/agent.go", "internal/tui/tui.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(model)

	require.True(t, m.globalSearch)
	require.Equal(t, "review", m.globalSearchDraft)
	require.Contains(t, m.View(), "type to search workspace")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("SearchTarget")})
	m = updated.(model)

	require.Len(t, m.globalSearchMatches, 1)
	require.Equal(t, "internal/tui/tui.go", m.globalSearchMatches[0].File)
	require.Equal(t, 3, m.globalSearchMatches[0].Line)
	require.Contains(t, m.View(), "global search: SearchTarget")
	require.Contains(t, m.View(), "internal/tui/tui.go:3")
	require.Contains(t, m.View(), "func SearchTarget")
	require.Contains(t, m.View(), "Enter/Tab insert @file#Lline")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.globalSearch)
	require.Equal(t, "review @internal/tui/tui.go#L3 ", m.textarea.Value())
	require.Equal(t, "line referenced", m.status)
}

func TestGlobalSearchShiftTabInsertsBareLocation(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("main.go", []byte("package main\n\nconst Needle = true\n"), 0o644))

	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"main.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Needle")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(model)

	require.False(t, m.globalSearch)
	require.Equal(t, "main.go:3 ", m.textarea.Value())
	require.Equal(t, "location inserted", m.status)
}

func TestGlobalSearchControlNavigation(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("a.go", []byte("package main\nconst NeedleA = true\n"), 0o644))
	require.NoError(t, os.WriteFile("b.go", []byte("package main\nconst NeedleB = true\n"), 0o644))
	require.NoError(t, os.WriteFile("c.go", []byte("package main\nconst NeedleC = true\n"), 0o644))

	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"a.go", "b.go", "c.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Needle")})
	m = updated.(model)

	require.Len(t, m.globalSearchMatches, 3)
	require.Equal(t, 0, m.globalSearchSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	require.Equal(t, 1, m.globalSearchSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = updated.(model)
	require.Equal(t, 0, m.globalSearchSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	require.Equal(t, 2, m.globalSearchSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	m = updated.(model)
	require.Equal(t, 0, m.globalSearchSelected)
}

func TestGlobalSearchEscapeRestoresDraft(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("main.go", []byte("package main\n"), 0o644))

	ta := newPromptTextarea("unfinished")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"main.go"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("package")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.False(t, m.globalSearch)
	require.Equal(t, "unfinished", m.textarea.Value())
}

func TestGlobalSearchSkipsBinaryFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("image.bin", []byte{0x89, 0x00, 0x01}, 0o644))

	preview := PreviewWithGlobalSearch("", []string{"image.bin"}, "image", 96, 24, false)

	require.True(t, preview.GlobalSearch)
	require.Empty(t, preview.Matches)
	require.Contains(t, preview.View, "no matches")
}

func TestPreviewWithGlobalSearch(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("main.go", []byte("package main\n\nconst Needle = true\n"), 0o644))

	preview := PreviewWithGlobalSearch("inspect", []string{"main.go"}, "needle", 96, 24, false)

	require.True(t, preview.GlobalSearch)
	require.Equal(t, []string{"main.go:3"}, preview.Matches)
	require.Contains(t, preview.View, "global search: needle")

	accepted := PreviewWithGlobalSearch("inspect", []string{"main.go"}, "needle", 96, 24, true)
	require.False(t, accepted.GlobalSearch)
	require.Equal(t, "inspect @main.go#L3 ", accepted.Value)
}

func TestModelPickerSelectsRuntimeModel(t *testing.T) {
	ta := newPromptTextarea("keep draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.modelOptions = []string{"sonnet", "opus"}
	m.currentModel = "sonnet"
	m.selectModel = func(_ context.Context, model string) (RuntimeControlResult, error) {
		return RuntimeControlResult{Title: "Model", Status: "model selected", Lines: []string{"Model: " + model}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	m = updated.(model)

	require.True(t, m.modelPicker)
	require.Equal(t, 0, m.modelPickerSelected)
	require.Contains(t, m.View(), "model picker")
	require.Contains(t, m.View(), "sonnet  current")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.False(t, m.modelPicker)
	require.Equal(t, "opus", m.currentModel)
	require.Equal(t, "keep draft", m.textarea.Value())
	require.Equal(t, "model selected", m.status)
	require.Contains(t, m.View(), "Model")
	require.Contains(t, m.View(), "Model: opus")
}

func TestModelPickerVimNavigation(t *testing.T) {
	ta := newPromptTextarea("keep draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.modelOptions = []string{"haiku", "sonnet", "opus"}
	m.currentModel = "haiku"
	m.selectModel = func(_ context.Context, model string) (RuntimeControlResult, error) {
		return RuntimeControlResult{Title: "Model", Status: "model selected", Lines: []string{"Model: " + model}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	m = updated.(model)
	require.True(t, m.modelPicker)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(model)
	require.Equal(t, 1, m.modelPickerSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(model)
	require.Equal(t, 0, m.modelPickerSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	m = updated.(model)
	require.Equal(t, 2, m.modelPickerSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	m = updated.(model)
	require.Equal(t, 0, m.modelPickerSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	require.Equal(t, 1, m.modelPickerSelected)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, "sonnet", m.currentModel)
}

func TestRuntimeToggleShortcutsAppendStatus(t *testing.T) {
	preview := PreviewWithRuntimeToggle("", "alt+o", RuntimeControlResult{
		Title:  "Fast Mode",
		Status: "fast on",
		Lines:  []string{"Fast mode: on", "Previous: off"},
	}, 96, 24)

	require.Equal(t, "ready", preview.Mode)
	require.Contains(t, preview.View, "Fast Mode")
	require.Contains(t, preview.View, "Fast mode: on")
	require.Contains(t, preview.View, "fast: on")

	thinking := PreviewWithRuntimeToggle("", "alt+t", RuntimeControlResult{
		Title:  "Thinking",
		Status: "thinking medium",
		Lines:  []string{"Reasoning: medium", "Previous: low"},
	}, 96, 24)

	require.Contains(t, thinking.View, "Thinking")
	require.Contains(t, thinking.View, "Reasoning: medium")
	require.Contains(t, thinking.View, "thinking: medium")

	undo := PreviewWithRuntimeControl("", "ctrl+x ctrl+u", RuntimeControlResult{
		Title:  "Undo",
		Status: "restored",
		Lines:  []string{"Path: notes.txt", "Restored: true"},
	}, 96, 24)

	require.Contains(t, undo.View, "Undo")
	require.Contains(t, undo.View, "Path: notes.txt")

	exported := PreviewWithRuntimeControl("", "ctrl+x ctrl+s", RuntimeControlResult{
		Title:  "Conversation Exported",
		Status: "exported",
		Lines:  []string{"Session: session-1", "File: .codog/exports/session-1.md"},
	}, 96, 24)

	require.Contains(t, exported.View, "Conversation Exported")
	require.Contains(t, exported.View, ".codog/exports/session-1.md")

	copied := PreviewWithRuntimeControl("", "ctrl+x ctrl+y", RuntimeControlResult{
		Title:  "Conversation Copied",
		Status: "copied",
		Lines:  []string{"Session: session-1", "Clipboard: pbcopy", "Bytes: 128"},
	}, 96, 24)

	require.Contains(t, copied.View, "Conversation Copied")
	require.Contains(t, copied.View, "Clipboard: pbcopy")
}

func TestCustomTUIKeybindingsTriggerComposerActions(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"edit composer in $EDITOR": {"ctrl+e"},
	})
	m.externalEditor = func(_ context.Context, value string) (string, error) {
		require.Equal(t, "draft", value)
		return "edited", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(model)
	require.NotNil(t, cmd)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "edited", m.textarea.Value())
	require.Equal(t, "editor updated", m.status)
}

func TestCustomTUIKeybindingsOpenQuickSearchAndStash(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.fileCandidates = []string{"internal/tui/tui.go"}
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"quick open files":          {"alt+q"},
		"stash or restore composer": {"ctrl+y"},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}, Alt: true})
	m = updated.(model)
	require.True(t, m.quickOpen)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.quickOpen)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(model)
	require.Empty(t, m.textarea.Value())
	require.NotNil(t, m.stashedPrompt)
	require.Equal(t, "prompt stashed", m.status)
}

func TestCustomTUIKeybindingsDeleteAroundCursor(t *testing.T) {
	ta := newPromptTextarea("first second")
	ta.SetCursor(len("first"))
	m := newModel(context.Background(), ta, nil, nil)
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"delete before cursor": {"alt+u"},
		"delete after cursor":  {"alt+k"},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}, Alt: true})
	m = updated.(model)
	require.Equal(t, " second", m.textarea.Value())
	require.Equal(t, "deleted before cursor", m.status)

	m.textarea.SetValue("first second")
	m.textarea.SetCursor(len("first"))
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}, Alt: true})
	m = updated.(model)
	require.Equal(t, "first", m.textarea.Value())
	require.Equal(t, "deleted after cursor", m.status)
}
