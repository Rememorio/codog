package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

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

func TestCtrlDRequiresDoublePressWhenComposerIsEmpty(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next := updated.(model)

	require.NotNil(t, cmd)
	require.True(t, next.exitPending)
	require.Equal(t, "ctrl+d", next.exitKey)
	require.Contains(t, next.View(), "Ctrl+D again to exit")

	updated, cmd = next.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next = updated.(model)
	require.NotNil(t, cmd)
	_, ok := cmd().(tea.QuitMsg)
	require.True(t, ok)
	require.False(t, next.result.Submitted)
}

func TestCtrlDPreservesAttachmentOnlyComposer(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, nil)
	m.attachments = []string{"screenshot.png"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	next := updated.(model)

	require.False(t, next.exitPending)
	require.Equal(t, []string{"screenshot.png"}, next.attachments)
	if cmd != nil {
		_, quit := cmd().(tea.QuitMsg)
		require.False(t, quit)
	}
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
