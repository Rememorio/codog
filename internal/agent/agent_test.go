package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/githubsetup"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/prompthistory"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/stretchr/testify/require"
)

type notifyBuffer struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes chan struct{}
}

func newNotifyBuffer() *notifyBuffer {
	return &notifyBuffer{writes: make(chan struct{}, 8)}
}

func (b *notifyBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case b.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (b *notifyBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTUIPermissionEventsWrapOriginalCallbacks(t *testing.T) {
	prompter := &tools.Prompter{}
	original := []string{}
	prompter.OnRequest = func(decision tools.PermissionDecision) {
		original = append(original, "request:"+decision.ToolName)
	}
	prompter.OnDecision = func(decision tools.PermissionDecision) {
		original = append(original, "decision:"+decision.ToolName)
	}
	entries := []tui.Entry{}
	wrapTUIPermissionEvents(prompter, func(entry tui.Entry) {
		entries = append(entries, entry)
	})

	request := tools.PermissionDecision{
		ToolName:    "bash",
		Required:    tools.PermissionDanger,
		Input:       `{"command":"go test ./..."}`,
		WouldPrompt: true,
		Message:     "writes outside workspace",
	}
	approved := request
	approved.Allowed = true
	approved.Reason = "user_approved"
	approved.Feedback = "run the focused package next"
	approved.Rule = "bash(go test:*)"

	prompter.OnRequest(request)
	prompter.OnDecision(approved)

	require.Equal(t, []string{"request:bash", "decision:bash"}, original)
	require.Len(t, entries, 2)
	require.Contains(t, entries[0].Text, "bash requires danger-full-access")
	require.Contains(t, entries[0].Text, "writes outside workspace")
	require.Equal(t, &tui.PermissionRequest{
		Tool:          "bash",
		Required:      "danger-full-access",
		Input:         `{"command":"go test ./..."}`,
		Message:       "writes outside workspace",
		SuggestedRule: "bash(go test)",
		AllowAlways:   true,
	}, entries[0].Permission)
	require.Empty(t, entries[1].Text)
	require.Equal(t, "permission", entries[1].Role)
	require.Nil(t, entries[1].Permission)
}

func TestTUIPermissionEventsDoNotRenderAutomaticDecisions(t *testing.T) {
	prompter := &tools.Prompter{}
	entries := []tui.Entry{}
	wrapTUIPermissionEvents(prompter, func(entry tui.Entry) { entries = append(entries, entry) })

	prompter.OnDecision(tools.PermissionDecision{
		ToolName: "read_file",
		Required: tools.PermissionReadOnly,
		Allowed:  true,
		Reason:   "permission_mode",
	})

	require.Empty(t, entries)
}

func TestTUIPermissionResponseEncodingAndBroadRuleSuppression(t *testing.T) {
	line := encodeTUIPermissionResponse(tui.PermissionResponse{
		Decision: "allow_always",
		Feedback: "continue with focused tests",
		Rule:     "go test:*",
	})
	require.True(t, strings.HasSuffix(line, "\n"))
	require.JSONEq(t, `{"decision":"allow_always","feedback":"continue with focused tests","rule":"go test:*"}`, strings.TrimSpace(line))

	prompter := &tools.Prompter{}
	entries := []tui.Entry{}
	wrapTUIPermissionEvents(prompter, func(entry tui.Entry) { entries = append(entries, entry) })
	prompter.OnRequest(tools.PermissionDecision{ToolName: "custom_tool", Required: tools.PermissionWorkspace, Input: `{}`})
	require.Len(t, entries, 1)
	require.Equal(t, "custom_tool", entries[0].Permission.SuggestedRule)
	require.False(t, entries[0].Permission.AllowAlways)
}

func TestRenderTUIQuestionRequest(t *testing.T) {
	text := renderTUIQuestionRequest(tools.UserQuestionRequest{
		Question: "Pick a lane",
		Choices:  []string{"alpha", "beta"},
		Default:  "beta",
	})

	require.Equal(t, "Pick a lane\n  1. alpha\n  2. beta\nDefault: beta", text)
}

func TestRenderModernTUIQuestionRequest(t *testing.T) {
	request := tools.UserQuestionRequest{Questions: []tools.UserQuestion{{
		Question: "Pick a lane?",
		Header:   "Lane",
		Options: []tools.UserQuestionOption{
			{Label: "Alpha", Description: "Stable", Preview: "preview"},
			{Label: "Beta", Description: "Fast"},
		},
	}}}

	text := renderTUIQuestionRequest(request)
	require.Contains(t, text, "Questions\n1. Pick a lane?")
	require.Contains(t, text, "1. Alpha - Stable")
	mapped := tuiQuestions(request.Questions)
	require.Equal(t, "preview", mapped[0].Options[0].Preview)
}

func TestTUIToolActivityMapsRunloopCall(t *testing.T) {
	activity := tuiToolActivity(runloop.ToolCall{
		ID:      "tool-1",
		Name:    "bash",
		Input:   `{"command":"printf ok"}`,
		Output:  `{"stdout":"ok"}`,
		IsError: true,
	}, "error")

	require.Equal(t, &tui.ToolActivity{
		ID:      "tool-1",
		Name:    "bash",
		Input:   `{"command":"printf ok"}`,
		Output:  `{"stdout":"ok"}`,
		Status:  "error",
		IsError: true,
	}, activity)
}

func TestPromptEmitsMultipleToolEventsInOneTurn(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Turns: []mockanthropic.Turn{
		{ToolUses: []mockanthropic.ToolUse{
			{ID: "tool-write", Name: "write_file", Input: json.RawMessage(`{"path":"multi-tool.txt","content":"created by first tool\n"}`)},
			{ID: "tool-bash", Name: "bash", Input: json.RawMessage(`{"command":"printf second-tool","timeout":1000}`)},
		}},
		{Text: "multi tool done"},
	}}.Handler())
	defer server.Close()

	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            2,
			AutoCompactMessages: 40,
			PermissionMode:      "allow",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	starts := []string{}
	finishes := []string{}
	err := app.promptWithOutputOptions(context.Background(), "run both tools", config.FlagOverrides{SessionID: "multi-tool-events"}, "text", false, turnOptions{
		OnToolStart: func(call runloop.ToolCall) {
			starts = append(starts, call.Name)
		},
		OnToolUse: func(call runloop.ToolCall) {
			finishes = append(finishes, call.Name)
		},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"write_file", "bash"}, starts)
	require.Equal(t, []string{"write_file", "bash"}, finishes)
	require.Contains(t, out.String(), "multi tool done")
	created, readErr := os.ReadFile(filepath.Join(workspace, "multi-tool.txt"))
	require.NoError(t, readErr)
	require.Equal(t, "created by first tool\n", string(created))
}

func TestLineAnswerReaderReturnsEOFWhenCanceled(t *testing.T) {
	done := make(chan struct{})
	close(done)
	reader := &lineAnswerReader{answers: make(chan string), done: done}
	buffer := make([]byte, 8)

	n, err := reader.Read(buffer)

	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)
}

func TestTUIModeStateCyclesPermissionModes(t *testing.T) {
	state := newTUIModeState(config.Config{PermissionMode: "read-only"})
	require.Equal(t, "read-only", state.Label())
	var cfg config.Config
	state.Apply(&cfg)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.False(t, cfg.PlanMode)

	require.Equal(t, "default", state.Cycle())
	state.Apply(&cfg)
	require.Equal(t, "prompt", cfg.PermissionMode)
	require.False(t, cfg.PlanMode)

	require.Equal(t, "accept edits", state.Cycle())
	state.Apply(&cfg)
	require.Equal(t, "workspace-write", cfg.PermissionMode)
	require.False(t, cfg.PlanMode)

	require.Equal(t, "plan", state.Cycle())
	state.Apply(&cfg)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.True(t, cfg.PlanMode)
}

func TestTUIModeStatePreservesBypassPermissionMode(t *testing.T) {
	state := newTUIModeState(config.Config{PermissionMode: "allow"})
	require.Equal(t, "bypass permissions", state.Label())
	var cfg config.Config
	state.Apply(&cfg)
	require.Equal(t, "allow", cfg.PermissionMode)
	require.False(t, cfg.PlanMode)
}

func TestTUIModeStateSyncsExternalPermissionChanges(t *testing.T) {
	state := newTUIModeState(config.Config{PermissionMode: "workspace-write"})
	require.Equal(t, "accept edits", state.Label())

	state.Sync(config.Config{PermissionMode: "read-only", PlanMode: true})
	require.Equal(t, "plan", state.Label())

	state.Sync(config.Config{PermissionMode: "allow"})
	require.Equal(t, "bypass permissions", state.Label())
}

func TestTUIRuntimeBadgesReflectConfig(t *testing.T) {
	fast := true
	app := &App{Config: config.Config{
		Model:           "glm52",
		FastMode:        &fast,
		ReasoningEffort: "high",
	}}

	require.Equal(t, []string{"model: glm52", "fast: on", "thinking: high"}, app.tuiRuntimeBadges())
}

func TestResumeSessionChoicesUseIdentityAndPromptFallback(t *testing.T) {
	updated := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	choices := resumeSessionChoices([]session.Session{
		{
			ID:       "named-session",
			Identity: session.SessionIdentity{Title: "Review auth flow", Tag: "security", Workspace: "/workspace/auth"},
			Metadata: session.SessionMetadata{UpdatedAt: updated, BranchName: "feature/auth"},
			Messages: []anthropic.Message{anthropic.TextMessage("user", "ignored fallback")},
		},
		{
			ID:       "fallback-session",
			Identity: session.SessionIdentity{Title: "fallback-session"},
			Metadata: session.SessionMetadata{ModifiedAt: updated.Add(-time.Hour)},
			Messages: []anthropic.Message{anthropic.TextMessage("user", "Investigate the scheduler race in the worker pool")},
		},
	})

	require.Len(t, choices, 2)
	require.Equal(t, "Review auth flow", choices[0].Title)
	require.Equal(t, "security", choices[0].Tag)
	require.Equal(t, "feature/auth", choices[0].BranchName)
	require.Equal(t, "/workspace/auth", choices[0].Workspace)
	require.Equal(t, updated, choices[0].UpdatedAt)
	require.Equal(t, "Investigate the scheduler race in the worker pool", choices[1].Title)
	require.Equal(t, updated.Add(-time.Hour), choices[1].UpdatedAt)
}

func TestTUISessionEntriesRestoreTextAttachmentsAndToolResults(t *testing.T) {
	sess := &session.Session{
		ID: "resume-session",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: []anthropic.ContentBlock{
					{Type: "text", Text: "inspect the image"},
					{Type: "image", Source: &anthropic.ContentSource{MediaType: "image/png"}},
				},
			},
			{
				Role: "assistant",
				Content: []anthropic.ContentBlock{
					{Type: "text", Text: "I will inspect it."},
					{Type: "tool_use", ID: "tool-1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
				},
			},
			anthropic.ToolResultMessage("tool-1", `{"content":"package main"}`, false),
		},
	}

	entries := tuiSessionEntries(sess)
	require.Len(t, entries, 4)
	require.Equal(t, "Session resume-session", entries[0].Text)
	require.Contains(t, entries[1].Text, "inspect the image")
	require.Contains(t, entries[1].Text, "Image attachment (image/png)")
	require.Equal(t, "I will inspect it.", entries[2].Text)
	require.NotNil(t, entries[3].Tool)
	require.Equal(t, "read_file", entries[3].Tool.Name)
	require.Equal(t, "success", entries[3].Tool.Status)
	require.Contains(t, entries[3].Tool.Output, "package main")
}

func TestTUISlashHandlerOffersPickerAndSynchronizesResumedSession(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("current-session")
	require.NoError(t, err)
	require.NoError(t, store.Append(current.ID, anthropic.TextMessage("user", "current prompt")))
	target, err := store.CreateWithIdentity("target-session", session.SessionIdentity{Title: "Target session title", Workspace: workspace})
	require.NoError(t, err)
	require.NoError(t, store.Append(target.ID, anthropic.TextMessage("user", "target prompt")))
	require.NoError(t, store.Append(target.ID, anthropic.TextMessage("assistant", "target answer")))
	current, err = store.OpenExisting(current.ID)
	require.NoError(t, err)

	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	picker, err := handler(context.Background(), "/resume")
	require.NoError(t, err)
	require.True(t, picker.Handled)
	require.Len(t, picker.SessionChoices, 1)
	require.Equal(t, "target-session", picker.SessionChoices[0].ID)
	require.Equal(t, "Target session title", picker.SessionChoices[0].Title)

	resumed, err := handler(context.Background(), "/resume Target session title")
	require.NoError(t, err)
	require.True(t, resumed.Handled)
	require.NotNil(t, resumed.Session)
	require.Equal(t, "target-session", resumed.Session.ID)
	require.Equal(t, "target-session", current.ID)
	require.Contains(t, resumed.Output, "session resumed: target-session")
	require.Contains(t, resumed.Session.Entries[1].Text, "target prompt")
	require.Contains(t, resumed.Session.Candidates, "/resume")

	cleared, err := handler(context.Background(), "/conversation clear --confirm")
	require.NoError(t, err)
	require.True(t, cleared.Handled)
	require.NotNil(t, cleared.Session)
	require.NotEqual(t, "target-session", cleared.Session.ID)
	require.Equal(t, cleared.Session.ID, current.ID)
	require.Len(t, cleared.Session.Entries, 1)
	require.NotContains(t, cleared.Session.Entries[0].Text, "target prompt")
}

func TestTUISlashHandlerOpensInteractiveControlViews(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("before\n"), 0o644))
	runGit(t, workspace, "add", "tracked.txt")
	runGit(t, workspace, "commit", "-m", "initial")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("after\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "staged.txt"), []byte("staged\n"), 0o644))
	runGit(t, workspace, "add", "staged.txt")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "untracked.txt"), []byte("untracked\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "nested", "note.txt"), []byte("nested\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "worker-state.json"), []byte("{}\n"), 0o644))

	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("control-session")
	require.NoError(t, err)
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "glm52",
			PermissionMode: "prompt",
			PermissionRules: config.PermissionRules{
				Allow: []string{"read_file"},
				Ask:   []string{"bash"},
				Deny:  []string{"write_file"},
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	modeState := newTUIModeState(app.Config)
	handler := app.tuiSlashHandler(current, modeState)

	modelResult, err := handler(context.Background(), "/model")
	require.NoError(t, err)
	require.True(t, modelResult.OpenModelPicker)

	todosResult, err := handler(context.Background(), "/todos")
	require.NoError(t, err)
	require.True(t, todosResult.OpenTodos)

	permissionsResult, err := handler(context.Background(), "/permissions")
	require.NoError(t, err)
	require.NotNil(t, permissionsResult.PermissionSettings)
	require.Contains(t, permissionsResult.PermissionSettings.Allow, "read_file")
	require.Contains(t, permissionsResult.PermissionSettings.Ask, "bash")
	require.Contains(t, permissionsResult.PermissionSettings.Deny, "write_file")

	contextResult, err := handler(context.Background(), "/context")
	require.NoError(t, err)
	require.NotNil(t, contextResult.Information)
	require.Equal(t, "Context", contextResult.Information.Title)
	require.Contains(t, strings.Join(contextResult.Information.Lines, "\n"), "glm52")

	statusResult, err := handler(context.Background(), "/status")
	require.NoError(t, err)
	require.NotNil(t, statusResult.CommandView)
	require.Equal(t, 0, statusResult.CommandView.SelectedTab)
	require.Equal(t, []string{"Status", "Config", "Usage"}, commandViewTabTitles(statusResult.CommandView.Tabs))

	configResult, err := handler(context.Background(), "/settings")
	require.NoError(t, err)
	require.NotNil(t, configResult.CommandView)
	require.Equal(t, 1, configResult.CommandView.SelectedTab)
	require.Contains(t, commandViewItemLabels(configResult.CommandView.Tabs[1].Items), "Model")
	require.Contains(t, commandViewItemLabels(configResult.CommandView.Tabs[1].Items), "Output style")
	require.Contains(t, commandViewItemLabels(configResult.CommandView.Tabs[1].Items), "Vim mode")

	usageResult, err := handler(context.Background(), "/usage")
	require.NoError(t, err)
	require.NotNil(t, usageResult.CommandView)
	require.Equal(t, 2, usageResult.CommandView.SelectedTab)
	require.Contains(t, strings.Join(usageResult.CommandView.Tabs[2].Lines, "\n"), "Total tokens")

	themeResult, err := handler(context.Background(), "/theme")
	require.NoError(t, err)
	require.True(t, themeResult.OpenThemePicker)

	fastResult, err := handler(context.Background(), "/fast")
	require.NoError(t, err)
	require.NotNil(t, fastResult.CommandView)
	require.Equal(t, "Fast mode", fastResult.CommandView.Title)
	require.Contains(t, commandViewItemLabels(fastResult.CommandView.Tabs[0].Items), "Enabled")

	outputStyleResult, err := handler(context.Background(), "/output-style")
	require.NoError(t, err)
	require.NotNil(t, outputStyleResult.CommandView)
	require.Equal(t, "Output style", outputStyleResult.CommandView.Title)
	require.Contains(t, commandViewItemLabels(outputStyleResult.CommandView.Tabs[0].Items), "concise")

	sandboxResult, err := handler(context.Background(), "/sandbox")
	require.NoError(t, err)
	require.NotNil(t, sandboxResult.CommandView)
	require.Equal(t, "Sandbox", sandboxResult.CommandView.Title)
	require.Contains(t, commandViewItemLabels(sandboxResult.CommandView.Tabs[0].Items), "Automatic")
	require.Contains(t, commandViewItemLabels(sandboxResult.CommandView.Tabs[0].Items), "Disabled")

	statsResult, err := handler(context.Background(), "/stats")
	require.NoError(t, err)
	require.NotNil(t, statsResult.CommandView)
	require.Equal(t, 2, statsResult.CommandView.SelectedTab)

	addDirResult, err := handler(context.Background(), "/add-dir")
	require.NoError(t, err)
	require.NotNil(t, addDirResult.TextInputDialog)
	require.Equal(t, "add-dir", addDirResult.TextInputDialog.Action)

	planResult, err := handler(context.Background(), "/plan inspect the release")
	require.NoError(t, err)
	require.Equal(t, "inspect the release", planResult.Query)
	require.True(t, app.Config.PlanMode)
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Equal(t, "plan", modeState.Label())

	exitPlanResult, err := handler(context.Background(), "/exit-plan")
	require.NoError(t, err)
	require.Contains(t, exitPlanResult.Output, "inactive")
	require.False(t, app.Config.PlanMode)
	require.Equal(t, "prompt", app.Config.PermissionMode)
	require.Equal(t, "default", modeState.Label())

	statusJSONResult, err := handler(context.Background(), "/status --json")
	require.NoError(t, err)
	require.Nil(t, statusJSONResult.CommandView)
	require.Contains(t, statusJSONResult.Output, `"kind": "status"`)

	diffResult, err := handler(context.Background(), "/diff")
	require.NoError(t, err)
	require.NotNil(t, diffResult.Diff)
	require.Len(t, diffResult.Diff.Sources, 2)
	allFiles := []tui.DiffFile{}
	for _, source := range diffResult.Diff.Sources {
		allFiles = append(allFiles, source.Files...)
	}
	require.ElementsMatch(t, []string{"tracked.txt", "staged.txt", "untracked.txt", "nested/note.txt"}, diffFilePaths(allFiles))
	require.Equal(t, "modified", diffFileByPath(t, allFiles, "tracked.txt").Status)
	require.Equal(t, "added", diffFileByPath(t, allFiles, "staged.txt").Status)
	require.Equal(t, "untracked", diffFileByPath(t, allFiles, "untracked.txt").Status)

	jsonResult, err := handler(context.Background(), "/diff --json")
	require.NoError(t, err)
	require.Nil(t, jsonResult.Diff)
	require.Contains(t, jsonResult.Output, `"kind": "diff"`)
}

