package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

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
	require.NotContains(t, m.View(), "Restored 2 queued prompts")
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
	m.slash = func(_ context.Context, line string) (SlashResult, error) {
		slashLines = append(slashLines, line)
		return SlashResult{Output: "ran " + line, Handled: true}, nil
	}

	updated, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.True(t, m.busy)
	require.NotNil(t, firstCmd)

	m.textarea.SetValue("!printf codog")
	updated, queueCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, queueCmd)
	require.Equal(t, []string{"!printf codog"}, queuedPromptTexts(m.queuedPrompts))
	require.Contains(t, m.View(), "queued prompts: 1")
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
	require.Equal(t, []string{"second prompt", "third prompt"}, queuedPromptTexts(m.queuedPrompts))

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "second prompt\nthird prompt", m.textarea.Value())
	require.Equal(t, "editing queued prompts", m.status)
	require.NotContains(t, m.View(), "queued prompts:")

	m.textarea.SetValue("second prompt\nthird prompt edited")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Nil(t, cmd)
	require.Equal(t, []string{"second prompt\nthird prompt edited"}, queuedPromptTexts(m.queuedPrompts))
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
	m.queuedPrompts = testQueuedPrompts("queued one", "queued two")
	m.textarea.SetValue("draft")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)

	require.Nil(t, cmd)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "queued one\nqueued two\ndraft", m.textarea.Value())
	require.Equal(t, "editing queued prompts", m.status)
}

func TestUpMovesWithinMultilineDraftBeforeEditingQueue(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("first line\nsecond line"), nil, nil)
	m.busy = true
	m.queuedPrompts = testQueuedPrompts("queued one")
	m.textarea.CursorEnd()
	require.Equal(t, 1, m.textarea.Line())

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)

	require.NotNil(t, cmd)
	require.Equal(t, []string{"queued one"}, queuedPromptTexts(m.queuedPrompts))
	require.Equal(t, "first line\nsecond line", m.textarea.Value())
	require.Equal(t, 0, m.textarea.Line())
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
	m.attachments = []string{"queued.txt"}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	require.Len(t, m.queuedPrompts, 1)
	m.attachments = []string{"draft.txt"}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	done := firstCmd().(turnDoneMsg)
	updated, _ = m.Update(done)
	m = updated.(model)

	require.False(t, m.busy)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "queued prompt", m.textarea.Value())
	require.Equal(t, []string{"queued.txt", "draft.txt"}, m.attachments)
	require.Equal(t, "interrupted · 1 queued restored", m.status)
	require.NotContains(t, m.View(), "Restored 1 queued prompt")
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
		{name: "tool search", activity: ToolActivity{Name: "tool_search", Output: `{"match_names":["web_fetch","web_search"],"matches":[{}]}`}, want: []string{"Loaded web_fetch, web_search"}},
		{name: "tool search empty", activity: ToolActivity{Name: "tool_search", Output: `{"match_names":[]}`}, want: []string{"No matching tools"}},
		{name: "web fetch", activity: ToolActivity{Name: "web_fetch", Output: `{"status_code":200,"title":"Example","summary":"summarized body","text":"body"}`}, want: []string{"Title: Example"}},
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
	transcriptCount := len(m.transcript)
	updated, _ := m.Update(turnStreamMsg{Role: "permission", Delta: "Permission\n- bash requires danger-full-access", Permission: request})
	m = updated.(model)
	require.True(t, m.awaitingPermission)
	require.Len(t, m.transcript, transcriptCount)
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

	updated, _ = m.Update(turnStreamMsg{Role: "permission"})
	m = updated.(model)
	require.False(t, m.awaitingPermission)
	require.Equal(t, "permission answered", m.status)
	require.Len(t, m.transcript, transcriptCount)
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

func TestQuestionChoiceRestoresComposerDraft(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea("draft in progress"), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Question: "Pick", Choices: []string{"alpha", "beta"}})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Equal(t, []string{"beta"}, answers)
	require.False(t, m.awaitingQuestion)
	require.Equal(t, "draft in progress", m.textarea.Value())
}

