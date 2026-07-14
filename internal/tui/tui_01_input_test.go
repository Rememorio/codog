package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

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
	cmd := runSlashCommand(context.Background(), func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true}, nil
	}, "/clear")
	msg := cmd().(turnDoneMsg)

	require.Equal(t, "Done.", msg.Output)
	require.NoError(t, msg.Err)
}

func TestSlashQueryStartsARegularModelTurn(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/plan inspect the release"), nil, nil)
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, Output: "Enabled plan mode.", Query: "inspect the release"}, nil
	}
	var submitted string
	m.submit = func(_ context.Context, prompt string) (string, error) {
		submitted = prompt
		return "plan ready", nil
	}

	updated, slashCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, submitCmd := m.Update(slashCmd())
	m = updated.(model)
	require.NotNil(t, submitCmd)
	require.Equal(t, "inspect the release", strings.TrimSpace(m.transcript[len(m.transcript)-1].Text))
	for _, entry := range m.transcript {
		require.NotEqual(t, "/plan inspect the release", strings.TrimSpace(entry.Text))
	}

	_ = submitCmd()
	require.Equal(t, "inspect the release", submitted)
}

func TestSlashSessionStateReplacesTranscriptHistoryAndCandidates(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/resume target"), []string{"/resume old"}, []transcriptEntry{{Role: "assistant", Text: "old answer"}})
	m.setHistory([]string{"old prompt"})
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		require.Equal(t, "/resume target", line)
		return SlashResult{
			Output:  "session resumed: target",
			Handled: true,
			Session: &SessionState{
				ID: "target",
				Entries: []Entry{
					{Role: "system", Text: "Session target"},
					{Role: "user", Text: "restored prompt"},
					{Role: "assistant", Text: "restored answer"},
				},
				History:    []string{"restored prompt"},
				Candidates: []string{"/resume target", "/status"},
			},
		}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.False(t, m.busy)
	require.Equal(t, []string{"restored prompt"}, m.history)
	require.Equal(t, []string{"/resume target", "/status"}, m.candidates)
	require.Contains(t, m.View(), "restored prompt")
	require.Contains(t, m.View(), "restored answer")
	require.Contains(t, m.View(), "session resumed: target")
	require.NotContains(t, m.View(), "old answer")
}

func TestBareResumeOpensEmbeddedPickerAndResumesSelection(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/resume"), nil, nil)
	calls := []string{}
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		calls = append(calls, line)
		if line == "/resume" {
			return SlashResult{Handled: true, SessionChoices: []SessionChoice{{ID: "target", Title: "Target session", MessageCount: 2}}}, nil
		}
		return SlashResult{
			Handled: true,
			Session: &SessionState{
				ID:      "target",
				Entries: []Entry{{Role: "system", Text: "Session target"}, {Role: "user", Text: "target prompt"}},
			},
		}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, m.sessionPicker)
	require.Contains(t, m.View(), "Resume a session")
	require.Contains(t, m.View(), "Target session")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Nil(t, m.sessionPicker)
	require.True(t, m.busy)

	done := cmd()
	require.Equal(t, []string{"/resume", "/resume target"}, calls)
	updated, _ = m.Update(done)
	m = updated.(model)
	require.Contains(t, m.View(), "target prompt")
}

func TestSlashInteractiveViewsOpenWithoutTranscriptNoise(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		result SlashResult
		assert func(*testing.T, model)
	}{
		{
			name:   "model",
			input:  "/model",
			result: SlashResult{Handled: true, OpenModelPicker: true},
			assert: func(t *testing.T, m model) {
				require.True(t, m.modelPicker)
				require.Contains(t, m.View(), "model picker")
			},
		},
		{
			name:  "diff",
			input: "/diff",
			result: SlashResult{Handled: true, Diff: &DiffView{Sources: []DiffSource{{
				Name:  "Unstaged changes",
				Files: []DiffFile{{Path: "main.go", Status: "modified", Summary: "+1 -1", Diff: "-old\n+new"}},
			}}}},
			assert: func(t *testing.T, m model) {
				require.True(t, m.diffDialog)
				require.Contains(t, m.View(), "main.go")
			},
		},
		{
			name:   "information",
			input:  "/context",
			result: SlashResult{Handled: true, Information: &InformationView{Title: "Context", Lines: []string{"Model glm52", "Tokens 42"}}},
			assert: func(t *testing.T, m model) {
				require.NotNil(t, m.information)
				require.Contains(t, m.View(), "Model glm52")
			},
		},
		{
			name:  "export",
			input: "/export",
			result: SlashResult{Handled: true, ExportDialog: &ExportDialog{
				DefaultFilename: "conversation.md",
			}},
			assert: func(t *testing.T, m model) {
				require.NotNil(t, m.exportDialog)
				require.Contains(t, m.View(), "Copy to clipboard")
				require.Contains(t, m.View(), "Save to file")
			},
		},
		{
			name:  "text input",
			input: "/add-dir",
			result: SlashResult{Handled: true, TextInputDialog: &TextInputDialog{
				Title:  "Add working directory",
				Prompt: "Enter a directory path:",
				Action: "add-dir",
			}},
			assert: func(t *testing.T, m model) {
				require.NotNil(t, m.textInputDialog)
				require.Contains(t, m.View(), "Enter a directory path")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newModel(context.Background(), newPromptTextarea(test.input), nil, nil)
			m.modelOptions = []string{"glm52", "sonnet"}
			m.selectModel = func(context.Context, string) (RuntimeControlResult, error) {
				return RuntimeControlResult{}, nil
			}
			m.slash = func(context.Context, string) (SlashResult, error) {
				return test.result, nil
			}

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(model)
			require.NotNil(t, cmd)
			updated, _ = m.Update(cmd())
			m = updated.(model)

			test.assert(t, m)
			require.NotContains(t, m.View(), test.input)
		})
	}
}