func commandViewTabTitles(tabs []tui.CommandViewTab) []string {
	titles := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		titles = append(titles, tab.Title)
	}
	return titles
}

func commandViewItemLabels(items []tui.CommandViewItem) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.Label)
	}
	return labels
}

func commandViewItemByLabel(t *testing.T, items []tui.CommandViewItem, label string) tui.CommandViewItem {
	t.Helper()
	for _, item := range items {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("missing command view item %q", label)
	return tui.CommandViewItem{}
}

func TestTUISlashHandlerOpensExtensionManagementViews(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	skillDir := filepath.Join(configHome, "skills", "demo")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: Demo skill\n---\n\n# Demo\n\nInspect the workspace.\n"), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("extensions-session")
	require.NoError(t, err)
	app := &App{
		Config: config.Config{
			ConfigHome:    configHome,
			EnabledSkills: []string{"demo"},
			MCPServers: map[string]config.MCPServerConfig{
				"local": {Command: "codog-test-mcp", Required: true},
			},
			Hooks: config.HookConfig{PreToolUse: []string{"printf hook"}},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
		PluginManifests: []plugins.Manifest{{
			ID: "demo-plugin", Name: "Demo Plugin", Version: "1.0.0", Description: "Plugin details", Enabled: true,
		}},
		AgentDefinitions: []agentdefs.Definition{{Name: "reviewer", Description: "Review changes", Model: "glm52"}},
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	for _, test := range []struct {
		command string
		tab     int
		item    string
	}{
		{command: "/skills", tab: 0, item: "demo"},
		{command: "/mcp", tab: 1, item: "local"},
		{command: "/hooks", tab: 2, item: "pre_tool_use"},
		{command: "/plugins", tab: 3, item: "Demo Plugin"},
		{command: "/agents", tab: 4, item: "reviewer"},
	} {
		result, err := handler(context.Background(), test.command)
		require.NoError(t, err, test.command)
		require.NotNil(t, result.CommandView, test.command)
		require.Equal(t, test.tab, result.CommandView.SelectedTab, test.command)
		require.Contains(t, commandViewItemLabels(result.CommandView.Tabs[test.tab].Items), test.item, test.command)
	}

	skillsView, err := handler(context.Background(), "/skills")
	require.NoError(t, err)
	require.Equal(t, "/skills disable demo", commandViewItemByLabel(t, skillsView.CommandView.Tabs[0].Items, "demo").SecondaryCommand)
	agentsView, err := handler(context.Background(), "/agents")
	require.NoError(t, err)
	require.Contains(t, commandViewItemLabels(agentsView.CommandView.Tabs[4].Items), "Create new agent")
	reviewer := commandViewItemByLabel(t, agentsView.CommandView.Tabs[4].Items, "reviewer")
	require.Equal(t, "prefill", reviewer.SecondaryAction)
	require.Equal(t, "/agents run reviewer ", reviewer.SecondaryCommand)

	detail, err := handler(context.Background(), "/skills show demo")
	require.NoError(t, err)
	require.NotNil(t, detail.Information)
	require.Contains(t, strings.Join(detail.Information.Lines, "\n"), "Inspect the workspace")
	agentDetail, err := handler(context.Background(), "/agents show reviewer")
	require.NoError(t, err)
	require.NotNil(t, agentDetail.Information)
	require.Contains(t, strings.Join(agentDetail.Information.Lines, "\n"), "Review changes")
	createdAgent, err := handler(context.Background(), "/agents create helper")
	require.NoError(t, err)
	require.NotNil(t, createdAgent.CommandView)
	require.Equal(t, 4, createdAgent.CommandView.SelectedTab)
	require.Contains(t, commandViewItemLabels(createdAgent.CommandView.Tabs[4].Items), "helper")

	jsonList, err := handler(context.Background(), "/skills list --json")
	require.NoError(t, err)
	require.Nil(t, jsonList.CommandView)
	require.Nil(t, jsonList.Information)
	require.Contains(t, jsonList.Output, `"kind": "skills"`)
}

func TestTUISlashHandlerOpensRuntimeManagementViews(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell command")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	sessions := session.NewWorkspaceStore(configHome, workspace)
	current, err := sessions.Open("runtime-session")
	require.NoError(t, err)

	taskStore := background.NewStore(configHome)
	task, err := taskStore.RunWithOptions("printf runtime-output; sleep 30", workspace, background.RunOptions{
		Kind:        "agent",
		SessionID:   current.ID,
		Description: "Review runtime changes",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = taskStore.Stop(task.ID) })
	require.Eventually(t, func() bool {
		log, logErr := taskStore.Logs(task.ID, 1024)
		return logErr == nil && strings.Contains(log, "runtime-output")
	}, 2*time.Second, 20*time.Millisecond)
	agentRun, err := agentruns.NewStore(configHome).Save(agentruns.Run{
		ID:        "run-runtime",
		Agent:     "reviewer",
		Prompt:    "Review runtime changes",
		Workspace: workspace,
		SessionID: current.ID,
		TaskID:    task.ID,
		CreatedAt: task.StartedAt,
		UpdatedAt: task.StartedAt,
	})
	require.NoError(t, err)

	teamEntry, err := team.NewStore(configHome).Create("reviewers", []team.TaskSpec{{
		Prompt: "Review runtime changes", TaskID: task.ID,
	}}, []string{task.ID})
	require.NoError(t, err)
	schedule, err := cron.NewStore(configHome).Create("@daily", "Review pending changes", "Daily review")
	require.NoError(t, err)

	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  sessions,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	for _, test := range []struct {
		command string
		tab     int
		item    string
	}{
		{command: "/tasks", tab: 0, item: "Review runtime changes"},
		{command: "/team", tab: 1, item: "reviewers"},
		{command: "/cron", tab: 2, item: "Daily review"},
		{command: "/agents runs", tab: 3, item: "@reviewer · Review runtime changes"},
		{command: "/subagent", tab: 3, item: "@reviewer · Review runtime changes"},
	} {
		result, err := handler(context.Background(), test.command)
		require.NoError(t, err, test.command)
		require.NotNil(t, result.CommandView, test.command)
		require.Equal(t, test.tab, result.CommandView.SelectedTab, test.command)
		require.Contains(t, commandViewItemLabels(result.CommandView.Tabs[test.tab].Items), test.item, test.command)
	}

	taskDetail, err := handler(context.Background(), "/tasks status "+task.ID)
	require.NoError(t, err)
	require.NotNil(t, taskDetail.Information)
	require.Contains(t, strings.Join(taskDetail.Information.Lines, "\n"), "runtime-output")

	teamDetail, err := handler(context.Background(), "/team status "+teamEntry.ID)
	require.NoError(t, err)
	require.NotNil(t, teamDetail.Information)
	require.Contains(t, strings.Join(teamDetail.Information.Lines, "\n"), "reviewers")

	scheduleDetail, err := handler(context.Background(), "/cron show "+schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, scheduleDetail.Information)
	require.Contains(t, strings.Join(scheduleDetail.Information.Lines, "\n"), "Review pending changes")

	agentRunDetail, err := handler(context.Background(), "/subagent status "+agentRun.ID)
	require.NoError(t, err)
	require.NotNil(t, agentRunDetail.Information)
	require.Contains(t, strings.Join(agentRunDetail.Information.Lines, "\n"), "runtime-output")

	disabled, err := handler(context.Background(), "/cron disable "+schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, disabled.CommandView)
	require.Equal(t, 2, disabled.CommandView.SelectedTab)
	require.Contains(t, commandViewItemByLabel(t, disabled.CommandView.Tabs[2].Items, "Daily review").Value, "disabled")

	jsonTask, err := handler(context.Background(), "/tasks status "+task.ID+" --json")
	require.NoError(t, err)
	require.Nil(t, jsonTask.Information)
	require.Contains(t, jsonTask.Output, `"kind": "background"`)

	jsonSchedule, err := handler(context.Background(), "/cron show "+schedule.ID+" --json")
	require.NoError(t, err)
	require.Nil(t, jsonSchedule.Information)
	require.Contains(t, jsonSchedule.Output, `"action": "show"`)

	jsonAgentRun, err := handler(context.Background(), "/agents status "+agentRun.ID+" --json")
	require.NoError(t, err)
	require.Nil(t, jsonAgentRun.Information)
	require.Contains(t, jsonAgentRun.Output, `"action": "status"`)

	stoppedRun, err := handler(context.Background(), "/agents stop "+agentRun.ID)
	require.NoError(t, err)
	require.NotNil(t, stoppedRun.CommandView)
	require.Equal(t, 3, stoppedRun.CommandView.SelectedTab)
	require.Contains(t, commandViewItemByLabel(t, stoppedRun.CommandView.Tabs[3].Items, "@reviewer · Review runtime changes").Value, "stopped")
}

func TestTUISlashHandlerOpensConversationManagementViews(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("conversation-current")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(current.ID, "inspect the current changes"))
	other, err := store.Open("conversation-other")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(other.ID, "review another branch"))

	bookmark, err := bookmarks.NewStore(configHome).Add(bookmarks.Bookmark{
		Name: "review-point", Workspace: workspace, SessionID: current.ID, Note: "Before review",
	})
	require.NoError(t, err)

	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	for _, test := range []struct {
		command string
		tab     int
		item    string
	}{
		{command: "/history", tab: 0, item: "inspect the current changes"},
		{command: "/sessions", tab: 1, item: "inspect the current changes"},
		{command: "/bookmarks", tab: 2, item: "review-point"},
	} {
		result, err := handler(context.Background(), test.command)
		require.NoError(t, err, test.command)
		require.NotNil(t, result.CommandView, test.command)
		require.Equal(t, test.tab, result.CommandView.SelectedTab, test.command)
		require.Contains(t, commandViewItemLabels(result.CommandView.Tabs[test.tab].Items), test.item, test.command)
	}

	history, err := handler(context.Background(), "/history")
	require.NoError(t, err)
	historyItem := commandViewItemByLabel(t, history.CommandView.Tabs[0].Items, "inspect the current changes")
	require.Equal(t, "prefill", historyItem.Action)
	require.Equal(t, "inspect the current changes", historyItem.Command)

	bookmarkDetail, err := handler(context.Background(), "/bookmarks show "+bookmark.ID)
	require.NoError(t, err)
	require.NotNil(t, bookmarkDetail.Information)
	require.Contains(t, strings.Join(bookmarkDetail.Information.Lines, "\n"), "Before review")

	rewind, err := handler(context.Background(), "/rewind")
	require.NoError(t, err)
	require.True(t, rewind.OpenMessageActions)

	jsonHistory, err := handler(context.Background(), "/history --json")
	require.NoError(t, err)
	require.Nil(t, jsonHistory.CommandView)
	require.Contains(t, jsonHistory.Output, `"kind": "prompt_history"`)

	added, err := handler(context.Background(), "/bookmarks add checkpoint")
	require.NoError(t, err)
	require.NotNil(t, added.CommandView)
	require.Equal(t, 2, added.CommandView.SelectedTab)
	require.Contains(t, commandViewItemLabels(added.CommandView.Tabs[2].Items), "checkpoint")
}

func TestTUISlashHandlerOpensWorkspaceWorkflowViews(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Instructions\n\nRun focused tests.\n"), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("workspace-workflows")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(current.ID, "inspect workspace workflows"))
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "glm52",
			PermissionMode: "read-only",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	memoryView, err := handler(context.Background(), "/memory")
	require.NoError(t, err)
	require.NotNil(t, memoryView.CommandView)
	require.Equal(t, "Memory", memoryView.CommandView.Title)
	require.Contains(t, commandViewItemLabels(memoryView.CommandView.Tabs[0].Items), "AGENTS.md")
	memoryItem := commandViewItemByLabel(t, memoryView.CommandView.Tabs[0].Items, "AGENTS.md")
	require.Equal(t, "/memory edit AGENTS.md", memoryItem.Command)
	require.Equal(t, "/memory show AGENTS.md", memoryItem.SecondaryCommand)

	memoryDetail, err := handler(context.Background(), "/memory show AGENTS.md")
	require.NoError(t, err)
	require.NotNil(t, memoryDetail.Information)
	require.Contains(t, strings.Join(memoryDetail.Information.Lines, "\n"), "Run focused tests.")

	doctorView, err := handler(context.Background(), "/doctor")
	require.NoError(t, err)
	require.NotNil(t, doctorView.Information)
	require.Equal(t, "Doctor", doctorView.Information.Title)
	require.Contains(t, strings.Join(doctorView.Information.Lines, "\n"), "Summary")

	ideView, err := handler(context.Background(), "/ide")
	require.NoError(t, err)
	require.NotNil(t, ideView.CommandView)
	require.Equal(t, "IDE", ideView.CommandView.Title)
	require.Contains(t, commandViewItemLabels(ideView.CommandView.Tabs[0].Items), "No IDE connected")
	require.Contains(t, commandViewItemLabels(ideView.CommandView.Tabs[0].Items), "Start IDE bridge")

	exportView, err := handler(context.Background(), "/export")
	require.NoError(t, err)
	require.NotNil(t, exportView.ExportDialog)
	require.Equal(t, "workspace-workflows.md", exportView.ExportDialog.DefaultFilename)

	compact, err := handler(context.Background(), "/compact")
	require.NoError(t, err)
	require.Equal(t, "compact", compact.RuntimeAction)
	copyResult, err := handler(context.Background(), "/copy")
	require.NoError(t, err)
	require.Equal(t, "copy", copyResult.RuntimeAction)

	added, err := handler(context.Background(), "/memory add Keep output concise.")
	require.NoError(t, err)
	require.NotNil(t, added.CommandView)
	require.Contains(t, commandViewItemLabels(added.CommandView.Tabs[0].Items), "AGENTS.md")
	contents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "Keep output concise.")

	jsonMemory, err := handler(context.Background(), "/memory list --json")
	require.NoError(t, err)
	require.Nil(t, jsonMemory.CommandView)
	require.Contains(t, jsonMemory.Output, `"kind": "memory"`)
	jsonIDE, err := handler(context.Background(), "/ide --json")
	require.NoError(t, err)
	require.Nil(t, jsonIDE.CommandView)
	require.Contains(t, jsonIDE.Output, `"kind": "ide"`)
}

