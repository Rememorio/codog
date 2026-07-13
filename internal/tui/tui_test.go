package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	m.slash = func(_ context.Context, line string) (string, bool, error) {
		require.Equal(t, "/status", line)
		return "Tools ok", true, nil
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
	view := renderQueuedPrompts([]string{"first queued prompt", "second queued prompt"})

	require.Contains(t, view, "queued prompts: 2")
	require.Contains(t, view, "1. first queued prompt")
	require.Contains(t, view, "2. second queued prompt")
	require.Empty(t, renderQueuedPrompts(nil))
}

func TestQueuedPromptsRenderBashMode(t *testing.T) {
	view := renderQueuedPrompts([]string{"!printf codog", "regular prompt"})

	require.Contains(t, view, "queued prompts: 2")
	require.Contains(t, view, "1. bash: printf codog")
	require.Contains(t, view, "2. regular prompt")
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
	m.slash = func(context.Context, string) (string, bool, error) {
		t.Fatal("bare /paste should be handled by the TUI")
		return "", false, nil
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
	m.slash = func(_ context.Context, line string) (string, bool, error) {
		called = true
		require.Equal(t, "/paste --json", line)
		return "{}", true, nil
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
	m.slash = func(context.Context, string) (string, bool, error) {
		t.Fatal("bare bash mode should not run")
		return "", false, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "!", m.textarea.Value())
	require.Equal(t, "bash", m.status)
	require.Contains(t, m.View(), "! for bash mode")
}

func TestEscapeClearsComposerBeforeExit(t *testing.T) {
	ta := newPromptTextarea("draft prompt")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.textarea.Value())
	require.Equal(t, "input cleared · press esc again to exit", m.status)
	require.Contains(t, m.View(), "Esc again to exit")

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)
	_, ok := cmd().(tea.QuitMsg)
	require.True(t, ok)
}

func TestEscapeExitPendingResetsAfterTyping(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.exitPending)

	updated, _ = m.Update(teaKey("x"))
	m = updated.(model)
	require.False(t, m.exitPending)
	require.Equal(t, "x", m.textarea.Value())
}

func TestControlCClearsComposerThenExitsOnSecondPress(t *testing.T) {
	ta := newPromptTextarea("draft prompt")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(model)
	require.Nil(t, cmd)
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
	require.Contains(t, footer, "accept edits")
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
	m.queuedPrompts = []string{"next"}

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

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(model)

	require.Equal(t, "accept edits", m.modeLabel)
	require.Equal(t, "system", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "Mode: accept edits", m.transcript[len(m.transcript)-1].Text)
	require.Contains(t, m.View(), "accept edits")
}

func TestAltMCyclesTUILocalModeFallback(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.modeLabel = "default"
	m.cycleMode = func() string { return "plan" }

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	m = updated.(model)

	require.Equal(t, "plan", m.modeLabel)
	require.Empty(t, m.textarea.Value())
	require.Equal(t, "system", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "Mode: plan", m.transcript[len(m.transcript)-1].Text)
	require.Contains(t, helpPanel(nil, 100), "Alt/Meta+M")
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

func TestTUILineStartAndEndActionsMoveComposerCursor(t *testing.T) {
	ta := newPromptTextarea("middle")
	ta.CursorEnd()
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = updated.(model)
	require.Equal(t, "line start", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
	m = updated.(model)
	require.Equal(t, ">middle", m.textarea.Value())

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(model)
	require.Equal(t, "line start", m.status)

	m.textarea.SetValue("middle")
	m.textarea.CursorStart()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	require.Equal(t, "line end", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	m = updated.(model)
	require.Equal(t, "middle!", m.textarea.Value())
}

func TestCustomTUIKeybindingsMoveComposerCursor(t *testing.T) {
	ta := newPromptTextarea("middle")
	ta.CursorEnd()
	m := newModel(context.Background(), ta, nil, nil)
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"move to line start": {"alt+h"},
		"move to line end":   {"alt+l"},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	m = updated.(model)
	require.Equal(t, "line start", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
	m = updated.(model)
	require.Equal(t, ">middle", m.textarea.Value())

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	m = updated.(model)
	require.Equal(t, "line end", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
	m = updated.(model)
	require.Equal(t, ">middle!", m.textarea.Value())
}

func TestCustomTUIKeybindingChordOpensEditor(t *testing.T) {
	preview := PreviewWithKeybindings("draft", map[string][]string{
		"edit composer in $EDITOR": {"ctrl+x ctrl+e"},
	}, nil, "ctrl+x ctrl+e", 96, 24)

	require.Equal(t, "edited: draft", preview.Value)
	require.Contains(t, preview.View, "editor updated")
}

func TestCustomTUIKeybindingChordTriggersRuntimeAction(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"copy current conversation": {"ctrl+x ctrl+y"},
	})
	called := false
	m.copyConversation = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Conversation Copied", Status: "copied", Lines: []string{"Clipboard: test"}}, nil
	}

	updated, cmd := m.Update(previewKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.Equal(t, "ctrl+x", m.keyChordPrefix)
	require.False(t, m.ctrlXChord)

	updated, cmd = m.Update(previewKey("ctrl+y"))
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Empty(t, m.keyChordPrefix)
	require.Equal(t, "copying", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.Equal(t, "copied", m.status)
	require.Contains(t, m.View(), "Conversation Copied")
}

func TestCustomTUIKeybindingChordFallsBackToDefaultCtrlX(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.keybindings = normalizeTUIKeybindings(map[string][]string{
		"edit composer in $EDITOR": {"ctrl+x ctrl+e"},
	})
	called := false
	m.undoLast = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Undo", Status: "restored", Lines: []string{"Path: notes.txt"}}, nil
	}

	updated, cmd := m.Update(previewKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.Equal(t, "ctrl+x", m.keyChordPrefix)

	updated, cmd = m.Update(previewKey("ctrl+u"))
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Empty(t, m.keyChordPrefix)
	require.Equal(t, "undoing", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.Contains(t, m.View(), "Path: notes.txt")
}

func TestCustomTUIContextKeybindingsDriveModalNavigation(t *testing.T) {
	preview := PreviewWithContextKeybindings("model-picker", map[string]map[string][]string{
		"tui-modal": {
			"move modal selection down":      {"alt+j"},
			"jump modal selection to bottom": {"alt+e"},
		},
	}, []string{"alt+j", "alt+e", "enter"}, 96, 24)

	require.False(t, preview.ModelPicker)
	require.Contains(t, preview.View, "Model: gamma")

	target := PreviewWithContextKeybindings("message-actions", map[string]map[string][]string{
		"tui-modal": {
			"move message target backward":  {"alt+h"},
			"move to previous user message": {"alt+u"},
		},
	}, []string{"alt+h", "alt+u", "enter"}, 96, 24)

	require.False(t, target.MessageMenu)
	require.Equal(t, "first prompt", target.Value)
}

func TestCustomTUIContextKeybindingsDriveAttachmentsAndDiff(t *testing.T) {
	attachments := PreviewWithContextKeybindings("attachments", map[string]map[string][]string{
		"tui-attachments": {
			"select next attachment":     {"alt+j"},
			"remove selected attachment": {"alt+x"},
		},
	}, []string{"alt+j", "alt+x"}, 96, 24)

	require.True(t, attachments.AttachmentsOpen)
	require.Equal(t, []string{"one.txt", "three.txt"}, attachments.Attachments)
	require.Contains(t, attachments.View, "three.txt")

	diff := PreviewWithContextKeybindings("diff", map[string]map[string][]string{
		"tui-diff": {
			"select next changed file": {"alt+j"},
			"view selected file diff":  {"alt+o"},
		},
	}, []string{"alt+j", "alt+o"}, 96, 24)

	require.True(t, diff.DiffDialog)
	require.Equal(t, "diff detail", diff.Mode)
	require.Contains(t, diff.View, "ADDED main_test.go")
}

func TestPreviewWithModelPicker(t *testing.T) {
	preview := PreviewWithModelPicker("inspect", []string{"sonnet", "opus"}, "sonnet", 96, 24, false)

	require.True(t, preview.ModelPicker)
	require.Equal(t, []string{"sonnet", "opus"}, preview.Matches)
	require.Equal(t, "inspect", preview.Value)
	require.Contains(t, preview.View, "model picker")

	selected := PreviewWithModelPicker("inspect", []string{"sonnet", "opus"}, "sonnet", 96, 24, true)

	require.False(t, selected.ModelPicker)
	require.Contains(t, selected.View, "Model: sonnet")
}

func TestMessageActionsCopyQuoteAndStash(t *testing.T) {
	entries := []transcriptEntry{{Role: "assistant", Text: "first line\nsecond line"}}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)
	var copiedMessage string
	m.copyMessage = func(_ context.Context, text string) (RuntimeControlResult, error) {
		copiedMessage = text
		return RuntimeControlResult{Title: "Message Copied", Status: "message copied", Lines: []string{"Clipboard: test-clipboard"}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)

	require.True(t, m.messageActions)
	require.Contains(t, m.View(), "message actions")
	require.Contains(t, m.View(), "copy to composer")
	require.Contains(t, m.View(), "copy to clipboard")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.False(t, m.messageActions)
	require.Equal(t, "first line\nsecond line", m.textarea.Value())
	require.Equal(t, "message copied", m.status)

	m.textarea.SetValue("")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "copying message", m.status)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.False(t, m.messageActions)
	require.Equal(t, "first line\nsecond line", copiedMessage)
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "message copied", m.status)
	require.Contains(t, m.View(), "Message Copied")
	require.Contains(t, m.View(), "Clipboard: test-clipboard")

	ta = newPromptTextarea("")
	m = newModel(context.Background(), ta, nil, entries)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 2 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, "> first line\n> second line", m.textarea.Value())
	require.Equal(t, "message quoted", m.status)

	m.textarea.SetValue("")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 3 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, m.stashedPrompt)
	require.Equal(t, "first line\nsecond line", m.stashedPrompt.Text)
	require.Equal(t, "message stashed", m.status)
}

func TestMessageActionsNavigateTargetMessages(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)

	require.True(t, m.messageActions)
	require.Contains(t, m.View(), "message actions 4/4")
	require.Contains(t, m.View(), "assistant: second answer")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)

	require.Contains(t, m.View(), "message actions 2/4")
	require.Contains(t, m.View(), "assistant: first answer")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.messageActions)
	require.Equal(t, "first answer", m.textarea.Value())
	require.Equal(t, "message copied", m.status)
}