func TestTextInputDialogSubmitsExactValue(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/add-dir"), nil, nil)
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, TextInputDialog: &TextInputDialog{
			Title:  "Add working directory",
			Prompt: "Enter a directory path:",
			Action: "add-dir",
		}}, nil
	}
	action := ""
	value := ""
	m.submitTextInput = func(_ context.Context, submittedAction string, submittedValue string) (RuntimeControlResult, error) {
		action = submittedAction
		value = submittedValue
		return RuntimeControlResult{Title: "Working Directory Added", Status: "directory added", Lines: []string{submittedValue}}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, m.textInputDialog)

	path := "../shared workspace"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path)})
	m = updated.(model)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Nil(t, m.textInputDialog)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "add-dir", action)
	require.Equal(t, path, value)
	require.Contains(t, m.View(), "Working Directory Added")
}

func TestCommandViewHonorsInitialItemSelection(t *testing.T) {
	preview := PreviewWithCommandView(CommandView{
		Title:        "Fast mode",
		SelectedItem: 1,
		Tabs: []CommandViewTab{{Title: "Preference", Items: []CommandViewItem{
			{Label: "Enabled"},
			{Label: "Disabled", Value: "current"},
		}}},
	}, nil, 80, 24)

	require.True(t, preview.CommandView)
	require.Contains(t, preview.View, "> Disabled  current")
}

func TestExportDialogSavesEnteredFilename(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/export"), nil, nil)
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, ExportDialog: &ExportDialog{DefaultFilename: "session.md"}}, nil
	}
	exported := ""
	m.exportConversationTo = func(_ context.Context, filename string) (RuntimeControlResult, error) {
		exported = filename
		return RuntimeControlResult{
			Title:  "Conversation Exported",
			Status: "exported",
			Lines:  []string{"File: " + filename},
		}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, m.exportDialog)
	require.NotContains(t, m.View(), "/export")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.exportFilenameInput)
	require.Equal(t, "session.md", m.textarea.Value())
	require.Contains(t, m.View(), "Enter filename")

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Nil(t, m.exportDialog)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "session.md", exported)
	require.Equal(t, "exported", m.status)
	require.Contains(t, m.View(), "Conversation Exported")
	require.Contains(t, m.View(), "File: session.md")
}

func TestSlashRuntimeActionUsesConversationControl(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/compact"), nil, nil)
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, RuntimeAction: "compact"}, nil
	}
	m.compactSession = func(context.Context) (RuntimeControlResult, error) {
		return RuntimeControlResult{Title: "Session Compacted", Status: "compacted", Lines: []string{"Removed: 2"}}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, cmd = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "compacted", m.status)
	require.Contains(t, m.View(), "Session Compacted")
	require.NotContains(t, m.View(), "/compact")
}

func TestSlashCommandViewOpensTabsAndActionsWithoutTranscriptNoise(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/config"), nil, nil)
	m.modelOptions = []string{"glm52", "sonnet"}
	m.selectModel = func(context.Context, string) (RuntimeControlResult, error) {
		return RuntimeControlResult{}, nil
	}
	m.slash = func(context.Context, string) (SlashResult, error) {
		view := CommandView{
			Title:       "Settings",
			SelectedTab: 1,
			Tabs: []CommandViewTab{
				{Title: "Status", Lines: []string{"Workspace ready"}},
				{Title: "Config", Items: []CommandViewItem{{Label: "Model", Value: "glm52", Action: "model"}}},
				{Title: "Usage", Lines: []string{"Total tokens 42"}},
			},
		}
		return SlashResult{Handled: true, CommandView: &view}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, m.commandView)
	require.Contains(t, m.View(), "Config")
	require.Contains(t, m.View(), "Model")
	require.Contains(t, m.View(), "glm52")
	require.NotContains(t, m.View(), "/config")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	require.Contains(t, m.View(), "Total tokens 42")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, m.commandView)
	require.True(t, m.modelPicker)
}