func TestTUISlashHandlerAlignsReferenceLocalWorkflows(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("reference-workflows")
	require.NoError(t, err)
	require.NoError(t, store.Append(current.ID, anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{
		{Type: "tool_use", ID: "read-ok", Name: "Read", Input: json.RawMessage(`{"path":"pkg/auth.go"}`)},
		{Type: "tool_use", ID: "read-failed", Name: "read_file", Input: json.RawMessage(`{"path":"missing.go"}`)},
		{Type: "tool_use", ID: "write-ok", Name: "write_file", Input: json.RawMessage(`{"path":"` + filepath.ToSlash(filepath.Join(workspace, "generated.go")) + `"}`)},
		{Type: "tool_use", ID: "notebook-ok", Name: "notebook_read", Input: json.RawMessage(`{"notebook_path":"analysis.ipynb"}`)},
	}}))
	require.NoError(t, store.Append(current.ID, anthropic.ToolResultMessage("read-ok", "contents", false)))
	require.NoError(t, store.Append(current.ID, anthropic.ToolResultMessage("read-failed", "not found", true)))
	current, err = store.OpenExisting(current.ID)
	require.NoError(t, err)
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "glm52"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	files, err := handler(context.Background(), "/files")
	require.NoError(t, err)
	require.NotNil(t, files.Information)
	require.Equal(t, "Files in context", files.Information.Title)
	require.Equal(t, []string{"pkg/auth.go", "generated.go", "analysis.ipynb"}, files.Information.Lines)
	require.NotContains(t, strings.Join(files.Information.Lines, "\n"), "missing.go")

	statusline, err := handler(context.Background(), "/statusline use git branch and context remaining")
	require.NoError(t, err)
	require.Contains(t, statusline.Query, "Set up Codog's status line UI")
	require.Contains(t, statusline.Query, "use git branch and context remaining")
	require.Contains(t, statusline.Query, "codog statusline")

	terminalView, err := handler(context.Background(), "/terminal-setup")
	require.NoError(t, err)
	require.NotNil(t, terminalView.CommandView)
	require.Equal(t, "Terminal setup", terminalView.CommandView.Title)
	require.Contains(t, commandViewItemLabels(terminalView.CommandView.Tabs[0].Items), "Install shell integration")
	require.Contains(t, commandViewItemLabels(terminalView.CommandView.Tabs[0].Items), "Show installation snippet")

	installed, err := handler(context.Background(), "/terminal-setup install --target shell")
	require.NoError(t, err)
	require.NotNil(t, installed.CommandView)
	require.Contains(t, commandViewItemLabels(installed.CommandView.Tabs[0].Items), "Remove shell integration")

	keybindingsView, err := handler(context.Background(), "/keybindings")
	require.NoError(t, err)
	require.NotNil(t, keybindingsView.CommandView)
	require.Equal(t, "Keybindings", keybindingsView.CommandView.Title)
	require.Contains(t, commandViewItemLabels(keybindingsView.CommandView.Tabs[0].Items), "Create template")

	created, err := handler(context.Background(), "/keybindings init")
	require.NoError(t, err)
	require.NotNil(t, created.CommandView)
	require.NotContains(t, commandViewItemLabels(created.CommandView.Tabs[0].Items), "Create template")
	require.FileExists(t, filepath.Join(configHome, "keybindings.json"))
}

func TestTUISlashHandlerRenamesAndBranchesConversations(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("conversation-source")
	require.NoError(t, err)
	require.NoError(t, store.Append(current.ID, anthropic.TextMessage("user", "Investigate scheduler timeout")))
	current, err = store.OpenExisting(current.ID)
	require.NoError(t, err)
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "glm52"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	renamed, err := handler(context.Background(), "/rename Scheduler investigation")
	require.NoError(t, err)
	require.NotNil(t, renamed.Session)
	require.Equal(t, "conversation-source", renamed.Session.ID)
	require.Contains(t, renamed.Session.Entries[0].Text, "Scheduler investigation")
	require.Equal(t, "Scheduler investigation", current.Identity.Title)

	autoRenamed, err := handler(context.Background(), "/rename")
	require.NoError(t, err)
	require.Equal(t, "conversation-source", autoRenamed.Session.ID)
	require.Equal(t, "Investigate scheduler timeout", current.Identity.Title)

	branched, err := handler(context.Background(), "/branch Follow-up analysis")
	require.NoError(t, err)
	require.NotNil(t, branched.Session)
	require.NotEqual(t, "conversation-source", branched.Session.ID)
	require.Equal(t, branched.Session.ID, current.ID)
	require.Equal(t, "Follow-up analysis", current.Identity.Title)
	require.Equal(t, "conversation-source", current.Metadata.ParentSessionID)
	require.Equal(t, "Follow-up analysis", current.Metadata.BranchName)
	require.Contains(t, branched.Output, "Conversation branched")

	source, err := store.OpenExisting("conversation-source")
	require.NoError(t, err)
	require.Equal(t, "Investigate scheduler timeout", source.Identity.Title)
	require.Len(t, source.Messages, 1)
}

func TestTUISlashHandlerTogglesSearchableSessionTag(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.CreateWithIdentity("tag-source", session.SessionIdentity{Title: "Tag workflow"})
	require.NoError(t, err)
	require.NoError(t, store.Append(current.ID, anthropic.TextMessage("user", "inspect tagged session")))
	current, err = store.OpenExisting(current.ID)
	require.NoError(t, err)
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "glm52"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	handler := app.tuiSlashHandler(current, newTUIModeState(app.Config))

	set, err := handler(context.Background(), "/tag release candidate")
	require.NoError(t, err)
	require.NotNil(t, set.Session)
	require.Equal(t, "release candidate", current.Identity.Tag)
	require.Contains(t, set.Output, "#release candidate")
	require.Contains(t, set.Session.Entries[0].Text, "#release candidate")

	confirm, err := handler(context.Background(), "/tag release candidate")
	require.NoError(t, err)
	require.NotNil(t, confirm.CommandView)
	require.Equal(t, "Remove tag?", confirm.CommandView.Title)
	require.Equal(t, []string{"Yes, remove tag", "No, keep tag"}, commandViewItemLabels(confirm.CommandView.Tabs[0].Items))

	kept, err := handler(context.Background(), "/conversation-tag keep")
	require.NoError(t, err)
	require.Contains(t, kept.Output, "Kept tag #release candidate")
	require.Equal(t, "release candidate", current.Identity.Tag)

	removed, err := handler(context.Background(), "/conversation-tag remove")
	require.NoError(t, err)
	require.Contains(t, removed.Output, "Removed tag #release candidate")
	require.Empty(t, current.Identity.Tag)
	require.NotContains(t, removed.Session.Entries[0].Text, "#release candidate")

	sanitized, err := handler(context.Background(), "/tag sec\u200burity")
	require.NoError(t, err)
	require.Equal(t, "security", current.Identity.Tag)
	require.Contains(t, sanitized.Output, "#security")

	reopened, err := store.OpenExisting(current.ID)
	require.NoError(t, err)
	require.Equal(t, "security", reopened.Identity.Tag)
	choices, err := app.tuiResumeSessionChoices("")
	require.NoError(t, err)
	require.Len(t, choices, 1)
	preview := tui.PreviewSessionPicker(choices, "security", 80, 24, true)
	require.Equal(t, current.ID, preview.SelectedID)
	require.Contains(t, preview.View, "#security")

	help, err := handler(context.Background(), "/tag")
	require.NoError(t, err)
	require.Contains(t, help.Output, "Usage: /tag <tag-name>")
}

func TestTUISideQuestionUsesDismissibleInformationPanel(t *testing.T) {
	view, ok := tuiSideQuestionInformation(
		"/btw why did this test fail?",
		"The fixture used the wrong path.\n\nbtw session: side-session\nsource session: main-session",
	)
	require.True(t, ok)
	require.Equal(t, "/btw", view.Title)
	require.True(t, view.DismissOnConfirm)
	require.Equal(t, "why did this test fail?", view.Lines[0])
	require.Contains(t, strings.Join(view.Lines, "\n"), "The fixture used the wrong path.")
	require.NotContains(t, strings.Join(view.Lines, "\n"), "side-session")

	_, ok = tuiSideQuestionInformation("/btw question --json", `{}`)
	require.False(t, ok)
}

func TestSubmitTUITextInputPreservesDirectoryWithSpaces(t *testing.T) {
	workspace := t.TempDir()
	external := filepath.Join(t.TempDir(), "shared workspace")
	require.NoError(t, os.MkdirAll(external, 0o755))
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	result, err := app.submitTUITextInput(context.Background(), "add-dir", external)
	require.NoError(t, err)
	require.Equal(t, "directory added", result.Status)
	require.Contains(t, strings.Join(result.Lines, "\n"), external)

	report, err := pathscope.BuildReport(workspace, nil, "list")
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	canonicalExternal, err := filepath.EvalSymlinks(external)
	require.NoError(t, err)
	require.Equal(t, canonicalExternal, report.Entries[0].Path)
}

func diffFilePaths(files []tui.DiffFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func diffFileByPath(t *testing.T, files []tui.DiffFile, path string) tui.DiffFile {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("missing diff file %q", path)
	return tui.DiffFile{}
}

func TestSelectTUIPermissionModeUpdatesCurrentSessionOnly(t *testing.T) {
	app := &App{Config: config.Config{PermissionMode: "prompt"}}
	state := newTUIModeState(app.Config)

	result, err := app.selectTUIPermissionMode(context.Background(), "accept edits", state)

	require.NoError(t, err)
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	require.False(t, app.Config.PlanMode)
	require.Equal(t, "accept edits", state.Label())
	require.Contains(t, result.Lines, "Mode: accept edits")
}

func TestToggleTUIVimUpdatesRuntimeAndPersistedPreference(t *testing.T) {
	configHome := t.TempDir()
	app := &App{
		Config: config.Config{ConfigHome: configHome, EditorMode: "default"},
		Out:    io.Discard,
		Err:    io.Discard,
	}

	result, err := app.toggleTUIVim(context.Background())

	require.NoError(t, err)
	require.Equal(t, "vim", app.Config.EditorMode)
	require.NotNil(t, result.VimEnabled)
	require.True(t, *result.VimEnabled)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"editorMode": "vim"`)
}

func TestTUIUntrackedPreviewDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on windows")
	}
	workspace := t.TempDir()
	external := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(external, []byte("external secret\n"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(workspace, "link.txt")))
	app := &App{Workspace: workspace}

	preview, lines := app.tuiUntrackedPreview("link.txt")

	require.Zero(t, lines)
	require.Contains(t, preview, "symlink: link.txt ->")
	require.NotContains(t, preview, "external secret")
}

func TestResumeSlashDoesNotCreateMissingSession(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	current, err := store.Open("current-session")
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Sessions: store, Workspace: workspace, Out: &out, Err: &out}

	app.handleResumeSlash(context.Background(), []string{"missing-session"}, current)

	require.Equal(t, "current-session", current.ID)
	require.Contains(t, out.String(), "session not found")
	_, err = store.OpenExisting("missing-session")
	require.ErrorIs(t, err, session.ErrSessionNotFound)

	out.Reset()
	app.handleSessionSlash([]string{"switch", "missing-switch"}, current)
	require.Equal(t, "current-session", current.ID)
	require.Contains(t, out.String(), "session not found")
	_, err = store.OpenExisting("missing-switch")
	require.ErrorIs(t, err, session.ErrSessionNotFound)
}

func TestEnterpriseAuditListsEvents(t *testing.T) {
	configHome := t.TempDir()
	require.NoError(t, audit.NewStore(configHome).Append(audit.Event{
		Type:      "permission",
		ToolName:  "bash",
		Allowed:   audit.Bool(false),
		SessionID: "session-1",
	}))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Enterprise([]string{"audit", "10"}))
	var report enterpriseAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "enterprise", report.Kind)
	require.Equal(t, "audit", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 10, report.Summary.Limit)
	require.Equal(t, 1, report.Summary.EventsReturned)
	require.Equal(t, 1, report.Summary.PermissionEvents)
	require.Equal(t, 1, report.Summary.DeniedEvents)
	require.Equal(t, 1, report.Summary.Tools["bash"])
	require.False(t, report.PolicyConfigured)
	require.Equal(t, "permission", report.Events[0].Type)
	require.False(t, *report.Events[0].Allowed)
}

func TestEnterpriseDefaultsToAudit(t *testing.T) {
	configHome := t.TempDir()
	require.NoError(t, audit.NewStore(configHome).Append(audit.Event{Type: "tool", ToolName: "read_file"}))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, PermissionMode: "read-only"},
		Out:    &out,
	}

	for _, args := range [][]string{
		nil,
		{"--json"},
		{"--output-format", "json"},
		{"--output-format", "text"},
		{"status", "3"},
		{"show"},
	} {
		out.Reset()
		require.NoError(t, app.Enterprise(args), args)
		var report enterpriseAuditReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report), args)
		require.Equal(t, "enterprise", report.Kind, args)
		require.Equal(t, "audit", report.Action, args)
		require.Equal(t, "ok", report.Status, args)
		require.Equal(t, "read-only", report.EffectivePermissionMode, args)
		require.Equal(t, 1, report.Summary.EventsReturned, args)
		if len(args) > 0 && args[0] == "status" {
			require.Equal(t, 3, report.Summary.Limit)
		}
	}
}

func TestEnterpriseVerifyCommand(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	dir := t.TempDir()
	policy := config.ManagedPolicy{MaxPermissionMode: "read-only", DeniedTools: []string{"bash"}}
	payload, err := config.ManagedPolicyPayload(policy)
	require.NoError(t, err)
	policy.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	policyPath := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, data, 0o644))

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Enterprise([]string{"verify", policyPath, base64.StdEncoding.EncodeToString(publicKey)}))
	var report enterpriseVerifyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "enterprise", report.Kind)
	require.Equal(t, "verify", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.SignatureValid)
	require.Equal(t, "read-only", report.Policy.MaxPermissionMode)
	require.NotContains(t, out.String(), policy.Signature)
}

func TestEnterpriseVerifyErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "enterprise", "verify"}, config.FlagOverrides{})
	})
	requireStructuredCLIError(t, err, []byte(out), "missing_argument", "missing_argument")
	require.Contains(t, out, `"command": "enterprise verify"`)
	require.Contains(t, out, `"argument": "POLICY PUBLIC_KEY"`)
}

func TestLocalSetupCommandsHonorGlobalJSONOutputFormat(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "enterprise"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var enterprise enterpriseAuditReport
	require.NoError(t, json.Unmarshal([]byte(out), &enterprise))
	require.Equal(t, "enterprise", enterprise.Kind)
	require.Equal(t, "audit", enterprise.Action)
	require.Equal(t, "ok", enterprise.Status)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "oauth", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var oauthStatus oauth.Status
	require.NoError(t, json.Unmarshal([]byte(out), &oauthStatus))
	require.Equal(t, "oauth", oauthStatus.Kind)
	require.Equal(t, "status", oauthStatus.Action)
	require.Equal(t, "ok", oauthStatus.Status)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--output-format", "json", "install-github-app", "--workflow", "claude", "--dry-run"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var setup githubsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &setup))
	require.Equal(t, "install_github_app", setup.Kind)
	require.Equal(t, "ok", setup.Status)
	require.True(t, setup.DryRun)
	require.Len(t, setup.Workflows, 1)
}

func TestEnterpriseErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "unknown action",
			args:      []string{"enterprise", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "enterprise"`, `"bogus"`},
		},
		{
			name:      "audit invalid limit",
			args:      []string{"enterprise", "audit", "bogus"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "limit"`, `"value": "bogus"`},
		},
		{
			name:      "status invalid limit",
			args:      []string{"enterprise", "status", "bogus"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "limit"`, `"value": "bogus"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func TestEnterpriseAuditReportsManagedPolicyStatus(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	configHome := t.TempDir()
	policy := config.ManagedPolicy{
		MaxPermissionMode: "read-only",
		DeniedTools:       []string{"bash"},
		PermissionRules:   config.PermissionRules{Deny: []string{"write_file"}},
	}
	payload, err := config.ManagedPolicyPayload(policy)
	require.NoError(t, err)
	policy.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	policyPath := filepath.Join(configHome, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, data, 0o644))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				EnterprisePolicy:          policyPath,
				EnterprisePolicyPublicKey: base64.StdEncoding.EncodeToString(publicKey),
			},
			PermissionMode: "read-only",
			PermissionRules: config.PermissionRules{
				Deny:        []string{"write_file"},
				DeniedTools: []string{"bash"},
			},
		},
		Out: &out,
	}
	require.NoError(t, app.Enterprise([]string{"audit", "5"}))
	var report enterpriseAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ok", report.Status)
	require.True(t, report.PolicyConfigured)
	require.True(t, report.PolicyPublicKeyPresent)
	require.True(t, report.PolicySignatureRequired)
	require.True(t, report.PolicySignatureValid)
	require.NotNil(t, report.Policy)
	require.Equal(t, "read-only", report.Policy.MaxPermissionMode)
	require.Contains(t, report.Policy.DeniedTools, "bash")
	require.NotContains(t, out.String(), policy.Signature)
}

func TestVersionCommandOutputsTextAndJSON(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	require.NoError(t, renderVersion(&out, workspace, nil))
	require.Contains(t, out.String(), "Codog")
	require.Contains(t, out.String(), "Version          0.1.0")
	out.Reset()

	require.NoError(t, renderVersion(&out, workspace, []string{"--json"}))
	require.Contains(t, out.String(), `"kind": "version"`)
	require.Contains(t, out.String(), `"version": "0.1.0"`)
	require.Contains(t, out.String(), `"go_version":`)

	require.NoError(t, RunCLI(context.Background(), []string{"--version"}, config.FlagOverrides{}))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"-v"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, "Codog")
	require.Contains(t, cliOut, "Version")
}

func TestUsageOverviewCommandsHaveDistinctReports(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	inputUsage := anthropic.Usage{InputTokens: 100}
	outputUsage := anthropic.Usage{OutputTokens: 50, CacheReadInputTokens: 25}
	require.NoError(t, store.AppendWithUsage("usage-session", anthropic.TextMessage("user", "hello"), &inputUsage))
	require.NoError(t, store.AppendWithUsage("usage-session", anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{
			{Type: "text", Text: "working"},
			{Type: "tool_use", ID: "tool-1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
		},
	}, &outputUsage))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-sonnet-4-5", MaxTokens: 4096},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.UsageOverview("cost", []string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	var cost usageOverviewReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cost))
	require.Equal(t, "cost", cost.Kind)
	require.Equal(t, "show", cost.Action)
	require.Equal(t, "ok", cost.Status)
	require.Equal(t, "usage-session", cost.SessionID)
	require.Equal(t, 175, cost.TotalTokens)
	require.NotNil(t, cost.CostUSD)
	require.Equal(t, cost.EstimatedUSD, *cost.CostUSD)
	require.Equal(t, "actual", cost.Source)
	require.Greater(t, *cost.CostUSD, 0.0)
	out.Reset()

	require.NoError(t, app.UsageOverview("tokens", []string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	var tokens usageOverviewReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &tokens))
	require.Equal(t, "tokens", tokens.Kind)
	require.Equal(t, 100, tokens.InputTokens)
	require.Equal(t, 50, tokens.OutputTokens)
	require.Equal(t, 25, tokens.CacheReadInputTokens)
	require.Equal(t, 64000, tokens.MaxOutputTokens)
	require.Equal(t, 200000, tokens.ContextWindowTokens)
	require.Equal(t, 199825, tokens.ContextRemainingTokens)
	require.InDelta(t, 0.0009, tokens.ContextUsedRatio, 0.0001)
	out.Reset()

	require.NoError(t, app.UsageOverview("stats", []string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	var stats usageOverviewReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stats))
	require.Equal(t, "stats", stats.Kind)
	require.Len(t, stats.Roles, 2)
	require.NotEmpty(t, stats.Blocks)
	require.NotNil(t, stats.ToolUse)
	require.Equal(t, 1, stats.ToolUse.ToolUses)
	out.Reset()

	require.NoError(t, app.UsageOverview("cost", nil, config.FlagOverrides{SessionID: "usage-session"}))
	require.Contains(t, out.String(), "Cost")
	require.Contains(t, out.String(), "Cost USD")
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/cost", []string{"--json"}, config.FlagOverrides{Resume: "usage-session"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &cost))
	require.Equal(t, "cost", cost.Kind)
	require.Equal(t, "usage-session", cost.SessionID)
	require.Equal(t, 175, cost.TotalTokens)
	require.NotNil(t, cost.CostUSD)
	require.Greater(t, *cost.CostUSD, 0.0)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/stats", []string{"--json"}, config.FlagOverrides{Resume: "usage-session"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &stats))
	require.Equal(t, "stats", stats.Kind)
	require.Equal(t, "usage-session", stats.SessionID)
	require.Equal(t, 175, stats.TotalTokens)
	require.NotNil(t, stats.ToolUse)
	require.Equal(t, 1, stats.ToolUse.ToolUses)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/tokens", []string{"--json"}, config.FlagOverrides{Resume: "usage-session"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &tokens))
	require.Equal(t, "tokens", tokens.Kind)
	require.Equal(t, "usage-session", tokens.SessionID)
	require.Equal(t, 175, tokens.TotalTokens)
	require.Equal(t, 200000, tokens.ContextWindowTokens)
}

func TestUsageOverviewDirectTokensDispatch(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{
		"config_home": configHome,
		"model":       "claude-sonnet-4-5",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	usageRecord := anthropic.Usage{InputTokens: 8, OutputTokens: 5}
	require.NoError(t, store.AppendWithUsage("dispatch-session", anthropic.TextMessage("assistant", "hello"), &usageRecord))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--session", "dispatch-session", "tokens", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report usageOverviewReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "tokens", report.Kind)
	require.Equal(t, 13, report.TotalTokens)
	require.Equal(t, 200000, report.ContextWindowTokens)
}

func TestSessionCommandsHonorGlobalJSONOutputFormat(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{
		"config_home": configHome,
		"model":       "claude-sonnet-4-5",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.AppendWithUsage("global-session", anthropic.TextMessage("user", "hello from global json"), &anthropic.Usage{InputTokens: 9}))
	require.NoError(t, store.AppendWithUsage("global-session", anthropic.TextMessage("assistant", "global json answer"), &anthropic.Usage{OutputTokens: 4}))

	base := []string{"--config", configPath, "--cwd", workspace, "--session", "global-session", "--output-format", "json"}
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), append(append([]string{}, base...), "cost"), config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cost usageOverviewReport
	require.NoError(t, json.Unmarshal([]byte(out), &cost))
	require.Equal(t, "cost", cost.Kind)
	require.Equal(t, 13, cost.TotalTokens)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), append(append([]string{}, base...), "tokens"), config.FlagOverrides{})
	})
	require.NoError(t, err)
	var tokens usageOverviewReport
	require.NoError(t, json.Unmarshal([]byte(out), &tokens))
	require.Equal(t, "tokens", tokens.Kind)
	require.Equal(t, 13, tokens.TotalTokens)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), append(append([]string{}, base...), "history"), config.FlagOverrides{})
	})
	require.NoError(t, err)
	var history prompthistory.Report
	require.NoError(t, json.Unmarshal([]byte(out), &history))
	require.Equal(t, "prompt_history", history.Kind)
	require.Equal(t, "global-session", history.SessionID)
	require.NotEmpty(t, history.Entries)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), append(append([]string{}, base...), "summary"), config.FlagOverrides{})
	})
	require.NoError(t, err)
	var summary sessionsummary.Report
	require.NoError(t, json.Unmarshal([]byte(out), &summary))
	require.Equal(t, "summary", summary.Kind)
	require.Equal(t, "global-session", summary.SessionID)
	require.Equal(t, 2, summary.MessageCount)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), append(append([]string{}, base...), "rename", "global-renamed"), config.FlagOverrides{})
	})
	require.NoError(t, err)
	var rename session.RenameResult
	require.NoError(t, json.Unmarshal([]byte(out), &rename))
	require.Equal(t, "global-session", rename.OldID)
	require.Equal(t, "global-renamed", rename.NewID)
}

func TestRunCLIPlanModeRequiredForcesReadOnlyStatus(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{
		"config_home": configHome,
		"model":       "claude-test",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--dangerously-skip-permissions",
			"--plan-mode-required",
			"status",
			"--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"permission_mode": "read-only"`)
	require.Contains(t, out, `"permission_mode_raw": "plan"`)
	require.Contains(t, out, `"permission_mode_source": "cli"`)
}