func TestMessageActionsModalNavigation(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	require.True(t, m.messageActions)
	require.Equal(t, 0, m.messageActionSelected)
	require.Equal(t, 3, m.messageActionTarget)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(model)
	require.Equal(t, 1, m.messageActionSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(model)
	require.Equal(t, 0, m.messageActionSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	m = updated.(model)
	require.Equal(t, len(messageActionLabels)-1, m.messageActionSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	m = updated.(model)
	require.Equal(t, 0, m.messageActionSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	require.Equal(t, 2, m.messageActionTarget)
	require.Contains(t, m.View(), "user: second prompt")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	require.Equal(t, 0, m.messageActionTarget)
	require.Contains(t, m.View(), "user: first prompt")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	m = updated.(model)
	require.Equal(t, 2, m.messageActionTarget)
}

func TestMessageActionsRestoreBeforeTurn(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "system", Text: "ready"},
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)
	var keep int
	m.restoreConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		keep = keepMessages
		return RuntimeControlResult{Title: "Conversation Restored", Status: "restored 2", Lines: []string{"Remaining: 2", "Removed: 2"}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 4 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "restoring", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, 2, keep)
	require.False(t, m.messageActions)
	require.Equal(t, "restored 2", m.status)
	require.Contains(t, m.View(), "Conversation Restored")
	require.Contains(t, m.View(), "Removed: 2")
}

func TestMessageActionsForkBeforeTurn(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "system", Text: "ready"},
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)
	var keep int
	m.forkConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		keep = keepMessages
		return RuntimeControlResult{Title: "Conversation Forked", Status: "forked 2", Lines: []string{"Remaining: 2", "Removed: 2"}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 5 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "forking", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, 2, keep)
	require.False(t, m.messageActions)
	require.Equal(t, "forked 2", m.status)
	require.Contains(t, m.View(), "Conversation Forked")
	require.Contains(t, m.View(), "Removed: 2")
}

func TestMessageActionsSummarizeFromTurn(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "system", Text: "ready"},
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)
	var keep int
	m.summarizeConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		keep = keepMessages
		return RuntimeControlResult{Title: "Conversation Summarized", Status: "summarized 2", Lines: []string{"Before: 2", "Summarized: 2"}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 6 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "summarizing", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, 2, keep)
	require.False(t, m.messageActions)
	require.Equal(t, "summarized 2", m.status)
	require.Contains(t, m.View(), "Conversation Summarized")
	require.Contains(t, m.View(), "Summarized: 2")
}

func TestMessageActionsSummarizeUpToTurn(t *testing.T) {
	entries := []transcriptEntry{
		{Role: "system", Text: "ready"},
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first answer"},
		{Role: "user", Text: "second prompt"},
		{Role: "assistant", Text: "second answer"},
	}
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, entries)
	var keep int
	m.summarizeUpToConversation = func(_ context.Context, keepMessages int) (RuntimeControlResult, error) {
		keep = keepMessages
		return RuntimeControlResult{Title: "Earlier Conversation Summarized", Status: "summarized earlier 2", Lines: []string{"Summarized: 2", "After: 2"}}, nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m = updated.(model)
	for range 7 {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "summarizing", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, 2, keep)
	require.False(t, m.messageActions)
	require.Equal(t, "summarized earlier 2", m.status)
	require.Contains(t, m.View(), "Earlier Conversation Summarized")
	require.Contains(t, m.View(), "After: 2")
}