func TestCommandViewRuntimeActionUpdatesVimMode(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.toggleVim = func(context.Context) (RuntimeControlResult, error) {
		enabled := true
		return RuntimeControlResult{Title: "Vim Mode", Status: "vim on", Lines: []string{"Vim mode: on"}, Setting: "vim", Value: "on", VimEnabled: &enabled}, nil
	}
	m.openCommandView(CommandView{
		Title: "Settings",
		Tabs: []CommandViewTab{{
			Title: "Config",
			Items: []CommandViewItem{{Label: "Vim mode", Value: "off", Action: "vim"}},
		}},
	})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.True(t, m.vimEnabled)
	require.NotNil(t, m.commandView)
	require.Contains(t, m.View(), "Vim mode  on")
	for _, entry := range m.transcript {
		require.NotContains(t, entry.Text, "Vim mode: on")
	}
}

func TestCommandViewScrollsAndRunsSecondaryAction(t *testing.T) {
	items := make([]CommandViewItem, 20)
	for index := range items {
		name := fmt.Sprintf("skill-%02d", index)
		items[index] = CommandViewItem{
			Label:            name,
			Value:            "available",
			Command:          "/skills show " + name,
			SecondaryLabel:   "enable",
			SecondaryCommand: "/skills enable " + name,
		}
	}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.height = 14
	called := ""
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		called = line
		view := InformationView{Title: "Skills", Lines: []string{"Enabled skill-00"}}
		return SlashResult{Handled: true, Information: &view}, nil
	}
	m.openCommandView(CommandView{Title: "Extensions", Tabs: []CommandViewTab{{Title: "Skills", Items: items}}})
	require.Contains(t, m.View(), "skill-00")
	require.NotContains(t, m.View(), "skill-19")

	for range 12 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	require.Greater(t, m.commandViewOffset, 0)
	require.Contains(t, m.View(), "skill-12")
	require.Contains(t, m.View(), "/20")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	require.NotNil(t, cmd)
	require.Nil(t, m.commandView)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "/skills enable skill-00", called)
	require.NotNil(t, m.information)
	require.Contains(t, m.View(), "Enabled skill-00")
}

func TestCommandViewUsesCustomSecondaryKeyAndRefreshes(t *testing.T) {
	view := CommandView{Title: "Runtime", Tabs: []CommandViewTab{{
		Title:          "Tasks",
		RefreshCommand: "/tasks",
		Items: []CommandViewItem{{
			Label:            "sleep 30",
			Value:            "running",
			Command:          "/tasks status task-1",
			SecondaryLabel:   "stop",
			SecondaryCommand: "/tasks stop task-1",
			SecondaryKey:     "x",
		}},
	}}}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	calls := []string{}
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		calls = append(calls, line)
		return SlashResult{Handled: true, CommandView: &view}, nil
	}
	m.openCommandView(view)
	require.Contains(t, m.View(), "X stop")
	require.Contains(t, m.View(), "R refresh")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, []string{"/tasks stop task-1"}, calls)
	require.NotNil(t, m.commandView)

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.Equal(t, []string{"/tasks stop task-1", "/tasks"}, calls)
	require.NotNil(t, m.commandView)
}

func TestCommandViewPrefillsComposerForPrimaryAndSecondaryActions(t *testing.T) {
	view := CommandView{Title: "Extensions", Tabs: []CommandViewTab{{
		Title: "Agents",
		Items: []CommandViewItem{
			{Label: "Create new agent", Action: "prefill", Command: "/agents create "},
			{
				Label:            "reviewer",
				Command:          "/agents show reviewer",
				SecondaryLabel:   "run",
				SecondaryAction:  "prefill",
				SecondaryCommand: "/agents run reviewer ",
			},
		},
	}}}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.openCommandView(view)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Nil(t, m.commandView)
	require.Equal(t, "/agents create ", m.textarea.Value())

	m.textarea.SetValue("")
	m.openCommandView(view)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Nil(t, m.commandView)
	require.Equal(t, "/agents run reviewer ", m.textarea.Value())
}