func TestHelpCommandOutputsTextAndJSON(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderHelpCommand(&out, nil))
	helpOutput := out.String()
	require.Contains(t, helpOutput, "Usage:")
	require.Contains(t, helpOutput, "[options] [prompt]")
	require.Contains(t, helpOutput, "Start the TUI and submit an initial prompt")
	require.Contains(t, helpOutput, "help all [query]")
	require.Contains(t, helpOutput, "--continue, -c")
	require.Contains(t, helpOutput, "--resume [ID|latest], -r [ID|latest]")
	require.Contains(t, helpOutput, "omit ID to choose")
	require.Contains(t, helpOutput, "--fork-session")
	require.Contains(t, helpOutput, "--fallback-model name")
	require.Contains(t, helpOutput, "--thinking enabled|adaptive|disabled")
	require.Contains(t, helpOutput, "--include-partial-messages")
	require.Contains(t, helpOutput, "--setting-sources sources")
	require.Contains(t, helpOutput, "--agents json")
	require.Contains(t, helpOutput, "--plugin-dir path")
	require.Contains(t, helpOutput, "--ide")
	require.Contains(t, helpOutput, "<cc-url|cc+unix-url>")
	require.Contains(t, helpOutput, "CODOG_EXTRA_BODY")
	require.NotContains(t, helpOutput, "Resume-safe commands:")
	out.Reset()

	require.NoError(t, renderHelpCommand(&out, []string{"--output-format", "json"}))
	var globalReport helpReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &globalReport))
	require.Equal(t, "help", globalReport.Kind)
	require.Equal(t, "show", globalReport.Action)
	require.Equal(t, "ok", globalReport.Status)
	require.Contains(t, globalReport.Help, "--continue, -c")
	require.Contains(t, globalReport.Help, "--resume [ID|latest], -r [ID|latest]")
	require.Contains(t, globalReport.Help, "--fork-session")
	require.Contains(t, globalReport.Help, "--fallback-model name")
	require.Contains(t, globalReport.Help, "--thinking enabled|adaptive|disabled")
	require.Contains(t, globalReport.Help, "--include-partial-messages")
	require.Contains(t, globalReport.Help, "<cc-url|cc+unix-url>")
	require.Contains(t, globalReport.Help, "help all [query]")
	out.Reset()

	require.NoError(t, renderHelpCommand(&out, []string{"all", "--output-format", "json"}))
	var catalog helpCatalogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &catalog))
	require.Equal(t, "catalog", catalog.Action)
	require.Equal(t, len(builtInCommandNames()), catalog.Count)
	require.Len(t, catalog.Commands, catalog.Count)
	require.Contains(t, catalog.Commands, helpCatalogEntry{
		Name:        "background",
		Usage:       "codog background [run|list|board|heartbeat|status|stop|restart|logs|watch|prune|supervise] [--output-format text|json]",
		Description: "Manage local background tasks.",
	})
	out.Reset()

	require.NoError(t, renderHelpCommand(&out, []string{"all", "mcp", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &catalog))
	require.Equal(t, "mcp", catalog.Query)
	require.NotZero(t, catalog.Count)
	require.Less(t, catalog.Count, len(builtInCommandNames()))
	for _, entry := range catalog.Commands {
		haystack := strings.ToLower(strings.Join(append([]string{entry.Name, entry.Usage, entry.Description}, entry.Aliases...), " "))
		require.Contains(t, haystack, "mcp")
	}
	out.Reset()

	require.NoError(t, renderHelpCommand(&out, []string{"doctor", "--output-format", "json"}))
	var report helpReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "help", report.Kind)
	require.Equal(t, "help", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "doctor", report.Topic)
	require.Equal(t, "doctor", report.Command)
	require.Equal(t, "codog doctor [--output-format text|json]", report.Usage)
	require.Contains(t, report.Help, "Doctor")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)
	require.Contains(t, report.OutputFields, "checks")
	require.Contains(t, report.StatusValues, "warn")
	require.Contains(t, report.CheckNames, "Auth")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"onboarding", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "onboarding", report.Topic)
	require.Contains(t, report.Help, "repo-scope token guidance")
	require.Contains(t, report.Help, ".gitignore")
	require.Contains(t, report.Help, ".codogignore")
	require.Contains(t, report.Help, "smallest useful")
	require.Contains(t, report.OutputFields, "scope_guidance")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"files", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "files", report.Topic)
	require.Contains(t, report.Help, "scope_risk")
	require.Contains(t, report.Help, "token sinks")
	require.Contains(t, report.OutputFields, "scope_risk")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"background", "--output-format", "json"}))
	report = helpReport{}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "background", report.Topic)
	require.Equal(t, "background", report.Command)
	require.Equal(t, "codog.help.generated.v1", report.SchemaVersion)
	require.Contains(t, report.Usage, "run|list|board|heartbeat|status|stop|restart|logs|watch|prune|supervise")
	require.Contains(t, report.Help, "Manage local background tasks")
	require.NotContains(t, report.Help, "not been specialized")
	require.Nil(t, report.LocalOnly)
	require.Nil(t, report.MutatesWorkspace)
	require.Contains(t, report.Related, "/background")
	require.Contains(t, report.Related, "codog capabilities resolve background")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"scope", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "scope", report.Topic)
	require.Contains(t, report.Help, "reversible actions")
	require.Contains(t, report.Help, ".codogignore")
	require.Contains(t, report.OutputFields, "restore_command")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"api", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "api", report.Topic)
	require.Contains(t, report.Usage, "listen")
	require.Contains(t, report.Usage, "start")
	require.Contains(t, report.Help, "serve|listen|start")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"ssh", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ssh", report.Topic)
	require.Contains(t, report.Usage, "-p|--print [PROMPT]")
	require.Contains(t, report.Help, "headless one-shot")
	require.NotContains(t, report.Help, "not supported with `ssh`")
	require.Contains(t, report.OutputFields, "print")
	require.Contains(t, report.OutputFields, "prompt_configured")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"mock-limits", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "mock-limits", report.Topic)
	require.Contains(t, report.Usage, "status")
	require.Contains(t, report.Usage, "server")
	require.Contains(t, report.Usage, "start")
	require.Contains(t, report.Help, "aliases for `serve`")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"status", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "status", report.Topic)
	require.Equal(t, "status", report.Command)
	require.Equal(t, "1.0", report.SchemaVersion)
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresCredentials)
	require.False(t, *report.RequiresCredentials)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)
	require.Contains(t, report.OutputFields, "workspace")
	require.Contains(t, report.OutputFields, "sandbox")
	require.Contains(t, report.OutputFields, "allowed_tools")
	require.Contains(t, report.WorkspaceFields, "memory_files")
	require.Contains(t, report.ConfigFields, "permission_mode_source")
	require.Contains(t, report.GitFields, "freshness")
	require.Contains(t, report.SandboxFields, "available")
	require.Contains(t, report.Related, "/status")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"prompt", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prompt", report.Topic)
	require.Equal(t, "prompt", report.Command)
	require.Contains(t, report.Usage, "stream-json")
	require.NotNil(t, report.LocalOnly)
	require.False(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresCredentials)
	require.True(t, *report.RequiresCredentials)
	require.NotNil(t, report.RequiresProviderRequest)
	require.True(t, *report.RequiresProviderRequest)
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)
	require.Contains(t, report.OutputFields, "tool_calls")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"speak", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "speak", report.Topic)
	require.Equal(t, "speak", report.Command)
	require.Contains(t, report.Help, "text-to-speech")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)
	require.Contains(t, report.OutputFields, "text_preview")
	require.Contains(t, report.StatusValues, "error")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"acp", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "acp", report.Topic)
	require.Equal(t, "acp", report.Command)
	require.Contains(t, report.Help, "stdio JSON-RPC")
	require.Contains(t, report.Aliases, "--acp")
	require.Contains(t, report.Formats, "json")
	require.Contains(t, report.OutputFields, "protocol")
	require.Contains(t, report.ProtocolFields, "methods")
	require.Contains(t, report.ContractFields, "unsupported_invocation_kind")
	require.Contains(t, report.ProtocolMethods, "session/list")
	require.NotNil(t, report.ServeStartsDaemon)
	require.True(t, *report.ServeStartsDaemon)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"reasoning", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "reasoning", report.Topic)
	require.Equal(t, "reasoning", report.Command)
	require.Contains(t, report.Help, "reasoning effort")
	require.Contains(t, report.OutputFields, "effort")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"rate-limit", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "rate-limit", report.Topic)
	require.Equal(t, "rate-limit", report.Command)
	require.Contains(t, report.Help, "retry and backoff")
	require.Contains(t, report.OutputFields, "max_retries")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"budget", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "budget", report.Topic)
	require.Equal(t, "budget", report.Command)
	require.Contains(t, report.Help, "token budget")
	require.Contains(t, report.OutputFields, "max_tokens")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"profile", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "profile", report.Topic)
	require.Equal(t, "profile", report.Command)
	require.Contains(t, report.Help, "OAuth provider profile")
	require.Contains(t, report.OutputFields, "active_profile")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"oauth", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "oauth", report.Topic)
	require.Equal(t, "oauth", report.Command)
	require.Contains(t, report.Help, "stored tokens")
	require.Contains(t, report.OutputFields, "token_present")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"metrics", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "metrics", report.Topic)
	require.Equal(t, "metrics", report.Command)
	require.Contains(t, report.Help, "usage metrics")
	require.Contains(t, report.OutputFields, "workspace_metrics")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"workspace", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "workspace", report.Topic)
	require.Equal(t, "workspace", report.Command)
	require.Contains(t, report.Help, "runtime workspace")
	require.Contains(t, report.OutputFields, "session_dir")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"clear", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "clear", report.Topic)
	require.Equal(t, "clear", report.Command)
	require.Contains(t, report.Help, "fresh empty local session")
	require.Contains(t, report.OutputFields, "continue_commands")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"reset", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "reset", report.Topic)
	require.Equal(t, "reset", report.Command)
	require.Contains(t, report.Help, "configuration sections")
	require.Contains(t, report.OutputFields, "reset_keys")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"language", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "language", report.Topic)
	require.Equal(t, "language", report.Command)
	require.Contains(t, report.Help, "interface language")
	require.Contains(t, report.OutputFields, "language")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"state", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "state", report.Topic)
	require.Equal(t, "state", report.Command)
	require.Contains(t, report.Help, "Produces state")
	require.Contains(t, report.Help, "codog prompt <text>")
	require.Contains(t, report.OutputFields, "worker_id")
	require.Contains(t, report.StatusValues, "running")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"code-intel", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "code-intel", report.Topic)
	require.Equal(t, "code-intel", report.Command)
	require.Contains(t, report.Help, "notebook-read")
	require.Contains(t, report.Help, "lsp")
	require.Contains(t, report.OutputFields, "symbols")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"notebook-edit", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "notebook-edit", report.Topic)
	require.Equal(t, "notebook-edit", report.Command)
	require.Contains(t, report.Help, "code-intel notebook-edit")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)
}