func TestPreviewWithMessageActions(t *testing.T) {
	preview := PreviewWithMessageActions([]Entry{{Role: "assistant", Text: "copy me"}}, 96, 24, -1)

	require.True(t, preview.MessageMenu)
	require.Equal(t, messageActionLabels, preview.Matches)
	require.Contains(t, preview.View, "message actions")

	copied := PreviewWithMessageActions([]Entry{{Role: "assistant", Text: "copy me"}}, 96, 24, 0)

	require.False(t, copied.MessageMenu)
	require.Equal(t, "copy me", copied.Value)

	copiedClipboard := PreviewWithMessageActions([]Entry{{Role: "assistant", Text: "copy me"}}, 96, 24, 1)
	require.False(t, copiedClipboard.MessageMenu)
	require.Contains(t, copiedClipboard.View, "Message Copied")

	restored := PreviewWithMessageActions([]Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 4)
	require.False(t, restored.MessageMenu)
	require.Contains(t, restored.View, "Conversation Restored")

	forked := PreviewWithMessageActions([]Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 5)
	require.False(t, forked.MessageMenu)
	require.Contains(t, forked.View, "Conversation Forked")

	summarized := PreviewWithMessageActions([]Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 6)
	require.False(t, summarized.MessageMenu)
	require.Contains(t, summarized.View, "Conversation Summarized")

	summarizedUpTo := PreviewWithMessageActions([]Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 7)
	require.False(t, summarizedUpTo.MessageMenu)
	require.Contains(t, summarizedUpTo.View, "Earlier Conversation Summarized")

	targeted := PreviewWithMessageActionTarget([]Entry{{Role: "assistant", Text: "first"}, {Role: "assistant", Text: "second"}}, 96, 24, 0, -1)
	require.False(t, targeted.MessageMenu)
	require.Equal(t, "first", targeted.Value)
}

func TestCtrlXCtrlKStopsBackgroundTasks(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.stopBackground = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Background Tasks", Status: "stopped 1", Lines: []string{"Stopped: 1", "agent: task-1"}}, nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "stopping background", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.False(t, m.ctrlXChord)
	require.Equal(t, "stopped 1", m.status)
	require.Contains(t, m.View(), "Background Tasks")
	require.Contains(t, m.View(), "agent: task-1")
}

func TestCtrlXCtrlCCompactsSession(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.compactSession = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Session Compacted", Status: "compacted 3", Lines: []string{"Session: session-1", "Removed: 3"}}, nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "compacting", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.False(t, m.ctrlXChord)
	require.Equal(t, "compacted 3", m.status)
	require.Contains(t, m.View(), "Session Compacted")
	require.Contains(t, m.View(), "Removed: 3")
}

func TestCtrlXCtrlUUndoesLastChange(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.undoLast = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Undo", Status: "restored", Lines: []string{"Path: notes.txt", "Restored: true"}}, nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "undoing", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.False(t, m.ctrlXChord)
	require.Equal(t, "restored", m.status)
	require.Contains(t, m.View(), "Undo")
	require.Contains(t, m.View(), "Path: notes.txt")
}

func TestCtrlXCtrlSExportsConversation(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.exportConversation = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Conversation Exported", Status: "exported", Lines: []string{"Session: session-1", "File: .codog/exports/session-1.md"}}, nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "exporting", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.False(t, m.ctrlXChord)
	require.Equal(t, "exported", m.status)
	require.Contains(t, m.View(), "Conversation Exported")
	require.Contains(t, m.View(), ".codog/exports/session-1.md")
}

func TestCtrlXCtrlYCopiesConversation(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	called := false
	m.copyConversation = func(context.Context) (RuntimeControlResult, error) {
		called = true
		return RuntimeControlResult{Title: "Conversation Copied", Status: "copied", Lines: []string{"Session: session-1", "Clipboard: pbcopy"}}, nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Equal(t, "copying", m.status)

	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.True(t, called)
	require.False(t, m.ctrlXChord)
	require.Equal(t, "copied", m.status)
	require.Contains(t, m.View(), "Conversation Copied")
	require.Contains(t, m.View(), "Clipboard: pbcopy")
}

func TestPreviewTogglesHelpPanel(t *testing.T) {
	preview := PreviewWithCandidates("/help", []string{"/status", "/context"}, 100, 24, false, false)
	require.True(t, preview.HelpOpen)
	require.Contains(t, preview.View, "Core commands")

	ta := newPromptTextarea("/help")
	m := newModel(context.Background(), ta, []string{"/status", "/context"}, nil)
	updated, _ := m.Update(teaKey("enter"))
	next := updated.(model)

	require.True(t, next.helpOpen)
	require.Empty(t, next.textarea.Value())
	require.Contains(t, next.View(), "Core commands")
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

func TestCtrlSStashesAndRestoresComposer(t *testing.T) {
	ta := newPromptTextarea("draft prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"notes.txt"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.textarea.Value())
	require.Empty(t, m.attachments)
	require.NotNil(t, m.stashedPrompt)
	require.Equal(t, "prompt stashed", m.status)
	require.Contains(t, m.View(), "stashed prompt")
	require.Contains(t, m.View(), "Ctrl+S restore")
	require.Contains(t, m.View(), "attachments: 1")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "draft prompt", m.textarea.Value())
	require.Equal(t, []string{"notes.txt"}, m.attachments)
	require.Nil(t, m.stashedPrompt)
	require.Equal(t, "stash restored", m.status)
}

func TestCtrlSWithoutDraftReportsNothingToStash(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, "nothing to stash", m.status)
	require.Nil(t, m.stashedPrompt)
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

func TestBusyExitCancelsTurnAndQuitsWithoutQueueing(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.submit = func(context.Context, string) (string, error) { return "done", nil }

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, firstCmd)
	require.NotNil(t, m.turnCancel)

	m.textarea.SetValue("/exit")
	updated, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, quitCmd)
	_, ok := quitCmd().(tea.QuitMsg)
	require.True(t, ok)
	require.Nil(t, m.turnCancel)
	require.Empty(t, m.queuedPrompts)
	require.Empty(t, m.textarea.Value())
	require.Equal(t, "exiting", m.status)
}

func TestTurnErrorRestoresQueuedPromptsAndDraft(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	m.submit = func(context.Context, string) (string, error) { return "done", nil }

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, firstCmd)
	m.textarea.SetValue("second prompt")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	m.textarea.SetValue("third prompt")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	m.textarea.SetValue("unsent draft")

	updated, nextCmd := m.Update(turnDoneMsg{Role: "assistant", Err: errors.New("provider unavailable")})
	m = updated.(model)
	require.Nil(t, nextCmd)
	require.False(t, m.busy)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "second prompt\n\nthird prompt\n\nunsent draft", m.textarea.Value())
	require.Equal(t, "error · 2 queued restored", m.status)
	require.Contains(t, m.View(), "provider unavailable")
	require.Contains(t, m.View(), "Restored 2 queued prompts to the composer after the failed turn.")
}