func TestFreeformQuestionDoesNotConsumeComposerDraft(t *testing.T) {
	answers := []string{}
	m := newModel(context.Background(), newPromptTextarea("unrelated draft"), nil, nil)
	m.busy = true
	m.questionAnswer = func(answer string) { answers = append(answers, answer) }
	m.openQuestionRequest(QuestionRequest{Question: "Explain"})

	require.True(t, m.questionCustom)
	require.Empty(t, m.textarea.Value())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("focused answer")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	require.Equal(t, []string{"focused answer"}, answers)
	require.False(t, m.awaitingQuestion)
	require.Equal(t, "unrelated draft", m.textarea.Value())
}

func TestCancelingQuestionRestoresComposerDraft(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea("keep this draft"), nil, nil)
	m.busy = true
	m.turnCancel = func() {}
	m.questionAnswer = func(string) {}
	m.openQuestionRequest(QuestionRequest{Question: "Explain"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	require.False(t, m.awaitingQuestion)
	require.Equal(t, "keep this draft", m.textarea.Value())
	require.Equal(t, "interrupting", m.status)
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
	cmd := runSlashCommand(context.Background(), func(context.Context, string) (SlashResult, error) {
		return SlashResult{Handled: true}, context.Canceled
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

func TestSubmittingOnlyAttachmentsPassesThemToSubmitter(t *testing.T) {
	ta := newPromptTextarea("")
	m := newModel(context.Background(), ta, nil, nil)
	m.attachments = []string{"screenshot.png"}
	var gotPrompt string
	var gotAttachments []string
	m.submitAttachments = func(_ context.Context, prompt string, attachments []string) (string, error) {
		gotPrompt = prompt
		gotAttachments = append([]string(nil), attachments...)
		return "described", nil
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(model)
	require.True(t, next.busy)
	require.Empty(t, next.attachments)
	require.Equal(t, "Attachments:\n- screenshot.png", next.transcript[len(next.transcript)-1].Text)
	require.NotNil(t, cmd)

	done := cmd().(turnDoneMsg)
	require.Equal(t, "", gotPrompt)
	require.Equal(t, []string{"screenshot.png"}, gotAttachments)
	require.Equal(t, "described", done.Output)
}

func TestPendingAttachmentsQueueWhileBusy(t *testing.T) {
	ta := newPromptTextarea("queued with file")
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.attachments = []string{"notes.txt"}
	transcriptCount := len(m.transcript)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next := updated.(model)

	require.Nil(t, cmd)
	require.Equal(t, []string{"queued with file"}, queuedPromptTexts(next.queuedPrompts))
	require.Equal(t, []string{"notes.txt"}, next.queuedPrompts[0].Attachments)
	require.Empty(t, next.attachments)
	require.Equal(t, "queued", next.status)
	require.Len(t, next.transcript, transcriptCount)
	require.Contains(t, next.View(), "queued with file [1 attachment]")
}

func TestQueuedPromptExecutesWithAttachmentsAndPreservesDraft(t *testing.T) {
	ta := newPromptTextarea("queued with file")
	m := newModel(context.Background(), ta, nil, nil)
	m.busy = true
	m.attachments = []string{"queued.txt"}
	var gotPrompt string
	var gotAttachments []string
	m.submitAttachments = func(_ context.Context, prompt string, attachments []string) (string, error) {
		gotPrompt = prompt
		gotAttachments = append([]string(nil), attachments...)
		return "queued done", nil
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	m.textarea.SetValue("draft in progress")
	m.attachments = []string{"draft.txt"}

	updated, cmd := m.Update(turnDoneMsg{Role: "assistant", Output: "first done"})
	m = updated.(model)
	require.True(t, m.busy)
	require.Empty(t, m.queuedPrompts)
	require.Equal(t, "draft in progress", m.textarea.Value())
	require.Equal(t, []string{"draft.txt"}, m.attachments)
	require.NotNil(t, cmd)

	done := cmd().(turnDoneMsg)
	require.Equal(t, "queued with file", gotPrompt)
	require.Equal(t, []string{"queued.txt"}, gotAttachments)
	require.Equal(t, "queued done", done.Output)
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