func TestCommandHelpShortCircuitsBeforeConfigLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	cases := []struct {
		name  string
		args  []string
		topic string
	}{
		{
			name:  "doctor global format",
			args:  []string{"--config", configPath, "--output-format", "json", "doctor", "--help"},
			topic: "doctor",
		},
		{
			name:  "compact suffix format",
			args:  []string{"--config", configPath, "compact", "--help", "--output-format", "json"},
			topic: "compact",
		},
		{
			name:  "session suffix format",
			args:  []string{"--config", configPath, "session", "--help", "--output-format", "json"},
			topic: "session",
		},
		{
			name:  "resume global flag",
			args:  []string{"--resume", "--help", "--output-format", "json"},
			topic: "resume",
		},
		{
			name:  "resume short global flag",
			args:  []string{"-r", "--help", "--output-format", "json"},
			topic: "resume",
		},
		{
			name:  "continue global flag",
			args:  []string{"--continue", "--help", "--output-format", "json"},
			topic: "resume",
		},
		{
			name:  "continue short global flag",
			args:  []string{"-c", "--help", "--output-format", "json"},
			topic: "resume",
		},
		{
			name:  "prompt provider help",
			args:  []string{"--config", configPath, "prompt", "--help", "--output-format", "json"},
			topic: "prompt",
		},
		{
			name:  "code intel help",
			args:  []string{"--config", configPath, "code-intel", "--help", "--output-format", "json"},
			topic: "code-intel",
		},
		{
			name:  "slash notebook read help",
			args:  []string{"--config", configPath, "/notebook-read", "--help", "--output-format", "json"},
			topic: "notebook-read",
		},
		{
			name:  "speak local help",
			args:  []string{"--config", configPath, "speak", "--help", "--output-format", "json"},
			topic: "speak",
		},
		{
			name:  "mcp local help",
			args:  []string{"--config", configPath, "mcp", "--help", "--output-format", "json"},
			topic: "mcp",
		},
		{
			name:  "good claude local help",
			args:  []string{"--config", configPath, "good-claude", "--help", "--output-format", "json"},
			topic: "good-claude",
		},
		{
			name:  "setupGitHubActions local help",
			args:  []string{"--config", configPath, "setupGitHubActions", "--help", "--output-format", "json"},
			topic: "setupGitHubActions",
		},
		{
			name:  "api local help",
			args:  []string{"--config", configPath, "api", "--help", "--output-format", "json"},
			topic: "api",
		},
		{
			name:  "prefetch local help",
			args:  []string{"--config", configPath, "prefetch", "--help", "--output-format", "json"},
			topic: "prefetch",
		},
		{
			name:  "models local help",
			args:  []string{"--config", configPath, "models", "--help", "--output-format", "json"},
			topic: "models",
		},
		{
			name:  "cache local help",
			args:  []string{"--config", configPath, "cache", "--help", "--output-format", "json"},
			topic: "cache",
		},
		{
			name:  "caches local help",
			args:  []string{"--config", configPath, "caches", "--help", "--output-format", "json"},
			topic: "caches",
		},
		{
			name:  "validation local help",
			args:  []string{"--config", configPath, "validation", "--help", "--output-format", "json"},
			topic: "validation",
		},
		{
			name:  "reviewRemote local help",
			args:  []string{"--config", configPath, "reviewRemote", "--help", "--output-format", "json"},
			topic: "reviewRemote",
		},
		{
			name:  "autofix-pr local help",
			args:  []string{"--config", configPath, "autofix-pr", "--help", "--output-format", "json"},
			topic: "autofix-pr",
		},
		{
			name:  "context-noninteractive local help",
			args:  []string{"--config", configPath, "context-noninteractive", "--help", "--output-format", "json"},
			topic: "context-noninteractive",
		},
		{
			name:  "conversation local help",
			args:  []string{"--config", configPath, "conversation", "--help", "--output-format", "json"},
			topic: "conversation",
		},
		{
			name:  "break-cache local help",
			args:  []string{"--config", configPath, "break-cache", "--help", "--output-format", "json"},
			topic: "break-cache",
		},
		{
			name:  "paste local help",
			args:  []string{"--config", configPath, "paste", "--help", "--output-format", "json"},
			topic: "paste",
		},
		{
			name:  "pin local help",
			args:  []string{"--config", configPath, "pin", "--help", "--output-format", "json"},
			topic: "pin",
		},
		{
			name:  "unpin local help",
			args:  []string{"--config", configPath, "unpin", "--help", "--output-format", "json"},
			topic: "unpin",
		},
		{
			name:  "extra-usage core local help",
			args:  []string{"--config", configPath, "extra-usage-core", "--help", "--output-format", "json"},
			topic: "extra-usage-core",
		},
		{
			name:  "extra-usage noninteractive local help",
			args:  []string{"--config", configPath, "extra-usage-noninteractive", "--help", "--output-format", "json"},
			topic: "extra-usage-noninteractive",
		},
		{
			name:  "notifications local help",
			args:  []string{"--config", configPath, "notifications", "--help", "--output-format", "json"},
			topic: "notifications",
		},
		{
			name:  "api-key local help",
			args:  []string{"--config", configPath, "api-key", "--help", "--output-format", "json"},
			topic: "api-key",
		},
		{
			name:  "temperature local help",
			args:  []string{"--config", configPath, "temperature", "--help", "--output-format", "json"},
			topic: "temperature",
		},
		{
			name:  "telemetry local help",
			args:  []string{"--config", configPath, "telemetry", "--help", "--output-format", "json"},
			topic: "telemetry",
		},
		{
			name:  "effort local help",
			args:  []string{"--config", configPath, "effort", "--help", "--output-format", "json"},
			topic: "effort",
		},
		{
			name:  "reasoning local help",
			args:  []string{"--config", configPath, "reasoning", "--help", "--output-format", "json"},
			topic: "reasoning",
		},
		{
			name:  "rate-limit local help",
			args:  []string{"--config", configPath, "rate-limit", "--help", "--output-format", "json"},
			topic: "rate-limit",
		},
		{
			name:  "ant-trace provider help",
			args:  []string{"--config", configPath, "ant-trace", "--help", "--output-format", "json"},
			topic: "ant-trace",
		},
		{
			name:  "budget local help",
			args:  []string{"--config", configPath, "budget", "--help", "--output-format", "json"},
			topic: "budget",
		},
		{
			name:  "profile local help",
			args:  []string{"--config", configPath, "profile", "--help", "--output-format", "json"},
			topic: "profile",
		},
		{
			name:  "metrics local help",
			args:  []string{"--config", configPath, "metrics", "--help", "--output-format", "json"},
			topic: "metrics",
		},
		{
			name:  "perf-issue local help",
			args:  []string{"--config", configPath, "perf-issue", "--help", "--output-format", "json"},
			topic: "perf-issue",
		},
		{
			name:  "reset local help",
			args:  []string{"--config", configPath, "reset", "--help", "--output-format", "json"},
			topic: "reset",
		},
		{
			name:  "settings alias help",
			args:  []string{"--config", configPath, "settings", "--help", "--output-format", "json"},
			topic: "settings",
		},
		{
			name:  "workspace local help",
			args:  []string{"--config", configPath, "workspace", "--help", "--output-format", "json"},
			topic: "workspace",
		},
		{
			name:  "memory local help",
			args:  []string{"--config", configPath, "memory", "--help", "--output-format", "json"},
			topic: "memory",
		},
		{
			name:  "keybindings local help",
			args:  []string{"--config", configPath, "keybindings", "--help", "--output-format", "json"},
			topic: "keybindings",
		},
		{
			name:  "language local help",
			args:  []string{"--config", configPath, "language", "--help", "--output-format", "json"},
			topic: "language",
		},
		{
			name:  "rate-limit-options local help",
			args:  []string{"--config", configPath, "rate-limit-options", "--help", "--output-format", "json"},
			topic: "rate-limit-options",
		},
		{
			name:  "mock-limits local help",
			args:  []string{"--config", configPath, "mock-limits", "--help", "--output-format", "json"},
			topic: "mock-limits",
		},
		{
			name:  "reset-limits local help",
			args:  []string{"--config", configPath, "reset-limits", "--help", "--output-format", "json"},
			topic: "reset-limits",
		},
		{
			name:  "generateSessionName local help",
			args:  []string{"--config", configPath, "generateSessionName", "--help", "--output-format", "json"},
			topic: "generateSessionName",
		},
		{
			name:  "onboarding local help",
			args:  []string{"--config", configPath, "onboarding", "--help", "--output-format", "json"},
			topic: "onboarding",
		},
		{
			name:  "state local help",
			args:  []string{"--config", configPath, "state", "--help", "--output-format", "json"},
			topic: "state",
		},
		{
			name:  "bookmarks local help",
			args:  []string{"--config", configPath, "bookmarks", "--help", "--output-format", "json"},
			topic: "bookmarks",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), tc.args, config.FlagOverrides{})
			})
			require.NoError(t, err)
			var report helpReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "help", report.Kind)
			require.Equal(t, "help", report.Action)
			require.Equal(t, "ok", report.Status)
			require.Equal(t, tc.topic, report.Topic)
			require.NotContains(t, out, "config_parse_error")
			require.NotContains(t, out, "missing_credentials")
		})
	}

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "doctor", "--help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "Doctor\n"))
	require.Contains(t, out, "no provider request or session resume required")
}