func TestBusyEnterQueuesBashPromptAndRunsThroughSlash(t *testing.T) {
	ta := newPromptTextarea("first prompt")
	m := newModel(context.Background(), ta, nil, nil)
	prompts := []string{}
	slashLines := []string{}
	m.submit = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "done: " + prompt, nil
	}
	m.slash = func(_ context.Context, line string) (string, bool, error) {
		slashLines = append(slashLines, line)
		return "ran " + line, true, nil
	}

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, firstCmd)

	m.textarea.SetValue("!printf codog")
	updated, queueCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"!printf codog"}, m.queuedPrompts)
	require.Contains(t, m.View(), "Queued bash 1")
	require.Contains(t, m.View(), "bash: printf codog")

	firstDone := firstCmd().(turnDoneMsg)
	updated, slashCmd := m.Update(firstDone)
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, slashCmd)
	require.Equal(t, []string{"first prompt"}, prompts)
	require.Equal(t, "user", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "!printf codog", m.transcript[len(m.transcript)-1].Text)

	updated, _ = m.Update(slashCmd())
	m = updated.(model)
	require.False(t, m.busy)
	require.Equal(t, []string{"/run printf codog"}, slashLines)
	require.Contains(t, m.View(), "ran /run printf codog")
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

func TestInterruptedTurnRestoresQueuedPrompts(t *testing.T) {
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
	require.Equal(t, "queued prompt", m.textarea.Value())
	require.Equal(t, "interrupted · 1 queued restored", m.status)
	require.Contains(t, m.View(), "Restored 1 queued prompt to the composer after the interrupted turn.")
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
	messages := make(chan tea.Msg, 6)
	permission := &PermissionRequest{Tool: "bash", Required: "workspace-write", AllowAlways: true}
	tool := &ToolActivity{ID: "tool-1", Name: "bash", Status: "running"}
	cmd := runStreamSubmitCommand(ctx, func(_ context.Context, prompt string, emit func(Entry)) (string, error) {
		emit(Entry{Role: "assistant", Text: "first "})
		emit(Entry{Role: "assistant", Text: prompt})
		emit(Entry{Role: "permission", Text: "confirm", Permission: permission})
		emit(Entry{Role: "tool", Text: "running", Tool: tool})
		return "", nil
	}, "chunk", messages)

	first := cmd()
	require.Equal(t, turnStreamMsg{Role: "assistant", Delta: "first "}, first)
	second := waitTurnMessage(messages)()
	require.Equal(t, turnStreamMsg{Role: "assistant", Delta: "chunk"}, second)
	third := waitTurnMessage(messages)()
	require.Equal(t, turnStreamMsg{Role: "permission", Delta: "confirm", Permission: permission}, third)
	fourth := waitTurnMessage(messages)()
	require.Equal(t, turnStreamMsg{Role: "tool", Delta: "running", Tool: tool}, fourth)
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

func TestStructuredToolActivityUpdatesInPlace(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	start := &ToolActivity{
		ID:     "tool-bash",
		Name:   "bash",
		Input:  `{"command":"printf ok"}`,
		Status: "running",
	}
	updated, _ := m.Update(turnStreamMsg{Role: "tool", Delta: "legacy start", Tool: start})
	m = updated.(model)

	require.Len(t, m.transcript, 2)
	require.NotSame(t, start, m.transcript[1].Tool)
	require.Equal(t, "running bash", m.status)
	require.Contains(t, m.View(), "Bash(printf ok)")
	require.Contains(t, m.View(), "running")

	finish := &ToolActivity{
		ID:     "tool-bash",
		Name:   "bash",
		Input:  `{"command":"printf ok"}`,
		Output: `{"stdout":"ok","exit_code":0,"duration_ms":3}`,
		Status: "success",
	}
	updated, _ = m.Update(turnStreamMsg{Role: "tool", Delta: "legacy finish", Tool: finish})
	m = updated.(model)

	require.Len(t, m.transcript, 2)
	require.Equal(t, "success", m.transcript[1].Tool.Status)
	require.Equal(t, "streaming", m.status)
	require.Contains(t, m.View(), "Bash(printf ok)")
	require.Contains(t, m.View(), "ok")
	require.NotContains(t, m.View(), "legacy start")

	m.transcriptMode = true
	m.refreshViewport()
	require.Contains(t, m.View(), `"duration_ms":3`)
}

func TestStructuredToolActivitiesKeepDistinctIDsAndAppendMissingStart(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	activities := []*ToolActivity{
		{ID: "read-1", Name: "read_file", Input: `{"path":"README.md"}`, Status: "running"},
		{ID: "grep-1", Name: "grep", Input: `{"pattern":"TODO","path":"internal"}`, Output: "match", Status: "success"},
		{ID: "read-1", Name: "read_file", Input: `{"path":"README.md"}`, Output: "line one\nline two", Status: "success"},
	}
	for _, activity := range activities {
		updated, _ := m.Update(turnStreamMsg{Role: "tool", Tool: activity})
		m = updated.(model)
	}

	require.Len(t, m.transcript, 3)
	require.Equal(t, "read-1", m.transcript[1].Tool.ID)
	require.Equal(t, "success", m.transcript[1].Tool.Status)
	require.Equal(t, "grep-1", m.transcript[2].Tool.ID)
	require.Contains(t, m.View(), "Read(README.md)")
	require.Contains(t, m.View(), "Grep(TODO in internal)")
}

func TestStructuredToolActivityRendersErrorsAndTruncatesCompactOutput(t *testing.T) {
	activity := ToolActivity{
		ID:      "tool-error",
		Name:    "write_file",
		Input:   `{"path":"nested/output.txt"}`,
		Output:  "one\ntwo\nthree\nfour\nfive\nsix",
		Status:  "success",
		IsError: true,
	}
	compact := renderToolActivity(activity, 48, false, stylesForTheme("no-color"))
	expanded := renderToolActivity(activity, 48, true, stylesForTheme("no-color"))

	require.Contains(t, compact, "! Write(nested/output.txt) failed")
	require.Contains(t, compact, "... 2 more lines")
	require.NotContains(t, compact, "six")
	require.Contains(t, expanded, "six")
	require.NotContains(t, compact, "\x1b[")
}

func TestStructuredToolActivityHeaderFitsNarrowViewport(t *testing.T) {
	rendered := renderToolActivity(ToolActivity{
		Name:   "write_file",
		Input:  `{"path":"a/very/long/output/path.txt"}`,
		Status: "running",
	}, 12, false, stylesForTheme("no-color"))

	header := strings.SplitN(rendered, "\n", 2)[0]
	require.LessOrEqual(t, lipgloss.Width(header), 12)
	require.Contains(t, header, "running")
}

func TestToolActivityInputSummaryCoversKnownAndFallbackTools(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "bash", input: `{"command":"go test ./..."}`, want: "go test ./..."},
		{name: "read_file", input: `{"file_path":"main.go"}`, want: "main.go"},
		{name: "multi_edit", input: `{"edits":[{"file_path":"first.go"}]}`, want: "first.go"},
		{name: "apply_patch", input: `{"patch":"ignored"}`, want: "patch"},
		{name: "notebook_edit", input: `{"notebook_path":"analysis.ipynb"}`, want: "analysis.ipynb"},
		{name: "glob", input: `{"pattern":"*.go","path":"internal"}`, want: "*.go in internal"},
		{name: "web_search", input: `{"query":"Go release"}`, want: "Go release"},
		{name: "ask_user_question", input: `{"questions":[{},{}]}`, want: "2 questions"},
		{name: "custom_tool", input: `{"prompt":"inspect"}`, want: "inspect"},
		{name: "custom_tool", input: "raw input", want: "raw input"},
	}
	for _, tc := range tests {
		t.Run(tc.name+tc.want, func(t *testing.T) {
			require.Equal(t, tc.want, toolActivityInputSummary(tc.name, tc.input))
		})
	}
}