func TestSlashResultOpensMessageActionsForLatestUserTurn(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, []transcriptEntry{
		{Role: "user", Text: "first prompt"},
		{Role: "assistant", Text: "first response"},
		{Role: "user", Text: "/rewind"},
	})

	updated, _ := m.Update(turnDoneMsg{OpenMessageActions: true})
	m = updated.(model)

	require.True(t, m.messageActions)
	require.Equal(t, "first prompt", m.transcript[m.messageActionTarget].Text)
	for _, entry := range m.transcript {
		require.NotEqual(t, "/rewind", entry.Text)
	}
	require.Contains(t, m.View(), "restore before turn")
}

func TestSlashPermissionsSelectsSessionMode(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/permissions"), nil, nil)
	selected := ""
	modeLabel := "default"
	m.readModeLabel = func() string { return modeLabel }
	m.selectPermissionMode = func(_ context.Context, mode string) (RuntimeControlResult, error) {
		selected = mode
		modeLabel = mode
		return RuntimeControlResult{Title: "Permissions", Status: "permissions updated", Lines: []string{"Mode: " + mode}}, nil
	}
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, PermissionSettings: &PermissionSettings{
			Modes: []PermissionModeOption{
				{Name: "default", Label: "default", Current: true},
				{Name: "accept edits", Label: "accept edits", Description: "Apply workspace edits"},
			},
			Allow: []string{"read_file"},
		}}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(cmd())
	m = updated.(model)
	require.NotNil(t, m.permissionSettings)
	require.Contains(t, m.View(), "read_file")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Equal(t, "accept edits", selected)
	require.Equal(t, "accept edits", m.modeLabel)
	require.Nil(t, m.permissionSettings)
	require.Contains(t, m.View(), "permissions updated")
}

func TestRuntimeControlDoesNotResetTemporaryPermissionMode(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.modeLabel = "accept edits"
	m.readModeLabel = func() string { return "default" }

	updated, _ := m.Update(runtimeControlDoneMsg{Result: RuntimeControlResult{Status: "fast mode on"}})
	m = updated.(model)

	require.Equal(t, "accept edits", m.modeLabel)
}

func TestSlashTodosLoadsInteractivePanel(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("/todos"), nil, nil)
	m.todos = func(context.Context) ([]TodoItem, error) {
		return []TodoItem{{ID: "1", Content: "Close the loop", Status: "in_progress"}}, nil
	}
	m.slash = func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true, OpenTodos: true}, nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	updated, cmd = m.Update(cmd())
	m = updated.(model)
	require.True(t, m.todosOpen)
	require.NotNil(t, cmd)
	updated, _ = m.Update(cmd())
	m = updated.(model)

	require.Contains(t, m.View(), "Close the loop")
	for _, entry := range m.transcript {
		require.NotEqual(t, "/todos", strings.TrimSpace(entry.Text))
	}
}

func TestInformationViewScrollsAndCloses(t *testing.T) {
	lines := make([]string, 20)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %02d", index+1)
	}
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.height = 12
	m.openInformation(InformationView{Title: "Context", Lines: lines})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(model)
	require.Greater(t, m.informationOffset, 0)
	require.Contains(t, m.View(), "/20")

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	require.Nil(t, m.information)
}

func TestInformationViewCanDismissOnConfirm(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeySpace}} {
		m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
		m.openInformation(InformationView{Title: "/btw", Lines: []string{"Question", "Answer"}, DismissOnConfirm: true})
		require.Contains(t, m.View(), "Enter/Space/Esc close")

		updated, _ := m.Update(key)
		m = updated.(model)
		require.Nil(t, m.information)
	}
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
	transcriptCount := len(m.transcript)

	m.textarea.SetValue("second prompt")
	updated, queueCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"second prompt"}, queuedPromptTexts(m.queuedPrompts))
	require.Equal(t, "", m.textarea.Value())
	require.Equal(t, "queued", m.status)
	require.Len(t, m.transcript, transcriptCount)
	require.Contains(t, m.View(), "queued prompts: 1")
	require.Contains(t, m.View(), "1 queued")

	m.textarea.SetValue("third prompt")
	updated, queueCmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"second prompt", "third prompt"}, queuedPromptTexts(m.queuedPrompts))
	require.Len(t, m.transcript, transcriptCount)
	require.Contains(t, m.View(), "queued prompts: 2")
	require.Contains(t, m.View(), "2 queued")

	firstDone := firstCmd().(turnDoneMsg)
	updated, secondCmd := m.Update(firstDone)
	m = updated.(model)
	require.True(t, m.busy)
	require.Equal(t, []string{"third prompt"}, queuedPromptTexts(m.queuedPrompts))
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