func TestCapabilitiesCommandOutputsTextAndJSON(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "claude-test",
			PermissionMode:      "read-only",
			AutoCompactMessages: 12,
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Capabilities(nil))
	require.Contains(t, out.String(), "Codog Capabilities")
	require.Contains(t, out.String(), "Resume-safe")
	require.Contains(t, out.String(), "Tool aliases")
	require.Contains(t, out.String(), "MCP local data")
	require.Contains(t, out.String(), "Mock parity")
	require.Contains(t, out.String(), "Terminal parity")
	require.Contains(t, out.String(), "Bridge parity")
	require.Contains(t, out.String(), "Orchestration")
	require.Contains(t, out.String(), "Release hardening")
	out.Reset()

	require.NoError(t, app.Capabilities([]string{"--json"}))
	var report capabilitiesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "capabilities", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "claude-test", report.Model)
	require.Equal(t, "read-only", report.PermissionMode)
	require.Equal(t, "ready", report.Terminal.Status)
	require.True(t, report.Terminal.TUISubmitSupported)
	require.True(t, report.Terminal.TUISlashCompletion)
	require.True(t, report.Terminal.PermissionCommandsPresent)
	require.Empty(t, report.Terminal.MissingRequiredCommands)
	require.Equal(t, "ready", report.Bridge.Status)
	require.Empty(t, report.Bridge.MissingBridgeMethods)
	require.Empty(t, report.Bridge.MissingControlRoutes)
	require.False(t, report.Bridge.RemoteAuthConfigured)
	require.Contains(t, report.Bridge.ExcludedExternalSurfaces, "official_remote_auth")
	require.Contains(t, report.Bridge.ExcludedExternalSurfaces, "official_ide_bridge_auth")
	require.Equal(t, "ready", report.Orchestration.Status)
	require.True(t, report.Orchestration.AgentStoreReady)
	require.True(t, report.Orchestration.PluginDiscoveryReady)
	require.True(t, report.Orchestration.MCPLifecycleReady)
	require.NotEmpty(t, report.Release.Platform)
	if sandbox.Detect().Available {
		require.Equal(t, "ready", report.Release.Status)
		require.NotContains(t, report.Release.MissingProductionSurfaces, "sandbox_available")
	} else {
		require.Equal(t, "degraded", report.Release.Status)
		require.Contains(t, report.Release.MissingProductionSurfaces, "sandbox_available")
	}
	require.NotContains(t, report.Release.MissingProductionSurfaces, "updater_manifest")
	require.NotContains(t, report.Release.MissingProductionSurfaces, "managed_policy")
	require.Contains(t, report.Release.ExcludedProductionSurfaces, "updater_manifest")
	require.Contains(t, report.Release.ExcludedProductionSurfaces, "managed_policy")
	require.Contains(t, report.Commands, "prompt")
	require.Contains(t, report.Commands, "ant-trace")
	require.Contains(t, report.Commands, "api")
	require.Contains(t, report.Commands, "break-cache")
	require.Contains(t, report.Commands, "caches")
	require.Contains(t, report.Commands, "extra-usage-core")
	require.Contains(t, report.Commands, "extra-usage-noninteractive")
	require.Contains(t, report.Commands, "exit")
	require.Contains(t, report.Commands, "autofix-pr")
	require.Contains(t, report.Commands, "resume")
	require.Contains(t, report.Commands, "session")
	require.Contains(t, report.Commands, "clear")
	require.Contains(t, report.Commands, "context-noninteractive")
	require.Contains(t, report.Commands, "conversation")
	require.Contains(t, report.Commands, "validation")
	require.Contains(t, report.Commands, "reviewRemote")
	require.Contains(t, report.Commands, "permissions")
	require.Contains(t, report.Commands, "plan")
	require.Contains(t, report.Commands, "teleport")
	require.Contains(t, report.Commands, "bridge")
	require.Contains(t, report.Commands, "setupGitHubActions")
	require.Contains(t, report.Commands, "bridge-kick")
	require.Contains(t, report.Commands, "bootstrap-plan")
	require.Contains(t, report.Commands, "prefetch")
	require.Contains(t, report.Commands, "cron")
	require.Contains(t, report.Commands, "deferred-init")
	require.Contains(t, report.Commands, "team")
	require.Contains(t, report.Commands, "startup-report")
	require.Contains(t, report.Commands, "budget")
	require.Contains(t, report.Commands, "capabilities")
	require.Contains(t, report.Commands, "continue")
	require.Contains(t, report.Commands, "bug")
	require.Contains(t, report.Commands, "checkpoint")
	require.Contains(t, report.Commands, "generateSessionName")
	require.Contains(t, report.Commands, "good-claude")
	require.Contains(t, report.Commands, "language")
	require.Contains(t, report.Commands, "metrics")
	require.Contains(t, report.Commands, "mock-limits")
	require.Contains(t, report.Commands, "mock-parity")
	require.Contains(t, report.Commands, "onboarding")
	require.Contains(t, report.Commands, "perf-issue")
	require.Contains(t, report.Commands, "profile")
	require.Contains(t, report.Commands, "rc")
	require.Contains(t, report.Commands, "rate-limit")
	require.Contains(t, report.Commands, "reasoning")
	require.Contains(t, report.Commands, "reset")
	require.Contains(t, report.Commands, "settings")
	require.Contains(t, report.Commands, "skill")
	require.Contains(t, report.Commands, "slash")
	require.Contains(t, report.Commands, "temperature")
	require.Contains(t, report.Commands, "telemetry")
	require.Contains(t, report.Commands, "workspace")
	require.Contains(t, report.Commands, "cwd")
	require.Contains(t, report.Commands, "tool-details")
	for _, internalName := range []string{
		"AddMarketplace", "addCommand", "ApiKeyStep", "BrowseMarketplace",
		"CheckExistingSecretStep", "CheckGitHubStep", "ChooseRepoStep", "CreatingStep",
		"createMovedToPluginCommand", "DiscoverPlugins", "ErrorStep", "ExistingWorkflowStep", "InstallAppStep",
		"ManageMarketplaces", "ManagePlugins", "OAuthFlowStep", "parseArgs",
		"pluginDetailsHelpers", "PluginErrors", "PluginOptionsDialog", "PluginOptionsFlow",
		"PluginSettings", "PluginTrustWarning", "SuccessStep", "ultrareviewCommand",
		"ultrareviewEnabled", "UltrareviewOverageDialog", "UnifiedInstalledCell",
		"usePagination", "ValidatePlugin", "WarningsStep", "xaaIdpCommand",
		"audit", "parity-audit", "reference-audit",
	} {
		require.NotContains(t, report.Commands, internalName)
	}
	require.Contains(t, report.Features, "approval_tokens")
	require.Contains(t, report.Features, "ask_user_question_multi_select")
	require.Contains(t, report.Features, "ask_user_question_previews")
	require.Contains(t, report.Features, "ask_user_question_tabs")
	require.Contains(t, report.Features, "broad_cwd_guard")
	require.Contains(t, report.Features, "bootstrap_plan")
	require.Contains(t, report.Features, "config_load_degraded")
	require.Contains(t, report.Features, "config_reset")
	require.Contains(t, report.Features, "command_surface_audit")
	require.Contains(t, report.Features, "deferred_init")
	require.Contains(t, report.Features, "doctor_config_load_degraded")
	require.Contains(t, report.Features, "doctor_config_validation")
	require.Contains(t, report.Features, "dynamic_tool_loading")
	require.Contains(t, report.Features, "execution_registry_resolve")
	require.Contains(t, report.Features, "hooks_health")
	require.Contains(t, report.Features, "interface_language")
	require.Contains(t, report.Features, "lane_event_projection")
	require.Contains(t, report.Features, "mcp_server")
	require.Contains(t, report.Features, "mcp_config_load_degraded")
	require.Contains(t, report.Features, "memory_age_scan")
	require.Contains(t, report.Features, "metrics")
	require.Contains(t, report.Features, "mock_parity_harness")
	require.Contains(t, report.Features, "policy_engine")
	require.Contains(t, report.Features, "plugin_lifecycle")
	require.Contains(t, report.Features, "plugins_config_load_degraded")
	require.Contains(t, report.Features, "providers_config_load_degraded")
	require.Contains(t, report.Features, "sampling_temperature")
	require.Contains(t, report.Features, "recovery_recipes_ledger")
	require.Contains(t, report.Features, "resume_safe_slash_metadata")
	require.Contains(t, report.Features, "session_identity_metadata")
	require.Contains(t, report.Features, "session_identity_reconciliation")
	require.Contains(t, report.Features, "stale_branch_guard")
	require.Contains(t, report.Features, "status_boot_preflight")
	require.Contains(t, report.Features, "status_boot_required_binaries")
	require.Contains(t, report.Features, "status_config_load_degraded")
	require.Contains(t, report.Features, "status_config_validation")
	require.Contains(t, report.Features, "team_watch")
	require.Contains(t, report.Features, "telemetry_preferences")
	require.Contains(t, report.Features, "tui_first_run_theme_onboarding")
	require.Contains(t, report.Features, "tui_live_theme_picker")
	require.Contains(t, report.Features, "tui_no_color_theme")
	require.Contains(t, report.Features, "tui_permission_picker")
	require.Contains(t, report.Features, "tui_question_picker")
	require.Contains(t, report.Features, "tui_structured_tool_activity")
	require.Contains(t, report.Features, "tui_permission_feedback")
	require.Contains(t, report.Features, "tui_permission_rule_edit")
	require.Contains(t, report.Features, "tui_tool_activity_in_place")
	require.Contains(t, report.Features, "tui_tool_output_expand")
	require.Contains(t, report.Features, "permission_feedback_model_bridge")
	require.Contains(t, report.Features, "tool_search_mcp_degraded")
	require.Contains(t, report.Features, "typed_task_packets")
	require.Contains(t, report.Features, "worker_startup_no_evidence")
	require.Contains(t, report.Features, "workspace_switch")
	require.Contains(t, report.Protocols, "mcp_stdio_server")
	require.Contains(t, report.OutputFormats, "stream-json")
	require.Equal(t, "ready", report.CommandSurface.Status)
	require.Equal(t, report.CommandCount, report.CommandSurface.CommandCount)
	require.Equal(t, report.CommandCount, report.CommandSurface.HelpTopicCount)
	require.Zero(t, report.CommandSurface.MissingHelpTopicCount)
	require.Empty(t, report.CommandSurface.MissingHelpTopics)
	require.Zero(t, report.CommandSurface.FallbackHelpTopicCount)
	require.Empty(t, report.CommandSurface.FallbackHelpTopics)
	require.Equal(t, report.CommandCount, report.CommandSurface.CompletionCommandCount)
	require.Zero(t, report.CommandSurface.MissingCompletionCommandCount)
	require.Empty(t, report.CommandSurface.MissingCompletionCommands)
	require.Contains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "tui")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "cost")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "tokens")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "history")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "prompt-history")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "summary")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "rename")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "enterprise")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "git")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "install-github-app")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "oauth")
	require.NotContains(t, report.CommandSurface.NoGlobalOutputFormatCommands, "oauth-refresh")
	require.Greater(t, report.CommandCount, 20)
	require.Greater(t, report.SlashCommandCount, 20)
	require.Greater(t, report.ResumeSafeSlashCount, 20)
	require.Equal(t, report.ResumeSafeSlashCount, len(report.ResumeSafeSlashCommands))
	require.ElementsMatch(t, capabilityReportResumeSafeSlashNames(report), report.ResumeSafeSlashCommands)
	require.Contains(t, report.ResumeSafeSlashCommands, "/status")
	require.Contains(t, report.ResumeSafeSlashCommands, "/capabilities")
	require.Contains(t, report.ResumeSafeSlashCommands, "/mock-parity")
	require.Equal(t, harness.ManifestSchemaVersion, report.MockParity.SchemaVersion)
	require.Equal(t, harness.ScenarioManifest().ScenarioCount, report.MockParity.ScenarioCount)
	require.GreaterOrEqual(t, len(report.MockParity.Categories), 8)
	require.Equal(t, "streaming_text", report.MockParity.Scenarios[0].Name)
	require.Greater(t, report.ToolCount, 10)
	require.Greater(t, report.ToolAliasCount, 40)
	statusSlash, ok := capabilityReportSlash(report, "/status")
	require.True(t, ok)
	require.True(t, statusSlash.ResumeSupported)
	conversationSlash, ok := capabilityReportSlash(report, "/conversation")
	require.True(t, ok)
	require.True(t, conversationSlash.ResumeSupported)
	advisorSlash, ok := capabilityReportSlash(report, "/advisor")
	require.True(t, ok)
	require.True(t, advisorSlash.ResumeSupported)
	systemPromptSlash, ok := capabilityReportSlash(report, "/system-prompt")
	require.True(t, ok)
	require.True(t, systemPromptSlash.ResumeSupported)
	toolDetailsSlash, ok := capabilityReportSlash(report, "/tool-details")
	require.True(t, ok)
	require.True(t, toolDetailsSlash.ResumeSupported)
	debugToolCallSlash, ok := capabilityReportSlash(report, "/debug-tool-call")
	require.True(t, ok)
	require.True(t, debugToolCallSlash.ResumeSupported)
	oauthSlash, ok := capabilityReportSlash(report, "/oauth")
	require.True(t, ok)
	require.True(t, oauthSlash.ResumeSupported)
	cronSlash, ok := capabilityReportSlash(report, "/cron")
	require.True(t, ok)
	require.True(t, cronSlash.ResumeSupported)
	teamSlash, ok := capabilityReportSlash(report, "/team")
	require.True(t, ok)
	require.True(t, teamSlash.ResumeSupported)
	hooksSlash, ok := capabilityReportSlash(report, "/hooks")
	require.True(t, ok)
	require.True(t, hooksSlash.ResumeSupported)
	skillSlash, ok := capabilityReportSlash(report, "/skill")
	require.True(t, ok)
	require.True(t, skillSlash.ResumeSupported)
	resetSlash, ok := capabilityReportSlash(report, "/reset")
	require.True(t, ok)
	require.True(t, resetSlash.ResumeSupported)
	planSlash, ok := capabilityReportSlash(report, "/plan")
	require.True(t, ok)
	require.True(t, planSlash.ResumeSupported)
	ultraPlanSlash, ok := capabilityReportSlash(report, "/ultraplan")
	require.True(t, ok)
	require.True(t, ultraPlanSlash.ResumeSupported)
	exitPlanSlash, ok := capabilityReportSlash(report, "/exit-plan")
	require.True(t, ok)
	require.True(t, exitPlanSlash.ResumeSupported)
	exitPlanModeSlash, ok := capabilityReportSlash(report, "/exit_plan_mode")
	require.True(t, ok)
	require.True(t, exitPlanModeSlash.ResumeSupported)
	setupSlash, ok := capabilityReportSlash(report, "/setup")
	require.True(t, ok)
	require.True(t, setupSlash.ResumeSupported)
	sandboxToggleSlash, ok := capabilityReportSlash(report, "/sandbox-toggle")
	require.True(t, ok)
	require.True(t, sandboxToggleSlash.ResumeSupported)
	speakSlash, ok := capabilityReportSlash(report, "/speak")
	require.True(t, ok)
	require.True(t, speakSlash.ResumeSupported)
	listenSlash, ok := capabilityReportSlash(report, "/listen")
	require.True(t, ok)
	require.True(t, listenSlash.ResumeSupported)
	terminalSetupSlash, ok := capabilityReportSlash(report, "/terminal-setup")
	require.True(t, ok)
	require.True(t, terminalSetupSlash.ResumeSupported)
	terminalSetupAliasSlash, ok := capabilityReportSlash(report, "/terminalSetup")
	require.True(t, ok)
	require.True(t, terminalSetupAliasSlash.ResumeSupported)
	remoteEnvSlash, ok := capabilityReportSlash(report, "/remote-env")
	require.True(t, ok)
	require.True(t, remoteEnvSlash.ResumeSupported)
	remoteSetupSlash, ok := capabilityReportSlash(report, "/remote-setup")
	require.True(t, ok)
	require.True(t, remoteSetupSlash.ResumeSupported)
	webSetupSlash, ok := capabilityReportSlash(report, "/web-setup")
	require.True(t, ok)
	require.True(t, webSetupSlash.ResumeSupported)
	voiceSlash, ok := capabilityReportSlash(report, "/voice")
	require.True(t, ok)
	require.True(t, voiceSlash.ResumeSupported)
	extraUsageSlash, ok := capabilityReportSlash(report, "/extra-usage")
	require.True(t, ok)
	require.True(t, extraUsageSlash.ResumeSupported)
	installSlackAppSlash, ok := capabilityReportSlash(report, "/install-slack-app")
	require.True(t, ok)
	require.True(t, installSlackAppSlash.ResumeSupported)
	stickersSlash, ok := capabilityReportSlash(report, "/stickers")
	require.True(t, ok)
	require.True(t, stickersSlash.ResumeSupported)
	passesSlash, ok := capabilityReportSlash(report, "/passes")
	require.True(t, ok)
	require.True(t, passesSlash.ResumeSupported)
	pasteSlash, ok := capabilityReportSlash(report, "/paste")
	require.True(t, ok)
	require.True(t, pasteSlash.ResumeSupported)
	pinSlash, ok := capabilityReportSlash(report, "/pin")
	require.True(t, ok)
	require.True(t, pinSlash.ResumeSupported)
	unpinSlash, ok := capabilityReportSlash(report, "/unpin")
	require.True(t, ok)
	require.True(t, unpinSlash.ResumeSupported)
	thinkBackSlash, ok := capabilityReportSlash(report, "/think-back")
	require.True(t, ok)
	require.True(t, thinkBackSlash.ResumeSupported)
	thinkbackSlash, ok := capabilityReportSlash(report, "/thinkback")
	require.True(t, ok)
	require.True(t, thinkbackSlash.ResumeSupported)
	thinkbackPlaySlash, ok := capabilityReportSlash(report, "/thinkback-play")
	require.True(t, ok)
	require.True(t, thinkbackPlaySlash.ResumeSupported)
	sessionsSlash, ok := capabilityReportSlash(report, "/sessions")
	require.True(t, ok)
	require.True(t, sessionsSlash.ResumeSupported)
	reloadPluginsSlash, ok := capabilityReportSlash(report, "/reload-plugins")
	require.True(t, ok)
	require.True(t, reloadPluginsSlash.ResumeSupported)
	heapdumpSlash, ok := capabilityReportSlash(report, "/heapdump")
	require.True(t, ok)
	require.True(t, heapdumpSlash.ResumeSupported)
	commitSlash, ok := capabilityReportSlash(report, "/commit")
	require.True(t, ok)
	require.True(t, commitSlash.ResumeSupported)
	require.Equal(t, len(report.ToolAliases), report.ToolAliasCount)
	require.Equal(t, "read_file", report.ToolAliases["Read"])
	require.Equal(t, "read_file", report.ToolAliases["ReadFile"])
	require.Equal(t, "read_file", report.ToolAliases["FileReadTool"])
	require.Equal(t, "write_file", report.ToolAliases["WriteFile"])
	require.Equal(t, "edit_file", report.ToolAliases["EditFile"])
	require.Equal(t, "multi_edit", report.ToolAliases["MultiEditFile"])
	require.Equal(t, "bash", report.ToolAliases["BashTool"])
	require.Equal(t, "mcp", report.ToolAliases["MCP"])
	require.Equal(t, "git_status", report.ToolAliases["GitStatus"])
	require.Equal(t, "structured_output", report.ToolAliases["StructuredOutputTool"])
	require.Equal(t, "send_user_message", report.ToolAliases["SendUserMessageTool"])
	require.Equal(t, "tool_search", report.ToolAliases["ToolSearchTool"])
	require.Equal(t, "tool_search", report.ToolAliases["ToolSearch"])
	require.Equal(t, "enter_plan_mode", report.ToolAliases["EnterPlanMode"])
	require.Equal(t, "exit_plan_mode", report.ToolAliases["ExitPlanMode"])
	require.Equal(t, "sleep", report.ToolAliases["SleepTool"])
	require.Equal(t, "repl", report.ToolAliases["REPLTool"])
	require.Equal(t, "git_blame", report.ToolAliases["GitBlameTool"])
	require.Equal(t, "git_diff", report.ToolAliases["GitDiffTool"])
	require.Equal(t, "git_log", report.ToolAliases["GitLogTool"])
	require.Equal(t, "git_show", report.ToolAliases["GitShowTool"])
	readTool, ok := capabilityReportTool(report, "read_file")
	require.True(t, ok)
	require.Contains(t, readTool.Aliases, "Read")
	require.Contains(t, readTool.Aliases, "ReadFile")
	require.Contains(t, readTool.Aliases, "FileReadTool")
	writeTool, ok := capabilityReportTool(report, "write_file")
	require.True(t, ok)
	require.Contains(t, writeTool.Aliases, "WriteFile")
	editTool, ok := capabilityReportTool(report, "edit_file")
	require.True(t, ok)
	require.Contains(t, editTool.Aliases, "EditFile")
	patchTool, ok := capabilityReportTool(report, "apply_patch")
	require.True(t, ok)
	require.Contains(t, patchTool.Aliases, "ApplyPatch")
	bashTool, ok := capabilityReportTool(report, "bash")
	require.True(t, ok)
	require.Contains(t, bashTool.Aliases, "Bash")
	require.Contains(t, bashTool.Aliases, "BashTool")
	enterPlanTool, ok := capabilityReportTool(report, "enter_plan_mode")
	require.True(t, ok)
	require.Contains(t, enterPlanTool.Aliases, "EnterPlanMode")
	exitPlanTool, ok := capabilityReportTool(report, "exit_plan_mode")
	require.True(t, ok)
	require.Contains(t, exitPlanTool.Aliases, "ExitPlanMode")
	sleepTool, ok := capabilityReportTool(report, "sleep")
	require.True(t, ok)
	require.Contains(t, sleepTool.Aliases, "SleepTool")
	replTool, ok := capabilityReportTool(report, "repl")
	require.True(t, ok)
	require.Contains(t, replTool.Aliases, "REPLTool")
	gitDiffTool, ok := capabilityReportTool(report, "git_diff")
	require.True(t, ok)
	require.Contains(t, gitDiffTool.Aliases, "GitDiffTool")
	gitLogTool, ok := capabilityReportTool(report, "git_log")
	require.True(t, ok)
	require.Contains(t, gitLogTool.Aliases, "GitLogTool")
	mcpTool, ok := capabilityReportTool(report, "mcp")
	require.True(t, ok)
	require.Contains(t, mcpTool.Aliases, "MCP")
	structuredTool, ok := capabilityReportTool(report, "structured_output")
	require.True(t, ok)
	require.Contains(t, structuredTool.Aliases, "StructuredOutputTool")
	require.Equal(t, 3, report.MCP.LocalResourceCount)
	require.Equal(t, 1, report.MCP.LocalTemplateCount)
	require.Equal(t, 3, report.MCP.LocalPromptCount)
	require.Greater(t, report.MCP.ExposedToolCount, 10)
	require.True(t, capabilityReportHasTool(report, "read_file"))
	require.True(t, capabilityReportHasSlash(report, "/ant-trace"))
	require.True(t, capabilityReportHasSlash(report, "/bug"))
	backfillSlash, ok := capabilityReportSlash(report, "/backfill-sessions")
	require.True(t, ok)
	require.True(t, backfillSlash.ResumeSupported)
	modelsSlash, ok := capabilityReportSlash(report, "/models")
	require.True(t, ok)
	require.True(t, modelsSlash.ResumeSupported)
	subagentSlash, ok := capabilityReportSlash(report, "/subagent")
	require.True(t, ok)
	require.True(t, subagentSlash.ResumeSupported)
	require.True(t, capabilityReportHasSlash(report, "/generateSessionName"))
	generateSessionNameSlash, ok := capabilityReportSlash(report, "/generateSessionName")
	require.True(t, ok)
	require.True(t, generateSessionNameSlash.ResumeSupported)
	require.True(t, capabilityReportHasSlash(report, "/onboarding"))
	require.True(t, capabilityReportHasSlash(report, "/capabilities"))
	require.True(t, capabilityReportHasSlash(report, "/checkpoint"))
	require.True(t, capabilityReportHasSlash(report, "/bookmarks"))
	feedbackSlash, ok := capabilityReportSlash(report, "/feedback")
	require.True(t, ok)
	require.True(t, feedbackSlash.ResumeSupported)
	prSlash, ok := capabilityReportSlash(report, "/pr")
	require.True(t, ok)
	require.True(t, prSlash.ResumeSupported)
	issueSlash, ok := capabilityReportSlash(report, "/issue")
	require.True(t, ok)
	require.True(t, issueSlash.ResumeSupported)
	reviewRemoteSlash, ok := capabilityReportSlash(report, "/reviewRemote")
	require.True(t, ok)
	require.True(t, reviewRemoteSlash.ResumeSupported)
	autofixPRSlash, ok := capabilityReportSlash(report, "/autofix-pr")
	require.True(t, ok)
	require.True(t, autofixPRSlash.ResumeSupported)
	prCommentsSlash, ok := capabilityReportSlash(report, "/pr-comments")
	require.True(t, ok)
	require.True(t, prCommentsSlash.ResumeSupported)
	require.True(t, capabilityReportHasSlash(report, "/new"))
	require.True(t, capabilityReportHasSlash(report, "/continue"))
	require.True(t, capabilityReportHasSlash(report, "/quit"))
	require.True(t, capabilityReportHasSlash(report, "/rc"))
	require.True(t, capabilityReportHasSlash(report, "/settings"))
	require.True(t, capabilityReportHasSlash(report, "/skill"))
	require.True(t, capabilityReportHasSlash(report, "/workspace"))
	require.True(t, capabilityReportHasSlash(report, "/good-claude"))
	require.True(t, capabilityReportHasMCPResource(report, "codog://workspace"))
	require.True(t, capabilityReportHasMCPPrompt(report, "review_changes"))
	require.True(t, commandAcceptsGlobalOutputFormat("ant-trace"))
	require.True(t, commandAcceptsGlobalOutputFormat("capabilities"))
	require.True(t, commandAcceptsGlobalOutputFormat("code-intel"))
	require.True(t, commandAcceptsGlobalOutputFormat("completion"))
	require.True(t, commandAcceptsGlobalOutputFormat("generateSessionName"))
	require.True(t, commandAcceptsGlobalOutputFormat("good-claude"))
	require.True(t, commandAcceptsGlobalOutputFormat("onboarding"))
	require.True(t, commandAcceptsGlobalOutputFormat("settings"))
	require.True(t, commandAcceptsGlobalOutputFormat("skill"))
	require.True(t, commandAcceptsGlobalOutputFormat("slash"))
	for _, internalName := range []string{
		"AddMarketplace", "addCommand", "ApiKeyStep", "BrowseMarketplace",
		"CheckGitHubStep", "CreatingStep", "DiscoverPlugins", "ExistingWorkflowStep",
		"ManageMarketplaces", "ManagePlugins", "PluginErrors", "PluginSettings",
		"PluginTrustWarning", "SuccessStep", "ultrareviewEnabled", "ValidatePlugin",
		"xaaIdpCommand", "audit", "parity-audit", "reference-audit",
	} {
		require.False(t, commandAcceptsGlobalOutputFormat(internalName))
	}
	require.True(t, commandAcceptsGlobalOutputFormat("bug"))
	require.True(t, commandAcceptsGlobalOutputFormat("bookmarks"))
	require.True(t, commandAcceptsGlobalOutputFormat("bridge"))
	require.True(t, commandAcceptsGlobalOutputFormat("bridge-kick"))
	require.True(t, commandAcceptsGlobalOutputFormat("bootstrap-plan"))
	require.True(t, commandAcceptsGlobalOutputFormat("prefetch"))
	require.True(t, commandAcceptsGlobalOutputFormat("checkpoint"))
	require.True(t, commandAcceptsGlobalOutputFormat("deferred-init"))
	require.True(t, commandAcceptsGlobalOutputFormat("definition"))
	require.True(t, commandAcceptsGlobalOutputFormat("diagnostics"))
	require.True(t, commandAcceptsGlobalOutputFormat("format"))
	require.True(t, commandAcceptsGlobalOutputFormat("hover"))
	require.True(t, commandAcceptsGlobalOutputFormat("ide"))
	require.True(t, commandAcceptsGlobalOutputFormat("install"))
	require.True(t, commandAcceptsGlobalOutputFormat("map"))
	require.True(t, commandAcceptsGlobalOutputFormat("remote"))
	require.True(t, commandAcceptsGlobalOutputFormat("notebook-read"))
	require.True(t, commandAcceptsGlobalOutputFormat("notebook-edit"))
	require.True(t, commandAcceptsGlobalOutputFormat("references"))
	require.True(t, commandAcceptsGlobalOutputFormat("symbols"))
	require.True(t, commandAcceptsGlobalOutputFormat("teleport"))
	require.True(t, commandAcceptsGlobalOutputFormat("startup-report"))
	require.True(t, commandAcceptsGlobalOutputFormat("ultraplan"))
	require.True(t, commandAcceptsGlobalOutputFormat("upgrade"))
	require.True(t, commandAcceptsGlobalOutputFormat("workspace"))
	require.True(t, commandAcceptsGlobalOutputFormat("cwd"))

	commandSnapshot := filepath.Join(t.TempDir(), "commands.json")
	require.NoError(t, os.WriteFile(commandSnapshot, []byte(`[
		{"name":"status","source_hint":"commands/status/index.ts"},
		{"name":"reviewRemote","source_hint":"commands/reviewRemote/index.ts"},
		{"name":"missing-reference-command","source_hint":"commands/missing/index.ts"}
	]`), 0o644))
	toolSnapshot := filepath.Join(t.TempDir(), "tools.json")
	require.NoError(t, os.WriteFile(toolSnapshot, []byte(`[
		{"name":"BashTool","source_hint":"tools/BashTool/BashTool.tsx"},
		{"name":"bashSecurity","source_hint":"tools/BashTool/bashSecurity.ts"},
		{"name":"constants","source_hint":"tools/REPLTool/constants.ts"},
		{"name":"FileReadTool","source_hint":"tools/FileReadTool/FileReadTool.tsx"},
		{"name":"MissingReferenceTool","source_hint":"tools/MissingReferenceTool/index.ts"}
	]`), 0o644))
	out.Reset()
	require.NoError(t, app.Capabilities([]string{"audit", "--commands-snapshot", commandSnapshot, "--tools-snapshot", toolSnapshot, "--json"}))
	var audit referenceParityAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &audit))
	require.Equal(t, "capabilities", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "gap", audit.Status)
	require.NotNil(t, audit.Commands)
	require.Equal(t, 3, audit.Commands.ReferenceCount)
	require.Equal(t, 2, audit.Commands.CoveredCount)
	require.Equal(t, 1, audit.Commands.MissingCount)
	require.Equal(t, "missing-reference-command", audit.Commands.Missing[0].Name)
	require.NotNil(t, audit.Tools)
	require.Equal(t, 5, audit.Tools.ReferenceCount)
	require.Equal(t, 2, audit.Tools.CoveredCount)
	require.Equal(t, 2, audit.Tools.GroupCoveredCount)
	require.Contains(t, audit.Tools.GroupCovered, referenceAuditMatch{Name: "bashSecurity", SourceHint: "tools/BashTool/bashSecurity.ts", Matched: "bash"})
	require.Contains(t, audit.Tools.GroupCovered, referenceAuditMatch{Name: "constants", SourceHint: "tools/REPLTool/constants.ts", Matched: "repl"})
	require.Equal(t, 1, audit.Tools.UncoveredCount)
	require.Equal(t, 1, audit.Tools.MissingCount)
	require.Equal(t, "MissingReferenceTool", audit.Tools.Missing[0].Name)
	require.Equal(t, []referenceAuditGroup{{Source: "tools/MissingReferenceTool", Count: 1}}, audit.Tools.MissingGroups)
	require.Contains(t, audit.Tools.Covered, referenceAuditMatch{Name: "BashTool", SourceHint: "tools/BashTool/BashTool.tsx", Matched: "bash"})
	out.Reset()
	require.NoError(t, app.Capabilities([]string{"audit", "--commands-snapshot", commandSnapshot}))
	require.Contains(t, out.String(), "Capability Snapshot Audit")
	require.Contains(t, out.String(), "Commands")
	require.Contains(t, out.String(), "Uncovered groups")
	require.Contains(t, out.String(), "missing-reference-command")

}