func TestToolActivityDisplayNameCoversBuiltinFamilies(t *testing.T) {
	tests := map[string]string{
		"bash":              "Bash",
		"powershell":        "PowerShell",
		"read_file":         "Read",
		"write_file":        "Write",
		"edit_file":         "Edit",
		"grep":              "Grep",
		"glob":              "Glob",
		"web_search":        "Web Search",
		"web_fetch":         "Web Fetch",
		"ask_user_question": "Ask User",
		"task_create":       "Task Create",
		"":                  "Tool",
	}
	for input, want := range tests {
		t.Run(want+input, func(t *testing.T) {
			require.Equal(t, want, toolActivityDisplayName(input))
		})
	}
}

func TestToolActivityOutputLinesSummarizeStructuredResults(t *testing.T) {
	tests := []struct {
		name     string
		activity ToolActivity
		expanded bool
		want     []string
	}{
		{name: "empty", activity: ToolActivity{}, want: nil},
		{name: "stdout and error", activity: ToolActivity{Output: `{"stdout":"ok","error":"exit status 7"}`}, want: []string{"ok", "error: exit status 7"}},
		{name: "stdout and stderr", activity: ToolActivity{Output: `{"stdout":"ok","stderr":"warning"}`}, want: []string{"ok", "stderr: warning"}},
		{name: "path bytes", activity: ToolActivity{Output: `{"path":"out.txt","bytes":12}`}, want: []string{"out.txt · 12 bytes"}},
		{name: "exit status", activity: ToolActivity{Output: `{"exit_code":0,"duration_ms":4}`}, want: []string{"Exit code 0 · 4 ms"}},
		{name: "message", activity: ToolActivity{Output: `{"message":"updated"}`}, want: []string{"updated"}},
		{name: "expanded raw json", activity: ToolActivity{Output: "{\n  \"stdout\": \"ok\"\n}"}, expanded: true, want: []string{"{", `  "stdout": "ok"`, "}"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, toolActivityOutputLines(tc.activity, tc.expanded))
		})
	}
}

func TestPermissionStreamEntryNavigatesAndAcceptsSelection(t *testing.T) {
	ta := newPromptTextarea("")
	answers := []string{}
	m := newModel(context.Background(), ta, nil, nil)
	m.permissionAnswer = func(answer string) {
		answers = append(answers, answer)
	}

	request := &PermissionRequest{
		Tool:        "bash",
		Required:    "danger-full-access",
		Input:       `{"command":"rm -rf build"}`,
		Message:     "destructive command",
		AllowAlways: true,
	}
	updated, _ := m.Update(turnStreamMsg{Role: "permission", Delta: "Permission\n- bash requires danger-full-access", Permission: request})
	m = updated.(model)
	require.True(t, m.awaitingPermission)
	require.Equal(t, "permission", m.status)
	require.Contains(t, m.View(), "Allow bash to use danger-full-access?")
	require.Contains(t, m.View(), "destructive command")
	require.Contains(t, m.View(), "don't ask again")
	require.Contains(t, statusBarText(m.status, 80), "Up/Down choose")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Equal(t, 1, m.permissionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.awaitingPermission)
	require.Equal(t, []string{"a"}, answers)
	require.Equal(t, "permission answered", m.status)

	updated, _ = m.Update(turnStreamMsg{Role: "permission", Delta: "Permission\n- bash approved: user_approved"})
	m = updated.(model)
	require.False(t, m.awaitingPermission)
	require.Equal(t, "permission answered", m.status)
}