func TestInternalReferenceSymbolsAreNotCLICommands(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}\n"), 0o644))

	for _, command := range []string{
		"AddMarketplace", "addCommand", "ApiKeyStep", "BrowseMarketplace",
		"CheckExistingSecretStep", "CheckGitHubStep", "ChooseRepoStep",
		"createMovedToPluginCommand", "CreatingStep", "DiscoverPlugins", "ErrorStep",
		"ExistingWorkflowStep", "InstallAppStep", "ManageMarketplaces", "ManagePlugins",
		"OAuthFlowStep", "parseArgs", "pluginDetailsHelpers", "PluginErrors",
		"PluginOptionsDialog", "PluginOptionsFlow", "PluginSettings", "PluginTrustWarning",
		"SuccessStep", "ultrareviewCommand", "ultrareviewEnabled",
		"UltrareviewOverageDialog", "UnifiedInstalledCell", "usePagination",
		"ValidatePlugin", "WarningsStep", "xaaIdpCommand",
	} {
		t.Run(command, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), []string{"--config", configPath, "--json", command, "--not-a-command-option"}, config.FlagOverrides{})
			})
			require.Error(t, err)
			var report commandNotFoundReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "command_not_found", report.Kind)
			require.Equal(t, command, report.Command)
		})
	}
}

func TestSlashCommandDiscoveryCLI(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "slash", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report slashCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "slash", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Greater(t, report.CommandCount, 100)
	require.NotEmpty(t, report.ResumeSafe)
	require.Contains(t, report.ResumeSafe, "/good-claude")
	require.True(t, slashCommandReportHasCommand(report, "/good-claude"))
	require.True(t, slashCommandReportHasCommand(report, "/status"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "slash", "candidates", "/go", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "candidates", report.Action)
	require.Equal(t, "/go", report.Query)
	require.Contains(t, report.Candidates, "/good-claude")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "help", "slash", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var help helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &help))
	require.Equal(t, "slash", help.Topic)
	require.Contains(t, help.Help, "codog slash candidates /st")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "/good-claude", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "feedback"`)
	require.Contains(t, out, `"action": "good_claude"`)
}

func TestCapabilitiesResolveProjectsExecutionRegistry(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "read-only",
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Capabilities([]string{"resolve", "BashTool", "--json"}))
	var aliasReport capabilityResolveReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aliasReport))
	require.Equal(t, "capabilities", aliasReport.Kind)
	require.Equal(t, "resolve", aliasReport.Action)
	require.Equal(t, "ok", aliasReport.Status)
	aliasMatch, ok := capabilityResolveMatch(aliasReport, "tool_alias", "BashTool")
	require.True(t, ok)
	require.Equal(t, "bash", aliasMatch.Canonical)
	require.Contains(t, aliasMatch.Aliases, "BashTool")
	require.NotEmpty(t, aliasMatch.Permission)
	out.Reset()

	require.NoError(t, app.Capabilities([]string{"resolve", "prefetch", "--json"}))
	var commandReport capabilityResolveReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &commandReport))
	require.Equal(t, "ok", commandReport.Status)
	commandMatch, ok := capabilityResolveMatch(commandReport, "command", "prefetch")
	require.True(t, ok)
	require.Contains(t, commandMatch.Usage, "codog prefetch")
	slashMatch, ok := capabilityResolveMatch(commandReport, "slash", "/prefetch")
	require.True(t, ok)
	require.True(t, slashMatch.ResumeSupported)
	out.Reset()

	require.NoError(t, app.Capabilities([]string{"resolve", "/sessions", "--json"}))
	var sessionsReport capabilityResolveReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &sessionsReport))
	require.Equal(t, "ok", sessionsReport.Status)
	sessionsMatch, ok := capabilityResolveMatch(sessionsReport, "slash", "/sessions")
	require.True(t, ok)
	require.True(t, sessionsMatch.ResumeSupported)
	require.Contains(t, sessionsMatch.Usage, "/sessions")
	out.Reset()

	require.NoError(t, app.Capabilities([]string{"resolve", "prefetxh", "--json"}))
	var missingReport capabilityResolveReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missingReport))
	require.Equal(t, "not_found", missingReport.Status)
	require.Empty(t, missingReport.Matches)
	require.Contains(t, missingReport.Suggestions, "prefetch")
}

func TestBootstrapPlanCommandOutputsTextAndJSON(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Bootstrap plan memory."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "hook.sh"), []byte("#!/bin/sh\nprintf ran > hook-ran.txt\nprintf '%s' '{\"additionalContext\":\"bootstrap context\",\"hookSpecificOutput\":{\"watchPaths\":[\"src\"]}}'\n"), 0o755))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			APIKey:         "secret",
			BaseURL:        "https://api.example.test",
			PermissionMode: "workspace-write",
			Hooks: config.HookConfig{
				SessionStart: []string{"./hook.sh"},
			},
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.BootstrapPlan(nil))
	require.Contains(t, out.String(), "Bootstrap Plan")
	require.Contains(t, out.String(), "resolve_workspace")
	require.Contains(t, out.String(), "provider_dispatch")
	require.FileExists(t, filepath.Join(workspace, "hook-ran.txt"))
	out.Reset()

	require.NoError(t, app.BootstrapPlan([]string{"--json"}))
	var report bootstrapPlanReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "bootstrap_plan", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, workspace, report.Workspace)
	require.Equal(t, report.PhaseCount, len(report.Phases))
	require.GreaterOrEqual(t, report.PhaseCount, 10)
	require.Equal(t, "ready", bootstrapPlanPhaseByName(t, report, "resolve_workspace").Status)
	require.Equal(t, "ready", bootstrapPlanPhaseByName(t, report, "load_config").Status)
	require.Equal(t, "ready", bootstrapPlanPhaseByName(t, report, "register_tools").Status)
	sessionStartPhase := bootstrapPlanPhaseByName(t, report, "run_session_start_hooks")
	require.Equal(t, "ready", sessionStartPhase.Status)
	require.Equal(t, float64(1), sessionStartPhase.Evidence["hook_count"])
	require.Equal(t, float64(1), sessionStartPhase.Evidence["executed_count"])
	require.Equal(t, "ok", sessionStartPhase.Evidence["hook_status"])
	require.Equal(t, float64(1), sessionStartPhase.Evidence["additional_context_count"])
	require.Equal(t, float64(1), sessionStartPhase.Evidence["watch_path_count"])
	require.Equal(t, "ready", bootstrapPlanPhaseByName(t, report, "provider_dispatch").Status)
	require.Equal(t, float64(1), bootstrapPlanPhaseByName(t, report, "load_memory").Evidence["file_count"])
}

func TestBootstrapPlanReportsSessionStartHookFailureAsWarning(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "hook.sh"), []byte("#!/bin/sh\necho failed >&2\nexit 3\n"), 0o755))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
			Hooks: config.HookConfig{
				SessionStart: []string{"./hook.sh"},
			},
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.BootstrapPlan([]string{"--json"}))
	var report bootstrapPlanReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "warn", report.Status)
	phase := bootstrapPlanPhaseByName(t, report, "run_session_start_hooks")
	require.Equal(t, "warn", phase.Status)
	require.Equal(t, float64(1), phase.Evidence["hook_count"])
	require.Equal(t, float64(1), phase.Evidence["executed_count"])
	require.Contains(t, phase.Evidence["error"], "hook failed")
}

func TestBootstrapPlanDegradesOnMalformedConfigFile(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "bootstrap-plan"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report bootstrapPlanReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "bootstrap_plan", report.Kind)
	require.Equal(t, "warn", report.Status)
	configPhase := bootstrapPlanPhaseByName(t, report, "load_config")
	require.Equal(t, "warn", configPhase.Status)
	require.Equal(t, "config_load_failed", configPhase.Evidence["config_load_error_kind"])
	require.Contains(t, configPhase.Evidence["config_load_error"], "broken.json")
}

func TestDeferredInitCommandReportsTrustGatedStartup(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			TrustedRoots:   []string{workspace},
			EnabledSkills:  []string{"review"},
			MCPServers:     map[string]config.MCPServerConfig{"local": {Command: "codog-test-mcp"}},
			PermissionMode: "workspace-write",
			Hooks: config.HookConfig{
				SessionStart: []string{"echo session"},
				Notification: []string{"echo notify"},
			},
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.DeferredInit("deferred-init", nil))
	require.Contains(t, out.String(), "Deferred Init")
	require.Contains(t, out.String(), "Trusted          true")
	out.Reset()

	require.NoError(t, app.DeferredInit("startup-report", []string{"--json"}))
	var report deferredInitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "deferred_init", report.Kind)
	require.Equal(t, "startup-report", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.Trusted)
	require.Equal(t, "workspace_matches_trusted_root", report.TrustReason)
	require.True(t, report.PluginInit)
	require.True(t, report.SkillInit)
	require.True(t, report.MCPPrefetch)
	require.True(t, report.SessionHooks)
	require.Equal(t, report.TaskCount, len(report.Tasks))
	require.Equal(t, "idle", deferredInitTaskByName(t, report, "plugin_init").Status)
	require.Equal(t, "enabled", deferredInitTaskByName(t, report, "skill_init").Status)
	require.Equal(t, 1, deferredInitTaskByName(t, report, "mcp_prefetch").Configured)
	require.Equal(t, "enabled", deferredInitTaskByName(t, report, "session_hooks").Status)
	require.Equal(t, "enabled", deferredInitTaskByName(t, report, "notification_hooks").Status)
	out.Reset()

	require.NoError(t, app.DeferredInit("deferred-init", []string{"run", "--json"}))
	var runReport deferredInitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runReport))
	require.Equal(t, "deferred_init", runReport.Kind)
	require.Equal(t, "run", runReport.Action)
	require.Equal(t, "ready", runReport.Status)
	require.True(t, runReport.Executed)
	require.NotNil(t, runReport.Prefetch)
	require.Equal(t, "prefetch", runReport.Prefetch.Kind)
	require.Equal(t, "deferred-init", runReport.Prefetch.Action)
	require.Equal(t, runReport.Prefetch.TaskCount, len(runReport.Prefetch.Tasks))
	require.Equal(t, "ok", prefetchTaskByName(t, *runReport.Prefetch, "project_scan").Status)
	require.Equal(t, "deferred init executed", runReport.Message)
}

func TestDeferredInitSkipsUntrustedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:    t.TempDir(),
			Model:         "claude-test",
			TrustedRoots:  []string{filepath.Join(t.TempDir(), "other")},
			EnabledSkills: []string{"review"},
			MCPServers:    map[string]config.MCPServerConfig{"local": {Command: "codog-test-mcp"}},
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.DeferredInit("deferred-init", []string{"--json"}))
	var report deferredInitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skipped", report.Status)
	require.False(t, report.Trusted)
	require.Equal(t, "workspace_not_trusted", report.TrustReason)
	require.False(t, report.PluginInit)
	require.False(t, report.SkillInit)
	require.False(t, report.MCPPrefetch)
	require.False(t, report.SessionHooks)
	require.Equal(t, "skipped", deferredInitTaskByName(t, report, "mcp_prefetch").Status)
	out.Reset()

	require.NoError(t, app.DeferredInit("deferred-init", []string{"run", "--json"}))
	var runReport deferredInitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runReport))
	require.Equal(t, "run", runReport.Action)
	require.False(t, runReport.Executed)
	require.Nil(t, runReport.Prefetch)
	require.Equal(t, "skipped", runReport.Status)
	require.Contains(t, runReport.Message, "not trusted")
}

func TestDeferredInitDegradesOnMalformedConfigFile(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "deferred-init"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report deferredInitReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "deferred_init", report.Kind)
	require.Equal(t, "warn", report.Status)
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)
	require.Contains(t, report.ConfigLoadError, "broken.json")
	require.False(t, report.PluginInit)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "deferred-init", "run", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "run", report.Action)
	require.False(t, report.Executed)
	require.Nil(t, report.Prefetch)
	require.Equal(t, "warn", report.Status)
	require.Contains(t, report.Message, "config did not load cleanly")
}

func TestPrefetchCommandReportsLocalStartupReadiness(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Prefetch memory.\n"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			MCPServers:     map[string]config.MCPServerConfig{"local": {Command: "codog-test-mcp"}},
			EnabledSkills:  []string{"review"},
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prefetch(nil))
	require.Contains(t, out.String(), "Prefetch")
	require.Contains(t, out.String(), "project_scan")
	out.Reset()

	require.NoError(t, app.Prefetch([]string{"status", "--json"}))
	var report prefetchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prefetch", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, report.TaskCount, len(report.Tasks))
	memoryScan := prefetchTaskByName(t, report, "memory_scan")
	require.Equal(t, float64(1), memoryScan.Evidence["instruction_files"])
	require.Equal(t, float64(2), memoryScan.Evidence["words"])
	require.Equal(t, float64(len("Prefetch memory.\n")), memoryScan.Evidence["bytes"])
	require.Equal(t, float64(1), prefetchTaskByName(t, report, "mcp_prefetch").Evidence["valid_count"])
	require.Equal(t, "ok", prefetchTaskByName(t, report, "session_store").Status)
}

func TestPrefetchSlashOutputsJSON(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.True(t, app.handleSlash(context.Background(), "/prefetch --json", &session.Session{ID: "session"}))
	var report prefetchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prefetch", report.Kind)
	require.Equal(t, "ok", report.Status)
}

func TestPrefetchDegradesOnMalformedConfigFile(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "prefetch"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report prefetchReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "prefetch", report.Kind)
	require.Equal(t, "warn", report.Status)
	configTask := prefetchTaskByName(t, report, "config_probe")
	require.Equal(t, "warn", configTask.Status)
	require.Equal(t, "config_load_failed", configTask.Evidence["config_load_error_kind"])
	require.Contains(t, configTask.Evidence["config_load_error"], "broken.json")
}

func bootstrapPlanPhaseByName(t *testing.T, report bootstrapPlanReport, name string) bootstrapPlanPhase {
	t.Helper()
	for _, phase := range report.Phases {
		if phase.Name == name {
			return phase
		}
	}
	require.Failf(t, "missing bootstrap phase", "phase %q was not reported", name)
	return bootstrapPlanPhase{}
}

func deferredInitTaskByName(t *testing.T, report deferredInitReport, name string) deferredInitTask {
	t.Helper()
	for _, task := range report.Tasks {
		if task.Name == name {
			return task
		}
	}
	require.Failf(t, "missing deferred init task", "task %q was not reported", name)
	return deferredInitTask{}
}

func prefetchTaskByName(t *testing.T, report prefetchReport, name string) prefetchTask {
	t.Helper()
	for _, task := range report.Tasks {
		if task.Name == name {
			return task
		}
	}
	require.Failf(t, "missing prefetch task", "task %q was not reported", name)
	return prefetchTask{}
}

func TestReasoningCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "reasoning", "high", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reasoning"`)
	require.Contains(t, out, `"effort": "high"`)

	storedConfigPath := filepath.Join(configHome, "config.json")
	stored, err := os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"reasoning_effort": "high"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "--output-format", "json", "reasoning"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reasoning"`)
	require.Contains(t, out, `"effort": "high"`)
	require.True(t, commandAcceptsGlobalOutputFormat("reasoning"))
}

func TestModelCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "model", "--path", configPath, "claude-persisted"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "model", report.Kind)
	require.Equal(t, "set", report.Action)
	require.Equal(t, "claude-persisted", report.Model)
	require.Equal(t, configPath, report.Path)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"model": "claude-persisted"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "model"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var status modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &status))
	require.Equal(t, "show", status.Action)
	require.Equal(t, "claude-persisted", status.Model)
	require.Empty(t, status.Path)
}

func TestResetCommandResetsConfigSections(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "custom-model",
		"max_tokens": 123,
		"language": "Japanese",
		"theme": "dark"
	}`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "model"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reset"`)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"section": "model"`)
	require.Contains(t, out, `"model"`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"model"`)
	require.NotContains(t, string(stored), `"max_tokens"`)
	require.Contains(t, string(stored), `"language": "Japanese"`)
	require.Contains(t, string(stored), `"theme": "dark"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "all"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"confirm_required": true`)
	require.FileExists(t, configPath)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "all", "--confirm"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"section": "all"`)
	require.NoFileExists(t, configPath)
	require.True(t, commandAcceptsGlobalOutputFormat("reset"))
}

func TestConfigResetSubcommandResetsExplicitConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "custom-model",
		"language": "Japanese",
		"theme": "dark",
		"editorMode": "vim"
	}`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "config", "reset", "interface"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reset"`)
	require.Contains(t, out, `"section": "interface"`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"model": "custom-model"`)
	require.NotContains(t, string(stored), `"language"`)
	require.NotContains(t, string(stored), `"theme"`)
	require.NotContains(t, string(stored), `"editorMode"`)
}

func TestLanguageCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "language", "Japanese", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "language"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"configured": true`)
	require.Contains(t, out, `"language": "Japanese"`)

	storedConfigPath := filepath.Join(configHome, "config.json")
	stored, err := os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"language": "Japanese"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "--output-format", "json", "language", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "language"`)
	require.Contains(t, out, `"action": "status"`)
	require.Contains(t, out, `"language": "Japanese"`)
	require.True(t, commandAcceptsGlobalOutputFormat("language"))
}

func TestPermissionsCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "permissions", "read-only", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report permissionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "permissions", report.Kind)
	require.Equal(t, "set", report.Action)
	require.Equal(t, "read-only", report.PermissionMode)
	require.Equal(t, filepath.Join(configHome, "config.json"), report.Path)
	require.NotEmpty(t, report.Modes)

	storedConfigPath := filepath.Join(configHome, "config.json")
	stored, err := os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"permission_mode": "read-only"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "--output-format", "text", "permissions", "show"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Permissions")
	require.Contains(t, out, "read-only")
	require.Contains(t, out, "● current")
	require.Contains(t, out, "workspace-write")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "permissions", "clear", "--path", storedConfigPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "clear", report.Action)
	require.Equal(t, "workspace-write", report.PermissionMode)
	stored, err = os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), "permission_mode")
}

func TestRateLimitCommandSetsShowsAndResetsConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"rate-limit", "set",
			"--path", configPath,
			"--max-retries", "5",
			"--initial-backoff-ms", "125",
			"--max-backoff-ms", "750",
			"--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "rate_limit"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"max_retries": 5`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"rate_limit"`)
	require.Contains(t, string(stored), `"max_retries": 5`)
	require.Contains(t, string(stored), `"initial_backoff_ms": 125`)
	require.Contains(t, string(stored), `"max_backoff_ms": 750`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "rate-limit", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "rate_limit"`)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"max_retries": 5`)
	require.True(t, commandAcceptsGlobalOutputFormat("rate-limit"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "rate-limit", "reset", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"previous"`)

	stored, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"rate_limit"`)
}

func TestBudgetCommandSetsShowsAndResetsConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"budget", "use",
			"--path", configPath,
			"--max-tokens", "8192",
			"--max-turns", "12",
			"--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "budget"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"max_tokens": 8192`)
	require.Contains(t, out, `"max_turns": 12`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"max_tokens": 8192`)
	require.Contains(t, string(stored), `"max_turns": 12`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "budget", "current"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "budget"`)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"max_tokens": 8192`)
	require.Contains(t, out, `"max_turns": 12`)
	require.True(t, commandAcceptsGlobalOutputFormat("budget"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "budget", "off", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"previous"`)

	stored, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"max_tokens"`)
	require.NotContains(t, string(stored), `"max_turns"`)
}

func TestRuntimePreferenceCommandErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "model missing output format",
			args:      []string{"model", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "model"`, `"option": "--output-format"`},
		},
		{
			name:      "model invalid output format",
			args:      []string{"model", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
		{
			name:      "models unknown option",
			args:      []string{"models", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "models"`, `"option": "--bogus"`},
		},
		{
			name:      "api missing addr",
			args:      []string{"api", "--addr", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "api"`, `"option": "--addr"`},
		},
		{
			name:      "api unknown command",
			args:      []string{"api", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "api"`, `"bogus"`},
		},
		{
			name:      "api-key missing key flag value",
			args:      []string{"api-key", "--key", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "api-key"`, `"option": "--key"`},
		},
		{
			name:      "api-key set missing key",
			args:      []string{"api-key", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "api-key set"`, `"argument": "KEY"`},
		},
		{
			name:      "advisor missing path",
			args:      []string{"advisor", "--path", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "advisor"`, `"option": "--path"`},
		},
		{
			name:      "advisor set missing model",
			args:      []string{"advisor", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "advisor set"`, `"argument": "MODEL"`},
		},
		{
			name:      "budget missing max tokens",
			args:      []string{"budget", "--max-tokens", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "budget"`, `"option": "--max-tokens"`},
		},
		{
			name:      "budget set missing value",
			args:      []string{"budget", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "budget set"`, `"argument": "VALUE"`},
		},
		{
			name:      "budget set unknown field",
			args:      []string{"budget", "set", "nope", "3"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "budget set"`, `"option": "nope"`},
		},
		{
			name:      "max tokens invalid count",
			args:      []string{"max-tokens", "0"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "COUNT"`, `"value": "0"`},
		},
		{
			name:      "max turns invalid count",
			args:      []string{"max-turns", "one"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "COUNT"`, `"value": "one"`},
		},
		{
			name:      "temperature set missing value",
			args:      []string{"temperature", "set"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "temperature set"`, `"argument": "VALUE"`},
		},
		{
			name:      "temperature invalid value",
			args:      []string{"temperature", "2"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "temperature"`, `"value": "2"`},
		},
		{
			name:      "reset missing target",
			args:      []string{"reset", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reset"`, `"option": "--target"`},
		},
		{
			name:      "reset unknown section",
			args:      []string{"reset", "bogus"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "section"`, `"value": "bogus"`},
		},
		{
			name:      "reset status extra",
			args:      []string{"reset", "status", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "reset status"`, `"extra"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}

func TestManagementSurfaceErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	workspace := t.TempDir()

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "capabilities unknown option",
			args:      []string{"capabilities", "bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "capabilities"`, `"option": "bogus"`},
		},
		{
			name:      "cache missing output format",
			args:      []string{"cache", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "cache"`, `"option": "--output-format"`},
		},
		{
			name:      "cache missing session",
			args:      []string{"cache", "--session", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "cache"`, `"option": "--session"`},
		},
		{
			name:      "cache unknown option",
			args:      []string{"cache", "--since"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "cache"`, `"option": "--since"`},
		},
		{
			name:      "break-cache missing output format",
			args:      []string{"break-cache", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "break-cache"`, `"option": "--output-format"`},
		},
		{
			name:      "break-cache missing message",
			args:      []string{"break-cache", "--message", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "break-cache"`, `"option": "--message"`},
		},
		{
			name:      "bookmarks missing output format",
			args:      []string{"bookmarks", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "bookmarks"`, `"option": "--output-format"`},
		},
		{
			name:      "bookmarks add missing name",
			args:      []string{"bookmarks", "add"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "bookmarks add"`, `"argument": "NAME"`},
		},
		{
			name:      "bookmarks invalid message index",
			args:      []string{"bookmarks", "--message", "nope", "add", "demo"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--message"`, `"value": "nope"`},
		},
		{
			name:      "metrics missing limit",
			args:      []string{"metrics", "--limit", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "metrics"`, `"option": "--limit"`},
		},
		{
			name:      "metrics invalid limit",
			args:      []string{"metrics", "--limit", "nope"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--limit"`, `"value": "nope"`},
		},
		{
			name:      "perf issue missing output",
			args:      []string{"perf-issue", "--output", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "perf-issue"`, `"option": "--output"`},
		},
		{
			name:      "perf issue invalid threshold",
			args:      []string{"perf-issue", "--token-threshold", "nope"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--token-threshold"`, `"value": "nope"`},
		},
		{
			name:      "insights missing limit",
			args:      []string{"insights", "--limit", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "insights"`, `"option": "--limit"`},
		},
		{
			name:      "think-back invalid year",
			args:      []string{"think-back", "--year", "nope"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--year"`, `"value": "nope"`},
		},
		{
			name:      "think-back missing output",
			args:      []string{"think-back", "--output", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "think-back"`, `"option": "--output"`},
		},
		{
			name:      "extra usage missing output format",
			args:      []string{"extra-usage", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "extra-usage"`, `"option": "--output-format"`},
		},
		{
			name:      "extra usage unknown argument",
			args:      []string{"extra-usage", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "extra-usage"`, `"bogus"`},
		},
		{
			name:      "install slack app missing output format",
			args:      []string{"install-slack-app", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "install-slack-app"`, `"option": "--output-format"`},
		},
		{
			name:      "install slack app unknown option",
			args:      []string{"install-slack-app", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "install-slack-app"`, `"option": "--bogus"`},
		},
		{
			name:      "stickers missing target",
			args:      []string{"stickers", "--target", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "stickers"`, `"option": "--target"`},
		},
		{
			name:      "stickers unexpected argument",
			args:      []string{"stickers", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "stickers"`, `"bogus"`},
		},
		{
			name:      "passes missing referral flag value",
			args:      []string{"passes", "--referral-url", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "passes"`, `"option": "--referral-url"`},
		},
		{
			name:      "passes set url missing value",
			args:      []string{"passes", "set-url"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "passes set-url"`, `"argument": "URL"`},
		},
		{
			name:      "passes unexpected argument",
			args:      []string{"passes", "show", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "passes show"`, `"extra"`},
		},
		{
			name:      "desktop missing output format",
			args:      []string{"desktop", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "desktop"`, `"option": "--output-format"`},
		},
		{
			name:      "desktop missing session",
			args:      []string{"desktop", "--session", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "desktop"`, `"option": "--session"`},
		},
		{
			name:      "desktop unknown flag",
			args:      []string{"desktop", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "desktop"`, `"option": "--bogus"`},
		},
		{
			name:      "desktop unexpected argument",
			args:      []string{"desktop", "status", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "desktop"`, `"extra"`},
		},
		{
			name:      "mobile missing addr",
			args:      []string{"mobile", "--addr", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "mobile"`, `"option": "--addr"`},
		},
		{
			name:      "mobile unknown flag",
			args:      []string{"mobile", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "mobile"`, `"option": "--bogus"`},
		},
		{
			name:      "mobile unexpected argument",
			args:      []string{"mobile", "ios", "android"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "mobile"`, `"android"`},
		},
		{
			name:      "reload plugins missing output format",
			args:      []string{"reload-plugins", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "reload-plugins"`, `"option": "--output-format"`},
		},
		{
			name:      "reload plugins unknown flag",
			args:      []string{"reload-plugins", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "reload-plugins"`, `"option": "--bogus"`},
		},
		{
			name:      "reload plugins unexpected argument",
			args:      []string{"reload-plugins", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "reload-plugins"`, `"bogus"`},
		},
		{
			name:      "dump manifests missing output format",
			args:      []string{"dump-manifests", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "dump-manifests"`, `"option": "--output-format"`},
		},
		{
			name:      "dump manifests invalid output format",
			args:      []string{"dump-manifests", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"option": "--output-format"`, `"value": "yaml"`},
		},
		{
			name:      "dump manifests unknown flag",
			args:      []string{"dump-manifests", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "dump-manifests"`, `"option": "--bogus"`},
		},
		{
			name:      "dump manifests unexpected argument",
			args:      []string{"dump-manifests", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "dump-manifests"`, `"bogus"`},
		},
		{
			name:      "brief missing output format",
			args:      []string{"brief", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "brief"`, `"option": "--output-format"`},
		},
		{
			name:      "brief missing status",
			args:      []string{"brief", "--status", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "brief"`, `"option": "--status"`},
		},
		{
			name:      "brief missing attachment",
			args:      []string{"brief", "--attach", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "brief"`, `"option": "--attach"`},
		},
		{
			name:      "brief unknown option",
			args:      []string{"brief", "--bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "brief"`, `"option": "--bogus"`},
		},
		{
			name:      "brief invalid status",
			args:      []string{"brief", "done", "--status", "urgent"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--status"`, `"value": "urgent"`},
		},
		{
			name:      "cron missing output format",
			args:      []string{"cron", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "cron"`, `"option": "--output-format"`},
		},
		{
			name:      "cron missing description",
			args:      []string{"cron", "create", "@daily", "check", "--description", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "cron"`, `"option": "--description"`},
		},
		{
			name:      "cron invalid now",
			args:      []string{"cron", "due", "--now", "soon"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--now"`, `"value": "soon"`},
		},
		{
			name:      "cron create missing prompt",
			args:      []string{"cron", "create", "@daily"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "cron create"`, `"argument": "SCHEDULE PROMPT"`},
		},
		{
			name:      "cron due unexpected argument",
			args:      []string{"cron", "due", "extra"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "cron due"`, `"extra"`},
		},
		{
			name:      "background missing output format",
			args:      []string{"background", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "background"`, `"option": "--output-format"`},
		},
		{
			name:      "background invalid output format",
			args:      []string{"background", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"option": "--output-format"`, `"value": "yaml"`},
		},
		{
			name:      "team missing output format",
			args:      []string{"team", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "team"`, `"option": "--output-format"`},
		},
		{
			name:      "team missing task",
			args:      []string{"team", "create", "reviewers", "--task", "--output-format", "json"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "team"`, `"option": "--task"`},
		},
		{
			name:      "team invalid limit",
			args:      []string{"team", "logs", "team-1", "--limit", "many"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--limit"`, `"value": "many"`},
		},
		{
			name:      "team invalid max events",
			args:      []string{"team", "watch", "team-1", "--max-events", "-1"},
			kind:      "invalid_flag_value",
			errorKind: "invalid_flag_value",
			contains:  []string{`"option": "--max-events"`, `"value": "-1"`},
		},
		{
			name:      "team create missing task prompt",
			args:      []string{"team", "create", "reviewers"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "team create"`, `"argument": "TASK"`},
		},
		{
			name:      "team status missing id",
			args:      []string{"team", "status"},
			kind:      "missing_argument",
			errorKind: "missing_argument",
			contains:  []string{`"command": "team status"`, `"argument": "TEAM_ID"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--cwd", workspace, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
	require.NoFileExists(t, filepath.Join(workspace, "--output-format"))
}