func TestPermissionRequestKeepsShortcutsAndEscapeDenial(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.permissionAnswer = func(answer string) { answers = append(answers, answer) }

	m.openPermissionRequest(PermissionRequest{Tool: "write_file", Required: "workspace-write"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(model)
	require.Equal(t, []string{"y"}, answers)

	m.openPermissionRequest(PermissionRequest{Tool: "bash", Required: "danger-full-access"})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Equal(t, []string{"y", "n"}, answers)
	require.False(t, m.awaitingPermission)
}

func TestPermissionRequestNavigationWrapsAndSupportsHomeEnd(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.permissionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openPermissionRequest(PermissionRequest{Tool: "bash", AllowAlways: true})

	keysAndSelections := []struct {
		key       tea.KeyMsg
		selection int
	}{
		{key: tea.KeyMsg{Type: tea.KeyEnd}, selection: 2},
		{key: tea.KeyMsg{Type: tea.KeyHome}, selection: 0},
		{key: tea.KeyMsg{Type: tea.KeyUp}, selection: 2},
		{key: tea.KeyMsg{Type: tea.KeyDown}, selection: 0},
	}
	for _, tc := range keysAndSelections {
		updated, _ := m.Update(tc.key)
		m = updated.(model)
		require.Equal(t, tc.selection, m.permissionSelected)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.True(t, m.permissionInput)
	require.Equal(t, 0, m.permissionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.False(t, m.permissionInput)

	for _, tc := range []struct {
		key       tea.KeyMsg
		selection int
	}{
		{key: tea.KeyMsg{Type: tea.KeyShiftTab}, selection: 2},
		{key: tea.KeyMsg{Type: tea.KeyRight}, selection: 0},
		{key: tea.KeyMsg{Type: tea.KeyLeft}, selection: 2},
	} {
		updated, _ = m.Update(tc.key)
		m = updated.(model)
		require.Equal(t, tc.selection, m.permissionSelected)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(model)
	require.Equal(t, []string{"a"}, answers)
	m.openPermissionRequest(PermissionRequest{Tool: "read_file"})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(model)
	require.True(t, m.awaitingPermission)
	require.Equal(t, []string{"a"}, answers)
}

func TestPermissionFeedbackInputRespondsAndRestoresComposerDraft(t *testing.T) {
	responses := []PermissionResponse{}
	m := newModel(context.Background(), newPromptTextarea("queued draft"), nil, nil)
	m.permissionRespond = func(response PermissionResponse) { responses = append(responses, response) }
	m.openPermissionRequest(PermissionRequest{
		Tool:          "bash",
		Required:      "danger-full-access",
		SuggestedRule: "bash(go test)",
		AllowAlways:   true,
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.True(t, m.permissionInput)
	require.Empty(t, m.textarea.Value())
	require.Contains(t, m.View(), "Add next-step guidance")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("run focused tests next")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Equal(t, []PermissionResponse{{Decision: "allow_once", Feedback: "run focused tests next"}}, responses)
	require.False(t, m.awaitingPermission)
	require.Equal(t, "queued draft", m.textarea.Value())
	require.Equal(t, "Ask codog...", m.textarea.Placeholder)
}

func TestPermissionRejectFeedbackAndEditableAlwaysRule(t *testing.T) {
	responses := []PermissionResponse{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.permissionRespond = func(response PermissionResponse) { responses = append(responses, response) }
	m.openPermissionRequest(PermissionRequest{Tool: "bash", SuggestedRule: "bash(go test)", AllowAlways: true})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.Equal(t, "n", m.permissionInputAnswer)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("use the read tool instead")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, PermissionResponse{Decision: "deny", Feedback: "use the read tool instead"}, responses[0])

	m.openPermissionRequest(PermissionRequest{Tool: "bash", SuggestedRule: "bash(go test)", AllowAlways: true})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.Equal(t, "a", m.permissionInputAnswer)
	require.Equal(t, "bash(go test)", m.textarea.Value())
	m.textarea.SetValue("go test:*")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, PermissionResponse{Decision: "allow_always", Rule: "go test:*"}, responses[1])
}

func TestPermissionInputCanCollapseWithoutAnswer(t *testing.T) {
	responses := []PermissionResponse{}
	m := newModel(context.Background(), newPromptTextarea("draft"), nil, nil)
	m.permissionRespond = func(response PermissionResponse) { responses = append(responses, response) }
	m.openPermissionRequest(PermissionRequest{Tool: "write_file"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.awaitingPermission)
	require.False(t, m.permissionInput)
	require.Empty(t, responses)
	require.Equal(t, "draft", m.textarea.Value())

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Equal(t, []PermissionResponse{{Decision: "deny"}}, responses)
}

func TestPastedPermissionFeedbackOnlyEntersActiveInput(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("draft"), nil, nil)
	m.permissionRespond = func(PermissionResponse) {}
	m.openPermissionRequest(PermissionRequest{Tool: "bash"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ignored"), Paste: true})
	m = updated.(model)
	require.Equal(t, "draft", m.textarea.Value())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted guidance"), Paste: true})
	m = updated.(model)
	require.Equal(t, "pasted guidance", m.textarea.Value())
}

func TestQuestionStreamEntryNavigatesAndAcceptsChoice(t *testing.T) {
	ta := newPromptTextarea("")
	answers := []string{}
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) {
		answers = append(answers, answer)
	}

	request := &QuestionRequest{Question: "Pick a TUI lane", Choices: []string{"alpha", "beta"}, Default: "alpha"}
	updated, _ := m.Update(turnStreamMsg{Role: "question", Delta: "Pick a TUI lane\n  1. alpha\n  2. beta", Question: request})
	m = updated.(model)
	require.True(t, m.awaitingQuestion)
	require.Equal(t, "question", m.status)
	require.Contains(t, m.View(), "1. alpha (default)")
	require.Contains(t, m.View(), "Type something")
	require.Contains(t, statusBarText(m.status, 80), "Up/Down choose")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.awaitingQuestion)
	require.Equal(t, []string{"beta"}, answers)
	require.Equal(t, "question answered", m.status)
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "user", m.transcript[len(m.transcript)-1].Role)
	require.Equal(t, "beta", m.transcript[len(m.transcript)-1].Text)
}

func TestQuestionRequestSupportsNumberShortcutAndCustomInput(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Question: "Pick", Choices: []string{"alpha", "beta"}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = updated.(model)
	require.Equal(t, 1, m.questionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, []string{"beta"}, answers)

	m.openQuestionRequest(QuestionRequest{Question: "Explain", Choices: []string{"short", "long"}})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.questionCustom)
	require.Contains(t, m.View(), "Type your response below")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom answer")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, []string{"beta", "custom answer"}, answers)
}

func TestQuestionRequestWithoutChoicesUsesDefaultAndCancel(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Question: "Continue?", Default: "yes"})

	require.True(t, m.questionCustom)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, []string{""}, answers)
	require.Equal(t, "yes", m.transcript[len(m.transcript)-1].Text)

	m.openQuestionRequest(QuestionRequest{Question: "Cancel me", Choices: []string{"one"}})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)
	require.Equal(t, "interrupting", m.status)
}

func TestModernQuestionRequestSupportsTabsMultiSelectAndReview(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Questions: []Question{
		{
			Question: "Pick a lane?",
			Header:   "Lane",
			Options: []QuestionOption{
				{Label: "Alpha", Description: "Stable", Preview: "alpha preview"},
				{Label: "Beta", Description: "Fast"},
			},
		},
		{
			Question:    "Enable features?",
			Header:      "Features",
			MultiSelect: true,
			Options: []QuestionOption{
				{Label: "Cache", Description: "Reuse results"},
				{Label: "Trace", Description: "Record spans"},
			},
		},
	}})

	require.Contains(t, m.View(), "[ ] Lane")
	require.Contains(t, m.View(), "Stable")
	require.Contains(t, m.View(), "alpha preview")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, 1, m.questionIndex)
	require.Contains(t, m.View(), "Select one or more")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, 2, m.questionIndex)
	require.Contains(t, m.View(), "Review answers")
	require.Contains(t, m.View(), "Lane: Beta")
	require.Contains(t, m.View(), "Features: Cache, Trace")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)
	require.Len(t, answers, 1)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(answers[0]), &decoded))
	require.Equal(t, map[string]string{"Pick a lane?": "Beta", "Enable features?": "Cache, Trace"}, decoded)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "Lane: Beta")
}

func TestModernQuestionReviewReturnsToFirstUnansweredTab(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(string) {}
	m.openQuestionRequest(QuestionRequest{Questions: []Question{
		{Question: "First?", Header: "First", Options: []QuestionOption{{Label: "A"}, {Label: "B"}}},
		{Question: "Second?", Header: "Second", Options: []QuestionOption{{Label: "C"}, {Label: "D"}}},
	}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	require.Equal(t, 2, m.questionIndex)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Equal(t, 0, m.questionIndex)
	require.Equal(t, "answer required", m.status)
}

func TestModernQuestionNavigationAndReviewControls(t *testing.T) {
	newInteraction := func() model {
		m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
		m.busy = true
		m.questionAnswer = func(string) {}
		m.openQuestionRequest(QuestionRequest{Questions: []Question{
			{Question: "First?", Header: "First", Options: []QuestionOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Second?", Header: "Second", Options: []QuestionOption{{Label: "C"}, {Label: "D"}}},
		}})
		return m
	}
	m := newInteraction()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	require.Equal(t, 1, m.questionIndex)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	require.Equal(t, 0, m.questionIndex)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	require.Equal(t, 2, m.questionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(model)
	require.Equal(t, 0, m.questionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Equal(t, 2, m.questionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.Equal(t, 0, m.questionSelected)

	m.questionIndex = 2
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	require.Equal(t, 1, m.questionIndex)
	m.questionIndex = 2
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(model)
	require.Equal(t, 0, m.questionIndex)

	m = newInteraction()
	m.questionIndex = 2
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)
	require.Equal(t, "interrupting", m.status)
}

func TestModernMultiQuestionEnterSelectsAtLeastOneOption(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Questions: []Question{{
		Question:    "Features?",
		Header:      "Features",
		MultiSelect: true,
		Options:     []QuestionOption{{Label: "Cache"}, {Label: "Trace"}},
	}}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)
	require.Len(t, answers, 1)
	require.JSONEq(t, `{"Features?":"Cache"}`, answers[0])
}

func TestQuestionRequestCoversLegacyNavigationCustomCancelAndEmptyState(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	updated, _ := m.updateQuestionRequest(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)

	m.busy = true
	m.questionAnswer = func(string) {}
	m.openQuestionRequest(QuestionRequest{Question: "Legacy?", Choices: []string{"A", "B"}})
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	require.Equal(t, 1, m.questionSelected)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	require.Equal(t, 0, m.questionSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	m = updated.(model)
	require.True(t, m.questionCustom)
	require.Equal(t, "custom", m.textarea.Value())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.awaitingQuestion)
	require.Equal(t, "interrupting", m.status)
}

func TestModernSingleQuestionSubmitsCustomAnswer(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Questions: []Question{{
		Question: "Approach?",
		Header:   "Approach",
		Options:  []QuestionOption{{Label: "A"}, {Label: "B"}},
	}}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Custom")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.False(t, m.awaitingQuestion)
	require.Len(t, answers, 1)
	require.JSONEq(t, `{"Approach?":"Custom"}`, answers[0])
}

func TestPastedQuestionTextBecomesCustomAnswer(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Questions: []Question{{
		Question: "Approach?",
		Header:   "Approach",
		Options:  []QuestionOption{{Label: "A"}, {Label: "B"}},
	}}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted custom"), Paste: true})
	m = updated.(model)
	require.True(t, m.questionCustom)
	require.Equal(t, "pasted custom", m.textarea.Value())
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.JSONEq(t, `{"Approach?":"pasted custom"}`, answers[0])
}

func TestInteractionRequestsFitNarrowNoColorTerminal(t *testing.T) {
	styles := stylesForTheme("no-color")
	views := []string{
		renderPermissionRequest(PermissionRequest{
			Tool:        "bash",
			Required:    "danger-full-access",
			Input:       `{"command":"printf a-very-long-command-that-must-be-truncated"}`,
			AllowAlways: true,
		}, 1, false, "", 32, styles),
		renderQuestionRequest(QuestionRequest{
			Question: "Choose a very long option without overflowing the terminal",
			Choices:  []string{"a very long first choice", "second"},
		}, 0, 0, false, nil, nil, 32, styles),
	}
	for _, view := range views {
		require.NotContains(t, view, "\x1b[")
		for _, line := range strings.Split(view, "\n") {
			require.LessOrEqual(t, lipgloss.Width(line), 24, line)
		}
	}
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
	require.Contains(t, next.View(), "Common workflows")
	require.Contains(t, next.View(), "/status")
	help := helpPanel([]string{"/status"}, 100)
	require.Contains(t, help, "Keys")
	require.Contains(t, help, "Shift+Enter")
	require.Contains(t, help, "Alt+Enter")
	require.Contains(t, help, "\\+Enter")
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

	updated, _ = next.Update(teaKey("ctrl+_"))
	next = updated.(model)
	require.Equal(t, "draft", next.textarea.Value())
	require.Equal(t, "undo", next.status)
}

func TestExternalEditorChordUpdatesComposer(t *testing.T) {
	ta := newPromptTextarea("draft")
	m := newModel(context.Background(), ta, nil, nil)
	m.externalEditor = func(_ context.Context, value string) (string, error) {
		require.Equal(t, "draft", value)
		return "edited by chord", nil
	}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	next := updated.(model)
	require.Nil(t, cmd)
	require.True(t, next.ctrlXChord)
	require.Equal(t, "ctrl+x", next.status)

	updated, cmd = next.Update(teaKey("ctrl+e"))
	next = updated.(model)
	require.Equal(t, "editing", next.status)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.Equal(t, "edited by chord", next.textarea.Value())
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

func TestComposerUndoRestoresTypingPasteAndCompletion(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, []string{"/model claude-test"}, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	next := updated.(model)
	require.Equal(t, "abc", next.textarea.Value())

	updated, _ = next.Update(teaKey("ctrl+_"))
	next = updated.(model)
	require.Equal(t, "", next.textarea.Value())
	require.Equal(t, "undo", next.status)

	updated, _ = next.insertPastedText("prefix ")
	next = updated.(model)
	require.Equal(t, "prefix ", next.textarea.Value())
	updated, _ = next.Update(teaKey("ctrl+shift+-"))
	next = updated.(model)
	require.Equal(t, "", next.textarea.Value())

	next.textarea.SetValue("/mo")
	next.refreshCompletionMenu()
	updated, _ = next.Update(teaKey("tab"))
	next = updated.(model)
	require.Equal(t, "/model claude-test ", next.textarea.Value())
	updated, _ = next.Update(teaKey("ctrl+_"))
	next = updated.(model)
	require.Equal(t, "/mo", next.textarea.Value())
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
	require.True(t, m.attachmentsOpen)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "Pending attachments: 1")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.False(t, m.attachmentsOpen)

	m.textarea.SetValue("/attach clear")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Empty(t, m.attachments)
	require.Equal(t, "attachments cleared", m.status)
}

func TestAttachmentSelectorNavigatesRemovesAndCloses(t *testing.T) {
	ta := newPromptTextarea("/attachments")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"one.txt", "two.txt", "three.txt"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.True(t, m.attachmentsOpen)
	require.Equal(t, 0, m.attachmentSelected)
	require.Contains(t, m.View(), "attachments 1/3")
	require.Contains(t, m.View(), "> 1. one.txt")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	require.Equal(t, 1, m.attachmentSelected)
	require.Contains(t, m.View(), "attachments 2/3")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(model)
	require.True(t, m.attachmentsOpen)
	require.Equal(t, []string{"one.txt", "three.txt"}, m.attachments)
	require.Equal(t, 1, m.attachmentSelected)
	require.Equal(t, "attachment removed", m.status)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "Removed attachment: two.txt")
	require.Contains(t, m.View(), "attachments 2/2")
	require.Contains(t, m.View(), "> 2. three.txt")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	require.Equal(t, 0, m.attachmentSelected)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	require.False(t, m.attachmentsOpen)
	require.Contains(t, m.View(), "attachments: 2")
}

func TestPreviewWithAttachmentNavigation(t *testing.T) {
	preview := PreviewWithAttachmentNavigation("describe", []string{"one.txt", "two.txt", "three.txt"}, []string{"right", "delete"}, 96, 24)

	require.True(t, preview.AttachmentsOpen)
	require.Equal(t, []string{"one.txt", "three.txt"}, preview.Attachments)
	require.Contains(t, preview.View, "attachments 2/2")
	require.Contains(t, preview.View, "three.txt")

	closed := PreviewWithAttachmentNavigation("describe", []string{"one.txt"}, []string{"esc"}, 96, 24)
	require.False(t, closed.AttachmentsOpen)
	require.Equal(t, []string{"one.txt"}, closed.Attachments)
	require.Contains(t, closed.View, "attachments: 1")
}

func TestPreviewWithDiffDialogNavigatesFilesSourcesAndDetails(t *testing.T) {
	sources := []DiffSource{
		{
			Name:     "Uncommitted changes",
			Subtitle: "git diff HEAD",
			Files: []DiffFile{
				{Path: "main.go", Status: "modified", Summary: "+2 -1", Diff: "@@ main.go\n-old\n+new"},
				{Path: "README.md", Status: "added", Summary: "+5", Diff: "new docs"},
			},
		},
		{
			Name:     "Turn 2",
			Subtitle: "write tests",
			Files: []DiffFile{
				{Path: "main_test.go", Status: "modified", Summary: "+10", Diff: "@@ tests\n+assert"},
			},
		},
	}

	preview := PreviewWithDiffDialog(sources, []string{"down", "enter"}, 96, 24)
	require.True(t, preview.DiffDialog)
	require.Equal(t, "diff detail", preview.Mode)
	require.Contains(t, preview.View, "diff 1/2: Uncommitted changes")
	require.Contains(t, preview.View, "ADDED README.md")
	require.Contains(t, preview.View, "new docs")

	source := PreviewWithDiffDialog(sources, []string{"right"}, 96, 24)
	require.True(t, source.DiffDialog)
	require.Equal(t, "diff", source.Mode)
	require.Contains(t, source.View, "diff 2/2: Turn 2")
	require.Contains(t, source.View, "main_test.go")

	closed := PreviewWithDiffDialog(sources, []string{"esc"}, 96, 24)
	require.False(t, closed.DiffDialog)
}

func TestCtrlXBackspaceRemovesLastAttachment(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"one.txt", "two.txt"}

	updated, cmd := m.Update(teaKey("ctrl+x"))
	m = updated.(model)
	require.Nil(t, cmd)
	require.True(t, m.ctrlXChord)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(model)
	require.Nil(t, cmd)
	require.False(t, m.ctrlXChord)
	require.Equal(t, []string{"one.txt"}, m.attachments)
	require.Equal(t, "attachment removed", m.status)
	require.Contains(t, m.transcript[len(m.transcript)-1].Text, "Removed attachment: two.txt")
	require.Contains(t, m.View(), "attachments: 1")
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

func TestCtrlTShowsTodos(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.todos = func(context.Context) ([]TodoItem, error) {
		return []TodoItem{
			{ID: "todo-1", Content: "write tests", ActiveForm: "writing tests", Status: "in_progress", Priority: "high"},
			{ID: "todo-2", Content: "run smoke", Status: "pending", Priority: "medium"},
			{ID: "todo-3", Content: "push main", Status: "completed", Priority: "low"},
		}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	next := updated.(model)
	require.True(t, next.todosOpen)
	require.True(t, next.todosLoading)
	require.Equal(t, "loading todos", next.status)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.True(t, next.todosOpen)
	require.False(t, next.todosLoading)
	require.Equal(t, "todos 3", next.status)
	require.Contains(t, next.View(), "tasks: 3 total, 1 done, 1 active, 1 open")
	require.Contains(t, next.View(), "[~] todo-1 writing tests high")
	require.Contains(t, next.View(), "[ ] todo-2 run smoke medium")
	require.Contains(t, next.View(), "[x] todo-3 push main low")

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	next = updated.(model)
	require.False(t, next.todosOpen)
}

func TestCtrlTShowsTodoErrors(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.todos = func(context.Context) ([]TodoItem, error) {
		return nil, errors.New("todos failed")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	next := updated.(model)
	require.NotNil(t, cmd)

	updated, _ = next.Update(cmd())
	next = updated.(model)
	require.True(t, next.todosOpen)
	require.Equal(t, "todos error", next.status)
	require.Contains(t, next.View(), "error: todos failed")
}

func TestPreviewWithTodos(t *testing.T) {
	preview := PreviewWithTodos("", []TodoItem{
		{ID: "todo-1", Content: "review implementation", Status: "in_progress", Priority: "high"},
	}, 96, 24)

	require.True(t, preview.TodosOpen)
	require.Contains(t, preview.View, "tasks: 1 total")
	require.Contains(t, preview.View, "review implementation")
}

func TestCtrlShiftTShowsTaskBoard(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.taskBoard = func(context.Context) (string, error) {
		return "Background tasks\n  Active   1", nil
	}

	updated, cmd := m.Update(teaKey("ctrl+shift+t"))
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

func TestCtrlShiftTRendersTaskBoardErrors(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.taskBoard = func(context.Context) (string, error) {
		return "", errors.New("task board failed")
	}

	updated, cmd := m.Update(teaKey("ctrl+shift+t"))
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
	require.Equal(t, "deleted before cursor", next.status)

	next.textarea.SetValue("first second")
	next.textarea.CursorStart()
	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	next = updated.(model)
	require.Empty(t, next.textarea.Value())
	require.Equal(t, "deleted after cursor", next.status)
}

func TestCtrlUAndCtrlKPreserveCurrentLineSideAndUndo(t *testing.T) {
	ta := newPromptTextarea("first second")
	ta.SetCursor(len("first"))
	m := newModel(context.Background(), ta, nil, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = updated.(model)
	require.Equal(t, " second", m.textarea.Value())
	require.Equal(t, "deleted before cursor", m.status)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ctrl+_")})
	m = updated.(model)
	require.Equal(t, "first second", m.textarea.Value())
	require.Equal(t, "undo", m.status)

	m.textarea.SetValue("first second")
	m.textarea.SetCursor(len("first"))
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(model)
	require.Equal(t, "first", m.textarea.Value())
	require.Equal(t, "deleted after cursor", m.status)
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
	require.Empty(t, next.textarea.Value())
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

	require.Contains(t, view, "provider returned an")
	require.Contains(t, view, "empty error body")
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
