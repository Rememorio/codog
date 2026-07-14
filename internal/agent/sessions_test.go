package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/reportconformance"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionname"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestResumedSessionShowJSONDefaultsToActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.runResumedSessionSlash([]string{"show", "--json"}, config.FlagOverrides{Resume: "active"}))
	var report sessionShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_show", report.Kind)
	require.Equal(t, "active", report.SessionID)
	require.Equal(t, 1, report.MessageCount)
	require.Len(t, report.Messages, 1)

	err := app.runResumedSessionSlash([]string{"show", "active", "extra", "--json"}, config.FlagOverrides{Resume: "active"})
	require.Error(t, err)
	var extraErr unexpectedExtraArgsError
	require.ErrorAs(t, err, &extraErr)
	require.Equal(t, "sessions show", extraErr.Command)
}

func TestResumedSessionExistsJSONMarksActiveAndRequiresTarget(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.runResumedSessionSlash([]string{"exists", "active", "--json"}, config.FlagOverrides{Resume: "active"}))
	var report sessionExistsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_exists", report.Kind)
	require.Equal(t, "active", report.SessionID)
	require.True(t, report.Exists)
	require.True(t, report.Active)
	require.NotEmpty(t, report.Path)
	out.Reset()

	require.NoError(t, app.runResumedSessionSlash([]string{"exists", "missing", "--json"}, config.FlagOverrides{Resume: "active"}))
	var missingReport sessionExistsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missingReport))
	require.Equal(t, "missing", missingReport.SessionID)
	require.False(t, missingReport.Exists)
	require.False(t, missingReport.Active)
	require.Empty(t, missingReport.Path)
	require.NotEmpty(t, missingReport.CandidatePath)

	err := app.runResumedSessionSlash([]string{"exists", "--json"}, config.FlagOverrides{Resume: "active"})
	require.Error(t, err)
	var requiredErr requiredArgumentError
	require.ErrorAs(t, err, &requiredErr)
	require.Equal(t, "sessions exists", requiredErr.Command)
	require.Equal(t, "ID", requiredErr.Argument)
}

func TestResumedSessionListJSONMarksActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.runResumedSessionSlash([]string{"list", "--json"}, config.FlagOverrides{Resume: "active"}))
	var report sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "sessions", report.Kind)
	require.Equal(t, "active", report.Active)
	require.Len(t, report.SessionDetails, 2)
	activeDetails := 0
	for _, detail := range report.SessionDetails {
		if detail.Active {
			activeDetails++
			require.Equal(t, "active", detail.ID)
		}
	}
	require.Equal(t, 1, activeDetails)
}

func TestSessionSlashListJSONMarksActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("active")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session list --json", sess))
	require.Empty(t, errOut.String())
	var report sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "active", report.Active)
	require.Len(t, report.SessionDetails, 1)
	require.True(t, report.SessionDetails[0].Active)
}

func TestSessionsListHonorsGlobalJSONOutputFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello sessions")))
	t.Chdir(workspace)

	for _, command := range []string{"sessions", "session"} {
		out, err := captureStdout(t, func() error {
			return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", command, "list"}, config.FlagOverrides{})
		})
		require.NoError(t, err)
		require.NotContains(t, out, "\t")

		var report sessionListReport
		require.NoError(t, json.Unmarshal([]byte(out), &report))
		require.Equal(t, "sessions", report.Kind)
		require.Equal(t, "list", report.Action)
		require.Equal(t, "ok", report.Status)
		require.Equal(t, workspace, report.Workspace)
		require.Equal(t, []string{"source"}, report.Sessions)
		require.Len(t, report.SessionDetails, 1)
		require.Equal(t, "source", report.SessionDetails[0].ID)
		require.Equal(t, 1, report.SessionDetails[0].MessageCount)
	}

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "session", "switch", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var switchReport sessionSwitchReport
	require.NoError(t, json.Unmarshal([]byte(out), &switchReport))
	require.Equal(t, "session_switch", switchReport.Kind)
	require.Equal(t, "switch", switchReport.Action)
	require.Equal(t, "source", switchReport.SessionID)
	require.Contains(t, switchReport.ContinueCommands[0], "--resume 'source' repl")
}

func TestSessionSlashExistsJSONMarksActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("active")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session exists active --json", sess))
	require.Empty(t, errOut.String())
	var report sessionExistsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_exists", report.Kind)
	require.Equal(t, "active", report.SessionID)
	require.True(t, report.Exists)
	require.True(t, report.Active)
}

func TestGenerateSessionNameCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "Fix the HTTP 500 in API users endpoint"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "Fix the HTTP 500 in API users endpoint")))
	require.NoError(t, store.AppendInput("existing", "Fix the HTTP 500 in API users endpoint"))
	require.NoError(t, store.Append("existing", anthropic.TextMessage("user", "collision holder")))
	_, err := store.Rename("existing", "fix-http-500-api-users-endpoint")
	require.NoError(t, err)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.GenerateSessionName([]string{"--session", "source", "--json"}, config.FlagOverrides{}))
	var report sessionname.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_name", report.Kind)
	require.Equal(t, "generate", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, "fix-http-500-api-users-endpoint-2", report.SuggestedID)
	require.Equal(t, 1, report.CollisionCount)
	require.Equal(t, "first_prompt", report.Source)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/generateSessionName", []string{"--source", "first"}, config.FlagOverrides{Resume: "source"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_name", report.Kind)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, "fix-http-500-api-users-endpoint-2", report.SuggestedID)
	require.Equal(t, "first_prompt", report.Source)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/generate-session-name", []string{"--text", "Summarize checkout regression"}, config.FlagOverrides{Resume: "source"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_name", report.Kind)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, "summarize-checkout-regression", report.SuggestedID)
	require.Equal(t, "text", report.Source)
	out.Reset()

	require.NoError(t, app.GenerateSessionName([]string{"--session", "source", "--rename", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "renamed", report.Status)
	require.True(t, report.Renamed)
	require.Equal(t, "source", report.OldID)
	require.Equal(t, "fix-http-500-api-users-endpoint-2", report.NewID)
	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.Exists("fix-http-500-api-users-endpoint-2")
	require.NoError(t, err)
	require.True(t, ok)
	out.Reset()

	require.NoError(t, store.AppendInput("slash", "Add session name slash support"))
	require.NoError(t, store.Append("slash", anthropic.TextMessage("user", "Add session name slash support")))
	sess, err := store.Open("slash")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/generateSessionName --rename --json", sess))
	require.Equal(t, "add-session-name-slash-support", sess.ID)
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "renamed", report.Status)
	require.Equal(t, "add-session-name-slash-support", report.NewID)
	require.Empty(t, errOut.String())
}

func TestClearAndResumeSlashSwitchSessionState(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "resume me")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       &errOut,
	}

	app.dynamicSkillPaths = []string{"src/app/main.go"}
	require.True(t, app.handleSlash(context.Background(), "/clear", sess))
	require.NotEqual(t, "source", sess.ID)
	require.Empty(t, sess.Messages)
	require.Empty(t, app.dynamicSkillPaths)
	require.Contains(t, errOut.String(), "session cleared:")
	errOut.Reset()

	app.dynamicSkillPaths = []string{"src/app/main.go"}
	require.True(t, app.handleSlash(context.Background(), "/resume source", sess))
	require.Equal(t, "source", sess.ID)
	require.Len(t, sess.Messages, 1)
	require.Empty(t, app.dynamicSkillPaths)
	require.Contains(t, errOut.String(), "session resumed: source")
	errOut.Reset()

	next, err := store.Open("")
	require.NoError(t, err)
	*sess = *next
	require.True(t, app.handleSlash(context.Background(), "/continue source", sess))
	require.Equal(t, "source", sess.ID)
	require.Len(t, sess.Messages, 1)
	require.Contains(t, errOut.String(), "session resumed: source")
}

func TestResumeLatestSlashSkipsCurrentSession(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("older-session", anthropic.TextMessage("user", "previous conversation")))
	require.NoError(t, store.Append("zz-current-session", anthropic.TextMessage("user", "current conversation")))
	sess, err := store.Open("zz-current-session")
	require.NoError(t, err)
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/resume latest", sess))
	require.Equal(t, "older-session", sess.ID)
	require.Len(t, sess.Messages, 1)
	require.Contains(t, errOut.String(), "session resumed: older-session")
}

func TestResumeLatestSlashFallsBackToSiblingWorkspace(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	storeA := session.NewWorkspaceStore(configHome, workspaceA)
	storeB := session.NewWorkspaceStore(configHome, workspaceB)
	require.NoError(t, storeA.Append("current-session", anthropic.TextMessage("user", "current")))
	require.NoError(t, storeB.Append("remote-session", anthropic.TextMessage("user", "remote")))
	sess, err := storeA.Open("current-session")
	require.NoError(t, err)
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  storeA,
		Workspace: workspaceA,
		Out:       io.Discard,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/resume latest", sess))
	require.Equal(t, "remote-session", sess.ID)
	require.Equal(t, filepath.Join(storeB.Dir, "remote-session.jsonl"), sess.Path)
	require.Contains(t, errOut.String(), "session resumed: remote-session")
}

func TestResumeRestoresTodosFromTranscript(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{
		Type:  "tool_use",
		Name:  "TodoWrite",
		Input: []byte(`{"todos":[{"content":"restore todo","status":"in_progress","priority":"high"}]}`),
	}}}))
	require.NoError(t, store.Append("done", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{
		Type:  "tool_use",
		Name:  "todo_write",
		Input: []byte(`{"todos":[{"content":"finished","status":"completed","priority":"low"}]}`),
	}}}))
	sess, err := store.Open("")
	require.NoError(t, err)
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	require.True(t, app.handleSlash(context.Background(), "/resume source", sess))
	state, err := todos.Load(workspace)
	require.NoError(t, err)
	require.Len(t, state.Items, 1)
	require.Equal(t, "restore todo", state.Items[0].Content)
	require.Equal(t, "in_progress", state.Items[0].Status)

	_, err = app.openSession(config.FlagOverrides{Resume: "done"})
	require.NoError(t, err)
	state, err = todos.Load(workspace)
	require.NoError(t, err)
	require.Empty(t, state.Items)
}

func TestRuntimeInfoSlashCommands(t *testing.T) {
	var out bytes.Buffer
	app := &App{Workspace: t.TempDir(), Out: &out, Err: io.Discard}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/version", sess))
	require.Contains(t, out.String(), "Codog")
	require.Contains(t, out.String(), "Version")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/acp --json", sess))
	require.Contains(t, out.String(), `"kind": "acp"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox", sess))
	require.Contains(t, out.String(), `"os":`)
	require.Contains(t, out.String(), `"strategy_statuses":`)
	require.Contains(t, out.String(), `"container":`)
	require.Contains(t, out.String(), `"namespace_supported":`)
	require.Contains(t, out.String(), `"requested":`)
	require.Contains(t, out.String(), `"filesystem_mode":`)
	require.Contains(t, out.String(), `"active_components":`)
}

func TestSandboxCommandReportsConfiguredRequest(t *testing.T) {
	workspace := t.TempDir()
	enabled := true
	namespace := false
	network := true
	var out bytes.Buffer
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			Sandbox: config.SandboxConfig{
				Enabled:               &enabled,
				NamespaceRestrictions: &namespace,
				NetworkIsolation:      &network,
				FilesystemMode:        "allow-list",
				AllowedMounts:         []string{"logs"},
			},
		}},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Sandbox())
	var report struct {
		Kind               string                         `json:"kind"`
		Action             string                         `json:"action"`
		Status             string                         `json:"status"`
		ConfiguredStrategy string                         `json:"configured_strategy"`
		Requested          bool                           `json:"requested"`
		RequestedNamespace bool                           `json:"requested_namespace"`
		RequestedNetwork   bool                           `json:"requested_network"`
		FilesystemMode     string                         `json:"filesystem_mode"`
		AllowedMounts      []string                       `json:"allowed_mounts"`
		Markers            []string                       `json:"markers"`
		Execution          sandbox.SandboxExecutionStatus `json:"execution"`
		ActiveComponents   map[string]bool                `json:"active_components"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "sandbox", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Contains(t, []string{"ok", "warn", "error"}, report.Status)
	require.Equal(t, "detect", report.ConfiguredStrategy)
	require.True(t, report.Requested)
	require.False(t, report.RequestedNamespace)
	require.True(t, report.RequestedNetwork)
	require.Equal(t, "allow-list", report.FilesystemMode)
	require.Equal(t, []string{filepath.Join(workspace, "logs")}, report.AllowedMounts)
	require.NotNil(t, report.Markers)
	require.NotNil(t, report.ActiveComponents)
	require.True(t, report.Execution.Requested.Enabled)
	require.False(t, report.Execution.Requested.NamespaceRestrictions)
	require.True(t, report.Execution.Requested.NetworkIsolation)
	require.Equal(t, sandbox.FilesystemIsolationAllowList, report.Execution.Requested.FilesystemMode)
}

func TestSandboxToggleCommandPersistsSettings(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.SandboxToggle([]string{"detect", "--json"}))
	require.Contains(t, out.String(), `"kind": "sandbox_toggle"`)
	require.Contains(t, out.String(), `"configured_strategy": "detect"`)
	require.Contains(t, out.String(), `"resolution_status":`)
	require.Contains(t, out.String(), `"namespace_supported":`)
	require.Equal(t, "detect", app.Config.Future.SandboxStrategy)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"strategy": "detect"`)
	require.NotContains(t, string(data), "sandbox_strategy")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox-toggle off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Sandbox Toggle")
	require.Contains(t, out.String(), "Configured       off")
	require.Contains(t, out.String(), "Namespace")
	require.Equal(t, "off", app.Config.Future.SandboxStrategy)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.SandboxToggle([]string{"restricted-token", "--json"}))
	require.Contains(t, out.String(), `"configured_strategy": "restricted-token"`)
	require.Equal(t, "restricted-token", app.Config.Future.SandboxStrategy)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"strategy": "restricted-token"`)
	require.NotContains(t, string(data), "sandbox_strategy")
	out.Reset()

	require.NoError(t, app.SandboxToggle([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"configured_strategy": ""`)
	require.Equal(t, "", app.Config.Future.SandboxStrategy)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"sandbox"`)
	require.NotContains(t, string(data), "sandbox_strategy")
}

func TestHeapDumpCommandWritesProfile(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	path := filepath.Join(workspace, "heap.pprof")

	require.NoError(t, app.HeapDump([]string{path, "--json"}))
	require.Contains(t, out.String(), `"kind": "heapdump"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"gc": true`)
	stat, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, stat.Size(), int64(0))
	out.Reset()

	slashPath := filepath.Join(workspace, "slash.pprof")
	require.True(t, app.handleSlash(context.Background(), "/heapdump "+slashPath+" --no-gc", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Heap Dump")
	require.Contains(t, out.String(), "GC               false")
	stat, err = os.Stat(slashPath)
	require.NoError(t, err)
	require.Greater(t, stat.Size(), int64(0))
	require.Empty(t, errOut.String())
}

func TestSystemPromptAndToolDetailsSlashCommands(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use the project style."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("debug notes\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/system-prompt", sess))
	require.Contains(t, out.String(), "Use the project style.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tool-details bash", sess))
	require.Contains(t, out.String(), "Tool")
	require.Contains(t, out.String(), "Name             bash")
	require.Contains(t, out.String(), "Permission       danger-full-access")
	require.Contains(t, out.String(), `"command"`)
	out.Reset()

	require.NoError(t, app.DebugToolCall(context.Background(), []string{"read_file", `{"path":"notes.txt"}`, "--json"}, config.FlagOverrides{SessionID: "session"}))
	require.Contains(t, out.String(), `"kind": "debug_tool_call"`)
	require.Contains(t, out.String(), `"tool": "read_file"`)
	require.Contains(t, out.String(), `"success": true`)
	require.Contains(t, out.String(), "debug notes")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), `/debug-tool-call read_file {"path": "notes.txt"}`, sess))
	require.Contains(t, out.String(), "Tool Call")
	require.Contains(t, out.String(), "Tool             read_file")
	require.Contains(t, out.String(), "debug notes")
	require.Empty(t, errOut.String())
}

func TestGitCommandStatusDiffAndCommit(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Git([]string{"status"}))
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	require.NoError(t, app.Git([]string{"status", "--json"}))
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.Equal(t, "show", statusJSON.Action)
	require.Equal(t, "ok", statusJSON.Status)
	require.False(t, statusJSON.Clean)
	require.NotEmpty(t, statusJSON.BranchLine)
	require.Len(t, statusJSON.Entries, 1)
	require.Equal(t, "??", statusJSON.Entries[0].Code)
	require.Equal(t, "notes.txt", statusJSON.Entries[0].Path)
	out.Reset()

	require.NoError(t, app.Git([]string{"--output-format", "JSON", "status"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.False(t, statusJSON.Clean)
	out.Reset()

	require.NoError(t, app.Git([]string{"commit", "--all", "add", "notes"}))
	require.Contains(t, out.String(), `"commit":`)
	require.Contains(t, out.String(), "add notes")
	var commitJSON commitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &commitJSON))
	require.Equal(t, "commit", commitJSON.Kind)
	require.Equal(t, "create", commitJSON.Action)
	require.Equal(t, "ok", commitJSON.Status)
	require.True(t, commitJSON.All)
	require.Contains(t, commitJSON.Summary, "add notes")
	out.Reset()

	require.NoError(t, app.Git([]string{"log", "1"}))
	require.Contains(t, out.String(), "add notes")
	out.Reset()

	require.NoError(t, app.Git([]string{"log", "1", "--json"}))
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "show", logJSON.Action)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Limit)
	require.Equal(t, 1, logJSON.Count)
	require.Len(t, logJSON.Entries, 1)
	require.Equal(t, "add notes", logJSON.Entries[0].Subject)
	require.NotEmpty(t, logJSON.Entries[0].Commit)
	require.Contains(t, logJSON.Raw, "add notes")
	out.Reset()

	require.NoError(t, app.Changelog([]string{"1"}))
	require.Contains(t, out.String(), "add notes")
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	require.NoError(t, app.Changelog([]string{"1", "--json"}))
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "show", changelogJSON.Action)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Limit)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "add notes", changelogJSON.Entries[0].Subject)
	require.Contains(t, changelogJSON.Raw, "notes.txt")
	out.Reset()

	runGit(t, workspace, "tag", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	require.NoError(t, app.Git([]string{"--output-format", "text", "commit", "--all", "feat: add feature"}))
	require.Contains(t, out.String(), "Commit")
	require.Contains(t, out.String(), "feat: add feature")
	out.Reset()
	require.NoError(t, app.ReleaseNotes([]string{"--from", "v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "release_notes"`)
	require.Contains(t, out.String(), `"name": "Features"`)
	require.Contains(t, out.String(), `"subject": "feat: add feature"`)
	out.Reset()

	require.NoError(t, app.Git([]string{"blame", "notes.txt", "1"}))
	require.Contains(t, out.String(), "hello")
	out.Reset()

	require.NoError(t, app.Git([]string{"blame", "notes.txt", "1", "--json"}))
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "show", blameJSON.Action)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, "notes.txt", blameJSON.Path)
	require.Equal(t, 1, blameJSON.Line)
	require.Equal(t, 1, blameJSON.Count)
	require.Len(t, blameJSON.Entries, 1)
	require.Equal(t, "hello", blameJSON.Entries[0].Line)
	require.Equal(t, "add notes", blameJSON.Entries[0].Summary)
	require.Contains(t, blameJSON.Raw, "hello")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\nagain\n"), 0o644))
	require.NoError(t, app.Git([]string{"diff"}))
	require.Contains(t, out.String(), "+again")
	out.Reset()

	require.NoError(t, app.Git([]string{"diff", "--json", "notes.txt"}))
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "show", diffJSON.Action)
	require.Equal(t, "ok", diffJSON.Status)
	require.False(t, diffJSON.Staged)
	require.False(t, diffJSON.Empty)
	require.Equal(t, []string{"notes.txt"}, diffJSON.Paths)
	require.Contains(t, diffJSON.Diff, "+again")
	out.Reset()

	runGit(t, workspace, "add", "notes.txt")
	require.NoError(t, app.Git([]string{"diff", "--staged", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.True(t, diffJSON.Staged)
	require.False(t, diffJSON.Empty)
	require.Contains(t, diffJSON.Diff, "+again")
	out.Reset()

	require.NoError(t, app.Stash([]string{"push", "agent stash"}))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.NoError(t, app.Git([]string{"stash", "list"}))
	require.Contains(t, out.String(), "agent stash")
	out.Reset()

	require.NoError(t, app.Stash([]string{"list", "--json"}))
	var stashJSON stashReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, "list", stashJSON.Action)
	require.Equal(t, "ok", stashJSON.Status)
	require.Equal(t, 1, stashJSON.Count)
	require.Len(t, stashJSON.Stashes, 1)
	require.Equal(t, "stash@{0}", stashJSON.Stashes[0].Ref)
	require.Contains(t, stashJSON.Stashes[0].Subject, "agent stash")
	require.Contains(t, stashJSON.Output, "agent stash")
	out.Reset()

	require.NoError(t, app.Git([]string{"--json", "stash", "list"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, 1, stashJSON.Count)
}

func TestRunCLIRoutesTopLevelGitAliases(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello cli\n"), 0o644))
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "commit", "--all", "cli", "alias", "commit"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"commit":`)
	require.Contains(t, out, "cli alias commit")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "log", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "cli alias commit")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "log", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal([]byte(out), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Count)
	require.Equal(t, "cli alias commit", logJSON.Entries[0].Subject)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "git", "--json", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.True(t, statusJSON.Clean)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "stash", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var emptyStashJSON stashReport
	require.NoError(t, json.Unmarshal([]byte(out), &emptyStashJSON))
	require.Equal(t, "stash", emptyStashJSON.Kind)
	require.Equal(t, "ok", emptyStashJSON.Status)
	require.Equal(t, 0, emptyStashJSON.Count)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "changelog", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal([]byte(out), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "cli alias commit", changelogJSON.Entries[0].Subject)
	require.Contains(t, changelogJSON.Raw, "notes.txt")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "blame", "notes.txt", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "hello cli")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "blame", "notes.txt", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal([]byte(out), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, 1, blameJSON.Count)
	require.Equal(t, "hello cli", blameJSON.Entries[0].Line)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello cli\nagain\n"), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "+again")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal([]byte(out), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "ok", diffJSON.Status)
	require.Equal(t, 1, diffJSON.ChangedFileCount)
	require.Equal(t, []string{"notes.txt"}, diffJSON.ChangedFiles)
	require.Contains(t, diffJSON.Diff, "+again")
}

func TestDiffNonGitDirReportsTypedJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	t.Chdir(workspace)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NotContains(t, out, "usage: git diff")
	require.NotContains(t, out, "config_load_failed")

	var report diffReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "diff", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "no_git_repo", report.Result)
	require.Equal(t, "no_git_repo", report.ErrorKind)
	require.Contains(t, report.Message, "git repository")
	require.Contains(t, report.Hint, "git init")
	require.Equal(t, 0, report.ChangedFileCount)
	require.Empty(t, report.ChangedFiles)
}

func TestGitSlashDiffAndCommit(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/commit --all slash commit", sess))
	require.Contains(t, errOut.String(), "commit ")
	errOut.Reset()
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "slash-json.txt"), []byte("slash json\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/commit --json --all slash json commit", sess))
	var commitJSON commitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &commitJSON))
	require.Equal(t, "commit", commitJSON.Kind)
	require.Equal(t, "ok", commitJSON.Status)
	require.Contains(t, commitJSON.Summary, "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/log 1", sess))
	require.Contains(t, out.String(), "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/log --json 1", sess))
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Count)
	require.Equal(t, "slash json commit", logJSON.Entries[0].Subject)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/changelog 1", sess))
	require.Contains(t, out.String(), "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/changelog --json 1", sess))
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "slash json commit", changelogJSON.Entries[0].Subject)
	out.Reset()

	runGit(t, workspace, "tag", "v0.2.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: add slash feature")
	require.True(t, app.handleSlash(context.Background(), "/release-notes v0.2.0", sess))
	require.Contains(t, out.String(), "# Release Notes")
	require.Contains(t, out.String(), "feat: add slash feature")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/blame notes.txt 1", sess))
	require.Contains(t, out.String(), "hello slash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/blame notes.txt 1 --json", sess))
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, 1, blameJSON.Count)
	require.Equal(t, "hello slash", blameJSON.Entries[0].Line)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git status", sess))
	require.Contains(t, out.String(), "## ")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git status --json", sess))
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.Equal(t, "ok", statusJSON.Status)
	require.True(t, statusJSON.Clean)
	require.Empty(t, statusJSON.Entries)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git --json status", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.True(t, statusJSON.Clean)
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\nchanged\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/diff", sess))
	require.Contains(t, out.String(), "+changed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/diff --json", sess))
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "ok", diffJSON.Status)
	require.False(t, diffJSON.Empty)
	require.Contains(t, diffJSON.Diff, "+changed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash push slash stash", sess))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash list", sess))
	require.Contains(t, out.String(), "slash stash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash list --json", sess))
	var stashJSON stashReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, "ok", stashJSON.Status)
	require.Equal(t, 1, stashJSON.Count)
	require.Contains(t, stashJSON.Stashes[0].Subject, "slash stash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git --json stash list", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, 1, stashJSON.Count)
}

func TestBranchCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello branch\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add branch notes")
	base, err := gitops.Branch(workspace)
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Branch([]string{"create", "feature/one", "--switch", "--json"}))
	require.Contains(t, out.String(), `"kind": "branch"`)
	require.Contains(t, out.String(), `"current": "feature/one"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch rename feature/two", sess))
	require.Contains(t, out.String(), "feature/two")
	out.Reset()

	require.NoError(t, app.Git([]string{"branch", "current"}))
	require.Contains(t, out.String(), "feature/two")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch switch "+base, sess))
	require.Contains(t, out.String(), base)
	out.Reset()

	require.NoError(t, app.Branch([]string{"delete", "feature/two", "--force"}))
	require.Contains(t, out.String(), "delete")
	require.Contains(t, out.String(), "Deleted branch")
	require.Empty(t, errOut.String())
}

func TestBranchFreshnessCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	base, err := gitops.Branch(workspace)
	require.NoError(t, err)
	runGit(t, workspace, "switch", "-c", "topic")
	runGit(t, workspace, "switch", base)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: main update")

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.Branch([]string{"freshness", "topic", base, "--json"}))
	require.Contains(t, out.String(), `"action": "freshness"`)
	require.Contains(t, out.String(), `"status": "stale"`)
	require.Contains(t, out.String(), `"behind": 1`)
	require.Contains(t, out.String(), `"verification_blocked": true`)
	require.Contains(t, out.String(), `"recovery_scenario": "stale_branch"`)
	require.Contains(t, out.String(), `"lane_event": "branch.stale_against_main"`)
	require.Contains(t, out.String(), `"suggested_commands": [`)
	require.Contains(t, out.String(), `"fix: main update"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch freshness topic "+base, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Freshness        stale")
	require.Contains(t, out.String(), "Behind           1")
	require.Contains(t, out.String(), "Verification     blocked until branch is updated")
	require.Contains(t, out.String(), "Recovery         stale_branch")
	require.Contains(t, out.String(), "Event            branch.stale_against_main")
	require.Contains(t, out.String(), "git merge --ff-only")
	require.Contains(t, out.String(), "fix: main update")
	require.Empty(t, errOut.String())
}

func TestBranchLockCommandAndSlash(t *testing.T) {
	input := `{"intents":[{"lane_id":"lane-a","branch":"feature/shared","modules":["runtime"]},{"laneId":"lane-b","branch":"feature/shared","modules":["runtime/mcp"]}]}`
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}

	require.NoError(t, app.BranchLock([]string{"check", "--input", input, "--json"}))
	require.Contains(t, out.String(), `"kind": "branch_lock"`)
	require.Contains(t, out.String(), `"status": "collision"`)
	require.Contains(t, out.String(), `"collision_count": 1`)
	require.Contains(t, out.String(), `"module": "runtime"`)
	require.Contains(t, out.String(), `"lane-a"`)
	require.Contains(t, out.String(), `"lane-b"`)
	out.Reset()

	app.In = strings.NewReader(input)
	require.True(t, app.handleSlash(context.Background(), "/branch-lock check --stdin", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Branch Lock")
	require.Contains(t, out.String(), "Status           collision")
	require.Contains(t, out.String(), "branch=feature/shared module=runtime lanes=lane-a, lane-b")
	require.Empty(t, errOut.String())
}

func TestStaleBaseCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	baseSHA, err := gitops.Run(workspace, "rev-parse", "HEAD")
	require.NoError(t, err)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.StaleBase([]string{"--base-commit", baseSHA, "--json"}))
	require.Contains(t, out.String(), `"kind": "stale_base"`)
	require.Contains(t, out.String(), `"status": "matches"`)
	require.Contains(t, out.String(), `"matches": true`)
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "next.txt"), []byte("next\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: next")

	require.True(t, app.handleSlash(context.Background(), "/stale-base "+baseSHA, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Stale Base")
	require.Contains(t, out.String(), "Status           diverged")
	require.Contains(t, out.String(), "Matches          false")
	require.Contains(t, out.String(), "stale codebase")
	require.Empty(t, errOut.String())
}

func TestStaleBaseRejectsInvalidBaseCommit(t *testing.T) {
	workspace := initGitRepo(t)
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.ErrorContains(t, app.StaleBase([]string{"--base-commit", "not-a-sha", "--json"}), "hexadecimal")
	require.Empty(t, out.String())
	require.ErrorContains(t, app.StaleBase([]string{"--base-commit", "--json"}), "cannot start")
}

func TestTrustCommandAndSlash(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repo-a")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Trust([]string{"resolve", "--screen", "Do you trust the files in this folder?", "--allow", parent, "--json"}))
	require.Contains(t, out.String(), `"kind": "trust"`)
	require.Contains(t, out.String(), `"status": "auto_trusted"`)
	require.Contains(t, out.String(), `"trusted": true`)
	require.Contains(t, out.String(), `"policy": "auto_trust"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/trust Do you trust this folder? --deny "+workspace, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Trust")
	require.Contains(t, out.String(), "Status           denied")
	require.Contains(t, out.String(), "Policy           deny")
	require.Contains(t, out.String(), "trust_denied")
	require.Empty(t, errOut.String())
}

func TestGreenContractCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.GreenContract([]string{
		"check",
		"--merge-ready",
		"--required-level", "workspace",
		"--observed-level", "merge-ready",
		"--test-command", "go test ./...",
		"--base-branch-fresh",
		"--recovery-context",
		"--json",
	}))
	require.Contains(t, out.String(), `"kind": "green_contract"`)
	require.Contains(t, out.String(), `"status": "satisfied"`)
	require.Contains(t, out.String(), `"required_level": "workspace"`)
	require.Contains(t, out.String(), `"observed_level": "merge_ready"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/green-contract --merge-ready --observed-level workspace --test-result go-test-all=0", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Green Contract")
	require.Contains(t, out.String(), "Status           unsatisfied")
	require.Contains(t, out.String(), "Missing          base_branch_freshness, recovery_attempt_context")
	require.Empty(t, errOut.String())
}

func TestReportSchemaCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	input := `{"generated_at":"2026-05-14T00:00:00Z","producer":"worker-1","claims":[{"id":"claim-secret","kind":"observed_fact","text":"secret","confidence":"high","sensitivity":"secret"},{"id":"claim-fact","kind":"observed_fact","text":"done","confidence":"high","sensitivity":"public"}]}`

	require.NoError(t, app.ReportSchema([]string{
		"project",
		"--input", input,
		"--consumer", "viewer",
		"--max-sensitivity", "public",
		"--json",
	}))
	require.Contains(t, out.String(), `"kind": "report_schema"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"projection_id":`)
	require.Contains(t, out.String(), `"field_path": "claims[1]"`)
	out.Reset()

	require.NoError(t, app.ReportSchema([]string{"registry", "--json"}))
	var registryReport reportSchemaReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &registryReport))
	require.NotNil(t, registryReport.Registry)
	require.Equal(t, reportschema.SchemaV1, registryReport.Registry.SchemaVersion)
	require.Equal(t, harness.ReportSchemaVersion, reportSchemaRegistryEntry(t, *registryReport.Registry, "mock_parity_report").SchemaVersion)
	require.Equal(t, harness.ManifestSchemaVersion, reportSchemaRegistryEntry(t, *registryReport.Registry, "mock_parity_manifest").SchemaVersion)
	out.Reset()

	require.NoError(t, app.ReportSchema([]string{"registry", "--report", "report_backpressure", "--schema-version", reportschema.ReportingReportSchemaV1, "--field-family", "field_deltas", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &registryReport))
	require.NotNil(t, registryReport.Registry)
	require.Len(t, registryReport.Registry.Reports, 1)
	require.Equal(t, "report_backpressure", registryReport.Registry.Reports[0].ID)
	require.Equal(t, []string{"field_deltas[]", "field_deltas[].state"}, reportSchemaFieldIDs(*registryReport.Registry))
	require.Contains(t, registryReport.Registry.Fields[1].EnumValues, reportschema.FieldCarriedForward)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/report-schema registry", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Report Schema")
	require.Contains(t, out.String(), "Schema           claw.report.v1")
	require.Contains(t, out.String(), "identity.report_id")
	require.Contains(t, out.String(), "mock_parity_report "+harness.ReportSchemaVersion)
	require.Contains(t, out.String(), "mock_parity_manifest "+harness.ManifestSchemaVersion)
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/report-schema registry --report report_backpressure --field-family field_deltas", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "field_deltas[].state")
	require.Contains(t, out.String(), "enum=changed|unchanged|cleared|carried_forward")
	require.NotContains(t, out.String(), "claims[].kind")
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.ReportSchema([]string{"conformance-fixtures", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &registryReport))
	require.Len(t, registryReport.ConformanceCases, len(reportconformance.RequiredCases()))
	require.Equal(t, reportconformance.RequiredCases()[0].ProjectionID, registryReport.ConformanceCases[0].ProjectionID)
	out.Reset()

	require.NoError(t, app.ReportSchema([]string{"conformance", "--input", reportSchemaConformanceBundleJSON(t), "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &registryReport))
	require.NotNil(t, registryReport.Conformance)
	require.Equal(t, "ok", registryReport.Status)
	require.True(t, registryReport.Conformance.Valid)
	require.True(t, registryReport.Conformance.ParsePassed)
	require.True(t, registryReport.Conformance.SemanticPassed)
	require.NotNil(t, registryReport.Conformance.LastPassed)
	require.Equal(t, "test-consumer", registryReport.Conformance.LastPassed.Consumer)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/report-schema conformance-fixtures", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Fixture set      reporting-projection-golden-v1")
	require.Contains(t, out.String(), "public_full_redacts_internal_claims_and_item_detail")
	require.Empty(t, errOut.String())
}

func reportSchemaConformanceBundleJSON(t *testing.T) string {
	t.Helper()
	cases := make([]reportconformance.CaseResult, 0, len(reportconformance.RequiredCases()))
	for _, required := range reportconformance.RequiredCases() {
		cases = append(cases, reportconformance.CaseResult{
			Name:         required.Name,
			ProjectionID: required.ProjectionID,
			Parsed:       true,
			SemanticChecks: reportconformance.SemanticChecks{
				CanonicalIdentityCorrelated: true,
				RedactedFieldsHandled:       true,
				MissingFieldsDistinguished:  true,
				DowngradeHandled:            true,
				NoChangeHandled:             true,
				FreshnessHandled:            true,
			},
		})
	}
	data, err := json.Marshal(reportconformance.Bundle{
		SchemaVersion: reportconformance.BundleSchemaVersion,
		FixtureSet:    reportconformance.FixtureSetVersion,
		Consumer: reportconformance.ConsumerIdentity{
			Name:    "test-consumer",
			Version: "1.0.0",
		},
		PassedAt: "2026-07-07T16:30:00Z",
		Cases:    cases,
	})
	require.NoError(t, err)
	return string(data)
}

func TestG004ConformanceCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	valid := `{"schemaVersion":"g004.contract.bundle.v1","laneEvents":[{"event":"lane.started","status":"running","emittedAt":"2026-05-14T00:00:00Z","metadata":{"seq":1,"provenance":"worker","emitterIdentity":"codog","environmentLabel":"test"}},{"event":"lane.finished","status":"ok","emittedAt":"2026-05-14T00:01:00Z","metadata":{"seq":2,"provenance":"worker","emitterIdentity":"codog","environmentLabel":"test","eventFingerprint":"finish-2"}}],"reports":[{"schemaVersion":"g004.report.v1","reportId":"report-1","identity":{"contentHash":"hash"},"projection":{"provenance":"projection-policy"},"redaction":{"provenance":"redaction-policy"},"consumerCapabilities":["claims"],"findings":[{"kind":"fact","confidence":"high","statement":"finished"}],"fieldDeltas":[{"field":"status","previousHash":"old","currentHash":"new","attribution":"worker"}]}],"approvalTokens":[{"tokenId":"token-1","owner":"operator","scope":"workspace-write","issuedAt":"2026-05-14T00:00:00Z","oneTimeUse":true,"replayPreventionNonce":"nonce","delegationChain":[{"from":"operator","to":"worker","action":"grant","at":"2026-05-14T00:00:01Z"}]}]}`

	require.NoError(t, app.G004Conformance([]string{"validate", "--input", valid, "--json"}))
	require.Contains(t, out.String(), `"kind": "g004_conformance"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"valid": true`)
	out.Reset()

	invalidPath := filepath.Join(workspace, "invalid-g004.json")
	require.NoError(t, os.WriteFile(invalidPath, []byte(`{"schemaVersion":"g004.contract.bundle.v1","laneEvents":[]}`), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/g004-conformance validate "+invalidPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "G004 Conformance")
	require.Contains(t, out.String(), "Status           invalid")
	require.Contains(t, out.String(), "/laneEvents: array must not be empty")
	require.Empty(t, errOut.String())
}

func TestGitTagCommandAndSessionTagSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello tag\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add tag notes")
	var out bytes.Buffer
	var errOut bytes.Buffer
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	sess, err := store.Open("session")
	require.NoError(t, err)
	app := &App{Workspace: workspace, Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.Tag([]string{"create", "v0.1.0", "--message", "release v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "tag"`)
	require.Contains(t, out.String(), `"name": "v0.1.0"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag release", sess))
	require.Contains(t, out.String(), "Tagged session with #release")
	require.Equal(t, "release", sess.Identity.Tag)
	out.Reset()

	require.NoError(t, app.Git([]string{"tag", "show", "v0.1.0"}))
	require.Contains(t, out.String(), "release v0.1.0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag release", sess))
	require.Contains(t, out.String(), "--confirm")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag release --confirm", sess))
	require.Contains(t, out.String(), "Removed tag #release")
	require.Empty(t, sess.Identity.Tag)
	out.Reset()

	require.NoError(t, app.Tag([]string{"delete", "v0.1.0"}))
	require.Contains(t, out.String(), "Deleted tag")
	require.Empty(t, errOut.String())
}

func TestRuntimeConfigModelAndPermissionsSlash(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			APIKey:         "api-key-secret",
			AuthToken:      "auth-token-secret",
			BaseURL:        "https://api.example.test",
			Model:          "model-a",
			MaxTokens:      1000,
			MaxTurns:       3,
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Allow: []string{"read_file"},
				Deny:  []string{"bash:rm"},
			},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/config auth", sess))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "api-key-secret")
	require.NotContains(t, out.String(), "auth-token-secret")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/model model-b", sess))
	require.Equal(t, "model-b", app.Config.Model)
	require.Contains(t, errOut.String(), "model=model-b")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/models current --json", sess))
	var currentSlashModel modelDetailReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &currentSlashModel))
	require.Equal(t, "models", currentSlashModel.Kind)
	require.Equal(t, "show", currentSlashModel.Action)
	require.Equal(t, "model-b", currentSlashModel.RequestedModel)
	require.Equal(t, "model-b", currentSlashModel.ResolvedModel)
	require.False(t, currentSlashModel.RequiresProviderRequest)
	out.Reset()

	require.NoError(t, app.RunResumedSlash(context.Background(), "/models", []string{"show", "kimi"}, config.FlagOverrides{Resume: sess.ID}, "json"))
	var resumedSlashModel modelDetailReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &resumedSlashModel))
	require.Equal(t, "models", resumedSlashModel.Kind)
	require.Equal(t, "show", resumedSlashModel.Action)
	require.Equal(t, "kimi", resumedSlashModel.RequestedModel)
	require.Equal(t, "kimi-k2.5", resumedSlashModel.ResolvedModel)
	require.Equal(t, modelrouting.ProviderDashScope, resumedSlashModel.Provider)
	out.Reset()

	app.Config.Model = "qwen2.5-coder:7b"
	app.Config.BaseURL = "http://127.0.0.1:11434/v1"
	app.Config.RuntimeProvider = modelrouting.ProviderOpenAI
	app.Config.RuntimeProviderSource = "OLLAMA_HOST"
	require.True(t, app.handleSlash(context.Background(), "/models current --json", sess))
	var ollamaSlashModel modelDetailReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &ollamaSlashModel))
	require.Equal(t, "qwen2.5-coder:7b", ollamaSlashModel.RequestedModel)
	require.Equal(t, modelrouting.ProviderOpenAI, ollamaSlashModel.Provider)
	require.Equal(t, "openai_chat_completions", ollamaSlashModel.WireProtocol)
	require.Equal(t, "http://127.0.0.1:11434/v1", ollamaSlashModel.BaseURL)
	require.Equal(t, "none", ollamaSlashModel.AuthEnv)
	require.Equal(t, "OLLAMA_HOST", ollamaSlashModel.BaseURLEnv)
	require.True(t, ollamaSlashModel.OpenAICompatible)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/max-tokens 2048", sess))
	require.Equal(t, 2048, app.Config.MaxTokens)
	require.Contains(t, errOut.String(), "max_tokens=2048")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/max-turns 6", sess))
	require.Equal(t, 6, app.Config.MaxTurns)
	require.Contains(t, errOut.String(), "max_turns=6")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions", sess))
	require.Contains(t, out.String(), `"permission_mode": "workspace-write"`)
	require.Contains(t, out.String(), `"bash:rm"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions read-only", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, out.String(), "Permissions updated")
	require.Contains(t, out.String(), "Active mode      read-only")
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"permission_mode": "read-only"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions acceptEdits", sess))
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	require.Contains(t, out.String(), "Active mode      workspace-write")
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"permission_mode": "workspace-write"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions invalid", sess))
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	require.Contains(t, errOut.String(), "unknown permission mode: invalid")
	errOut.Reset()

	require.NoError(t, app.Model([]string{"model-c"}))
	require.Equal(t, "model-c", app.Config.Model)
	require.Contains(t, out.String(), "model=model-c")
	out.Reset()

	require.NoError(t, app.MaxTokens([]string{"4096"}))
	require.Equal(t, 4096, app.Config.MaxTokens)
	require.Contains(t, out.String(), "max_tokens=4096")
	out.Reset()

	require.NoError(t, app.MaxTurns([]string{"8"}))
	require.Equal(t, 8, app.Config.MaxTurns)
	require.Contains(t, out.String(), "max_turns=8")
	out.Reset()

	require.NoError(t, app.Permissions([]string{"workspace-write"}))
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	require.Contains(t, out.String(), "Permissions updated")
	require.Contains(t, out.String(), "Previous mode    workspace-write")
	require.Contains(t, out.String(), "Active mode      workspace-write")
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"permission_mode": "workspace-write"`)
	out.Reset()

	require.NoError(t, app.Permissions([]string{"plan"}))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, out.String(), "Active mode      read-only")
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"permission_mode": "read-only"`)
	out.Reset()

	require.NoError(t, app.Permissions([]string{"clear", "--json"}))
	var permissions permissionsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &permissions))
	require.Equal(t, "clear", permissions.Action)
	require.Equal(t, "workspace-write", permissions.PermissionMode)
	require.Equal(t, filepath.Join(configHome, "config.json"), permissions.Path)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "permission_mode")
	out.Reset()

	require.NoError(t, app.AllowedTools([]string{"add", "grep"}))
	require.Contains(t, app.Config.PermissionRules.Allow, "grep")
	require.Contains(t, out.String(), "Allowed tools")
	out.Reset()

	require.NoError(t, app.AllowedTools([]string{"add", "Read", "Bash(go test:*)", "mcp__playwright__*"}))
	require.Contains(t, app.Config.PermissionRules.Allow, "Read")
	require.Contains(t, app.Config.PermissionRules.Allow, "Bash(go test:*)")
	require.Contains(t, app.Config.PermissionRules.Allow, "mcp__playwright__*")
	out.Reset()

	err = app.AllowedTools([]string{"add", "teleport"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_tool_name")
	require.NotContains(t, app.Config.PermissionRules.Allow, "teleport")
}

func TestAuthCommandReportsLocalCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CODOG_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:   configHome,
			APIKey:       "sk-ant-test-secret",
			OAuthProfile: "default",
		},
		Out: &out,
		Err: io.Discard,
	}
	require.NoError(t, app.Auth(nil))
	var report authReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "auth", report.Kind)
	require.Equal(t, "status", report.Action)
	require.True(t, report.Ready)
	require.Equal(t, "api_key", report.AuthMethod)
	require.True(t, report.APIKeyConfigured)
	require.Equal(t, "config", report.APIKeySource)
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	out.Reset()

	require.NoError(t, app.Auth([]string{"status", "--text"}))
	require.Contains(t, out.String(), "Auth")
	require.Contains(t, out.String(), "Ready            true")
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	out.Reset()

	req, err := parseAuthArgs([]string{"login", "--email", "user@example.test", "--sso", "--console", "default"})
	require.NoError(t, err)
	require.Equal(t, "login", req.Action)
	require.Equal(t, []string{"--console", "default"}, req.Rest)
}

func TestAuthCommandHonorsGlobalOutputFormat(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"api_key":"secret-key"}`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "auth"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Auth")
	require.Contains(t, out, "Method           api_key")
	require.NotContains(t, out, "secret-key")
}

func TestSetupTokenCommandStoresAuthToken(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    io.Discard,
		In:     strings.NewReader(""),
	}

	require.NoError(t, app.SetupToken([]string{"--token", "oauth-long-lived-secret", "--json"}))
	require.NotContains(t, out.String(), "oauth-long-lived-secret")
	var report setupTokenReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "setup_token", report.Kind)
	require.Equal(t, "save", report.Action)
	require.True(t, report.Configured)
	require.Equal(t, configPath, report.Path)
	require.Contains(t, report.EnvVars, "CLAUDE_CODE_OAUTH_TOKEN")
	require.Equal(t, "oauth-long-lived-secret", app.Config.AuthToken)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "oauth-long-lived-secret", persisted.AuthToken)
}

func TestSetupTokenCommandReadsStdinAndHonorsGlobalOutputFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	originalStdin := os.Stdin
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("oauth-stdin-secret\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	os.Stdin = reader
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, reader.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "setup-token", "--stdin"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "setup_token"`)
	require.Contains(t, out, `"configured": true`)
	require.NotContains(t, out, "oauth-stdin-secret")

	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "oauth-stdin-secret", persisted.AuthToken)
}

func TestAPIKeyCommandAndSlash(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CODOG_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.APIKey([]string{"status", "--json"}))
	var status apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "api_key", status.Kind)
	require.False(t, status.Configured)
	require.Empty(t, status.RedactedValue)
	out.Reset()

	require.NoError(t, app.APIKey([]string{"set", "sk-ant-test-secret", "--json"}))
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	var setReport apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &setReport))
	require.True(t, setReport.Configured)
	require.Equal(t, "config", setReport.Source)
	require.NotEmpty(t, setReport.RedactedValue)
	require.Equal(t, "sk-ant-test-secret", app.Config.APIKey)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "sk-ant-test-secret", persisted.APIKey)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/api-key clear --json", &session.Session{ID: "session"}))
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	var clearReport apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &clearReport))
	require.False(t, clearReport.Configured)
	require.Empty(t, app.Config.APIKey)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "sk-ant-test-secret")
	require.NotContains(t, string(data), "api_key")
	require.Empty(t, errOut.String())
}

func TestTemperatureCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.Temperature([]string{"--json"}))
	var status temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "temperature", status.Kind)
	require.False(t, status.Configured)
	require.Nil(t, status.Temperature)
	out.Reset()

	require.NoError(t, app.Temperature([]string{"0.7", "--json"}))
	var setReport temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &setReport))
	require.True(t, setReport.Configured)
	require.NotNil(t, setReport.Temperature)
	require.InDelta(t, 0.7, *setReport.Temperature, 0.0001)
	require.NotNil(t, app.Config.Temperature)
	require.InDelta(t, 0.7, *app.Config.Temperature, 0.0001)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.NotNil(t, persisted.Temperature)
	require.InDelta(t, 0.7, *persisted.Temperature, 0.0001)
	out.Reset()

	require.Error(t, app.Temperature([]string{"1.5"}))
	require.NotNil(t, app.Config.Temperature)
	require.InDelta(t, 0.7, *app.Config.Temperature, 0.0001)

	require.True(t, app.handleSlash(context.Background(), "/temperature clear --json", &session.Session{ID: "session"}))
	var clearReport temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &clearReport))
	require.False(t, clearReport.Configured)
	require.Nil(t, app.Config.Temperature)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "temperature")
	require.Empty(t, errOut.String())
}

func TestTelemetryCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.Telemetry([]string{"status", "--json"}))
	var status telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "telemetry", status.Kind)
	require.False(t, status.Enabled)
	require.False(t, status.Configured)
	out.Reset()

	require.NoError(t, app.Telemetry([]string{"on", "--json"}))
	var enabled telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &enabled))
	require.True(t, enabled.Enabled)
	require.True(t, enabled.Configured)
	require.NotNil(t, app.Config.Privacy.TelemetryEnabled)
	require.True(t, *app.Config.Privacy.TelemetryEnabled)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.NotNil(t, persisted.Privacy.TelemetryEnabled)
	require.True(t, *persisted.Privacy.TelemetryEnabled)
	out.Reset()

	require.NoError(t, app.Telemetry([]string{"toggle", "--json"}))
	var toggled telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &toggled))
	require.False(t, toggled.Enabled)
	require.True(t, toggled.Configured)
	require.NotNil(t, app.Config.Privacy.TelemetryEnabled)
	require.False(t, *app.Config.Privacy.TelemetryEnabled)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/telemetry clear --json", &session.Session{ID: "session"}))
	var cleared telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cleared))
	require.False(t, cleared.Enabled)
	require.False(t, cleared.Configured)
	require.Nil(t, app.Config.Privacy.TelemetryEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "telemetry_enabled")
	require.Empty(t, errOut.String())
}

func TestAdvisorCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Model:      "claude-sonnet-main",
		},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Advisor([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "advisor"`)
	require.Contains(t, out.String(), `"main_model": "claude-sonnet-main"`)
	require.NotContains(t, out.String(), `"model":`)
	out.Reset()

	require.NoError(t, app.Advisor([]string{"claude-opus-advisor", "--json"}))
	require.Equal(t, "claude-opus-advisor", app.Config.AdvisorModel)
	require.Contains(t, out.String(), `"model": "claude-opus-advisor"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"advisor_model": "claude-opus-advisor"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/advisor off", &session.Session{ID: "session"}))
	require.Empty(t, app.Config.AdvisorModel)
	require.Contains(t, out.String(), "Advisor")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "advisor_model")
	require.Empty(t, errOut.String())
}

func TestSlashCompletionCandidatesIncludeRuntimeContext(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill"), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.AppendInput("source", "hello"))
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-test"},
		Sessions:  store,
		Workspace: workspace,
	}

	candidates := app.slashCompletionCandidates("active-session")
	require.Contains(t, candidates, "/model claude-test")
	require.Contains(t, candidates, "/resume active-session")
	require.Contains(t, candidates, "/session show active-session")
	require.Contains(t, candidates, "/session exists active-session")
	require.Contains(t, candidates, "/session switch active-session")
	require.Contains(t, candidates, "/sessions show active-session")
	require.Contains(t, candidates, "/sessions exists active-session")
	require.Contains(t, candidates, "/sessions switch active-session")
	require.Contains(t, candidates, "/resume source")
	require.Contains(t, candidates, "/session show source")
	require.Contains(t, candidates, "/session exists source")
	require.Contains(t, candidates, "/session switch source")
	require.Contains(t, candidates, "/sessions show source")
	require.Contains(t, candidates, "/sessions exists source")
	require.Contains(t, candidates, "/sessions switch source")
	require.Contains(t, candidates, "/session prune")
	require.Contains(t, candidates, "/session prune --confirm")
	require.Contains(t, candidates, "/session delete ")
	require.Contains(t, candidates, "/sessions prune")
	require.Contains(t, candidates, "/sessions prune --confirm")
	require.Contains(t, candidates, "/sessions delete ")
	require.Contains(t, candidates, "/permissions workspace-write")
	require.Contains(t, candidates, "/team/review ")
	require.Contains(t, candidates, "/team/audit ")

	menu := app.slashMenuCandidates("active-session")
	require.Contains(t, menu, "/model")
	require.Contains(t, menu, "/resume")
	require.Contains(t, menu, "/team/review")
	require.Contains(t, menu, "/team/audit")
	require.NotContains(t, menu, "/model claude-test")
	require.NotContains(t, menu, "/resume active-session")
	require.NotContains(t, menu, "/permissions workspace-write")
}

func TestBookmarksArgumentParserBoundaries(t *testing.T) {
	req, err := parseBookmarksArgs([]string{
		"create", "release", "candidate",
		"--resume=resume-session",
		"--message-index=last",
		"--pull-request", "owner/repo#42",
		"--note=ready",
		"--all",
		"--output-format=json",
	}, config.FlagOverrides{Resume: "override-resume", SessionID: "override-session"})
	require.NoError(t, err)
	require.Equal(t, "add", req.Action)
	require.Equal(t, "release candidate", req.Name)
	require.Equal(t, "resume-session", req.SessionID)
	require.Equal(t, -1, req.MessageIndex)
	require.Equal(t, "owner/repo#42", req.PRRef)
	require.Equal(t, "ready", req.Note)
	require.True(t, req.All)
	require.Equal(t, "json", req.Format)

	req, err = parseBookmarksArgs([]string{"jump", "checkpoint", "--session", ""}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "show", req.Action)
	require.Equal(t, "checkpoint", req.Ref)
	require.Equal(t, "latest", req.SessionID)

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "list extra argument", args: []string{"list", "extra"}},
		{name: "add missing name", args: []string{"add"}},
		{name: "show missing ref", args: []string{"show"}},
		{name: "delete extra ref", args: []string{"delete", "one", "two"}},
		{name: "unknown action", args: []string{"unknown"}},
		{name: "missing output format", args: []string{"-o"}},
		{name: "missing session", args: []string{"--session"}},
		{name: "missing message", args: []string{"--message"}},
		{name: "invalid message", args: []string{"add", "name", "--message=0"}},
		{name: "missing pull request", args: []string{"--pr"}},
		{name: "missing note", args: []string{"--note"}},
		{name: "unknown option", args: []string{"--bogus"}},
		{name: "unknown format", args: []string{"--output-format=yaml"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseBookmarksArgs(test.args, config.FlagOverrides{})
			require.Error(t, err)
		})
	}
}

func TestBookmarksCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "first answer")))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Bookmarks([]string{"add", "checkpoint", "--session", "source", "--message", "1", "--note", "first", "--json"}, config.FlagOverrides{}))
	var report bookmarksReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "bookmarks", report.Kind)
	require.Equal(t, "add", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotNil(t, report.Bookmark)
	require.Equal(t, "checkpoint", report.Bookmark.Name)
	require.Equal(t, "source", report.Bookmark.SessionID)
	require.NotNil(t, report.Bookmark.MessageIndex)
	require.Equal(t, 0, *report.Bookmark.MessageIndex)
	require.Contains(t, report.ResumeCommand, "--resume")

	out.Reset()
	require.NoError(t, app.Bookmarks([]string{"list", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "list", report.Action)
	require.Len(t, report.Bookmarks, 1)

	out.Reset()
	handled := app.handleSlash(context.Background(), "/bookmarks add slash-mark --json", &session.Session{ID: "source"})
	require.True(t, handled)
	require.Empty(t, errOut.String())
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "add", report.Action)
	require.NotNil(t, report.Bookmark)
	require.Equal(t, "slash-mark", report.Bookmark.Name)
	require.NotNil(t, report.Bookmark.MessageIndex)
	require.Equal(t, 1, *report.Bookmark.MessageIndex)

	out.Reset()
	require.NoError(t, app.Bookmarks([]string{"show", "slash-mark", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "show", report.Action)
	require.NotNil(t, report.Bookmark)
	require.Equal(t, "slash-mark", report.Bookmark.Name)

	out.Reset()
	require.NoError(t, app.Bookmarks([]string{"delete", "checkpoint", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "delete", report.Action)
	require.Equal(t, 1, report.Removed)

	out.Reset()
	require.NoError(t, app.Bookmarks([]string{"clear", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "clear", report.Action)
	require.Equal(t, 1, report.Removed)
}

func TestSlashHelpIncludesRuntimeCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "team", "review.md"), []byte(`---
description: Review a target.
argument-hint: TARGET
---
Review $ARGUMENTS`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "status.md"), []byte(`---
description: Shadow status.
---
Shadow $ARGUMENTS`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "plugin.json"), []byte(`{"id":"demo","name":"demo","commands":["./commands/deploy.md"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "commands", "deploy.md"), []byte(`---
description: Deploy from plugin.
---
Deploy $ARGUMENTS`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte(`---
description: Audit a target.
argument-hint: SCOPE
---
# Audit
`), 0o644))
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace}

	app.renderSlashHelp(&out)

	help := out.String()
	require.Contains(t, help, "Runtime slash commands:")
	require.Contains(t, help, "/team/review TARGET")
	require.Contains(t, help, "Review a target.")
	require.Contains(t, help, "/demo/deploy")
	require.Contains(t, help, "Deploy from plugin.")
	require.Contains(t, help, "/team/audit SCOPE")
	require.Contains(t, help, "Audit a target.")
	require.NotContains(t, help, "Shadow status.")
}

func TestSlashCompleterReturnsReadlineSuffixes(t *testing.T) {
	completer := slashCompleter{candidates: []string{"/model claude-test", "/resume latest"}}

	suffixes, length := completer.Do([]rune("/model "), len([]rune("/model ")))
	require.Equal(t, len([]rune("/model ")), length)
	require.Equal(t, [][]rune{[]rune("claude-test")}, suffixes)

	suffixes, length = completer.Do([]rune("model"), len([]rune("model")))
	require.Zero(t, length)
	require.Empty(t, suffixes)
}

func TestRenderConfigInspectionSections(t *testing.T) {
	cfg := redactedConfig(config.Config{
		APIKey:         "secret",
		AuthToken:      "token",
		BaseURL:        "https://api.example.test",
		Model:          "model-a",
		AdvisorModel:   "claude-advisor",
		MaxTokens:      100,
		MaxTurns:       3,
		PermissionMode: "workspace-write",
		RAGBaseURL:     "http://rag.example.test",
		RAGTopKMax:     9,
		Future: config.FutureConfig{
			EditorBridgeSocket:        "codog.sock",
			EditorBridgeToken:         "bridge-secret",
			EnterprisePolicy:          "policy.json",
			EnterprisePolicyPublicKey: "enterprise-public-key",
			PluginMarketplaces:        []string{"https://market.example/index.json"},
			PluginMarketplaceKeys:     map[string]string{"https://market.example/index.json": "market-public-key"},
			RemoteEnabled:             true,
			RemoteAuthToken:           "remote-secret",
			RemoteLeaseSeconds:        45,
			UpdaterManifestURL:        "https://updates.example/manifest.json",
			BackgroundStatePath:       ".codog/custom-worker-state.json",
			ChromeDefaultEnabled:      boolPtr(true),
			NotificationsEnabled:      boolPtr(false),
			UltraReviewEnabled:        boolPtr(true),
			SlackAppInstallCount:      3,
			StickerOrderCount:         2,
			ExtraUsageVisitCount:      4,
			GuestPassReferralURL:      "https://example.test/pass",
			GuestPassVisitCount:       5,
			SandboxStrategy:           "detect",
			Sandbox: config.SandboxConfig{
				FilesystemMode: "allow-list",
				AllowedMounts:  []string{"logs"},
			},
		},
	})
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"get", "auth"}))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"--output-format", "json", "get", "auth"}))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"paths"}))
	require.Contains(t, out.String(), "project.json")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"model"}))
	require.Contains(t, out.String(), `"model": "model-a"`)
	require.Contains(t, out.String(), `"subagentModel": "claude-advisor"`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"model", "--output-format", "text"}))
	require.Contains(t, out.String(), "Config")
	require.Contains(t, out.String(), "model-a")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "rag", "--output-format", "json"}))
	require.Contains(t, out.String(), `"rag_base_url": "http://rag.example.test"`)
	require.Contains(t, out.String(), `"rag_top_k_max": 9`)
	require.Contains(t, out.String(), `"tool": "retrieve_context"`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "editor-bridge", "--output-format", "json"}))
	require.Contains(t, out.String(), `"socket": "codog.sock"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	require.NotContains(t, out.String(), "bridge-secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "enterprise", "--output-format", "json"}))
	require.Contains(t, out.String(), `"policy": "policy.json"`)
	require.Contains(t, out.String(), `"public_key_configured": true`)
	require.NotContains(t, out.String(), "enterprise-public-key")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "remote", "--output-format", "json"}))
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.Contains(t, out.String(), `"lease_seconds": 45`)
	require.NotContains(t, out.String(), "remote-secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "marketplace", "--output-format", "json"}))
	require.Contains(t, out.String(), `"sources"`)
	require.Contains(t, out.String(), "https://market.example/index.json")
	require.Contains(t, out.String(), `"public_keys"`)
	require.NotContains(t, out.String(), "market-public-key")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "sandbox", "--output-format", "json"}))
	require.Contains(t, out.String(), `"strategy": "detect"`)
	require.Contains(t, out.String(), `"filesystem_mode": "allow-list"`)
	require.Contains(t, out.String(), `"logs"`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "updater", "--output-format", "json"}))
	require.Contains(t, out.String(), `"manifest_url": "https://updates.example/manifest.json"`)
	require.Contains(t, out.String(), `"manifest_configured": true`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "preferences", "--output-format", "json"}))
	require.Contains(t, out.String(), `"chrome_default_enabled": true`)
	require.Contains(t, out.String(), `"chrome_configured": true`)
	require.Contains(t, out.String(), `"notifications_enabled": false`)
	require.Contains(t, out.String(), `"notifications_configured": true`)
	require.Contains(t, out.String(), `"ultrareview_enabled": true`)
	require.Contains(t, out.String(), `"ultrareview_configured": true`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "compatibility", "--output-format", "json"}))
	require.Contains(t, out.String(), `"slack_app_install_count": 3`)
	require.Contains(t, out.String(), `"sticker_order_count": 2`)
	require.Contains(t, out.String(), `"extra_usage_visit_count": 4`)
	require.Contains(t, out.String(), `"guest_pass_referral_url": "https://example.test/pass"`)
	require.Contains(t, out.String(), `"guest_pass_visit_count": 5`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"get", "background", "--output-format", "json"}))
	require.Contains(t, out.String(), `"state_path": ".codog/custom-worker-state.json"`)
	require.Contains(t, out.String(), `"state_configured": true`)
}

func TestToolRegistryUsesRAGConfig(t *testing.T) {
	app := &App{
		Config: config.Config{
			RAGBaseURL:        "http://127.0.0.1:1234",
			RAGTimeoutSeconds: 6,
			RAGTopKMax:        4,
			PermissionMode:    "read-only",
		},
		Workspace: t.TempDir(),
		In:        strings.NewReader(""),
		Err:       io.Discard,
	}

	registry, err := app.newToolRegistry()
	require.NoError(t, err)
	info, ok := registry.Info("retrieve_context")
	require.True(t, ok)
	require.Equal(t, tools.PermissionReadOnly, info.Permission)
}

func TestRenderConfigInspectionSurfacesValidationMetadata(t *testing.T) {
	cfg := redactedConfig(config.Config{
		Model:          "model-a",
		PermissionMode: "workspace-write",
		MCPServers: map[string]config.MCPServerConfig{
			"broken": {},
		},
		Hooks: config.HookConfig{
			PreToolUseCommands: []config.HookCommand{{Type: "command"}},
		},
	})
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json"}, []string{"--output-format", "json", "show"}))

	var payload struct {
		Kind           string                           `json:"kind"`
		Action         string                           `json:"action"`
		Status         string                           `json:"status"`
		MCPValidation  localstatus.MCPValidationStatus  `json:"mcp_validation"`
		HookValidation localstatus.HookValidationStatus `json:"hook_validation"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, "config", payload.Kind)
	require.Equal(t, "show", payload.Action)
	require.Equal(t, "degraded", payload.Status)
	require.Equal(t, 1, payload.MCPValidation.TotalConfigured)
	require.Equal(t, 1, payload.MCPValidation.InvalidCount)
	require.Equal(t, "broken", payload.MCPValidation.InvalidServers[0].Name)
	require.Equal(t, 1, payload.HookValidation.InvalidCount)
	require.Equal(t, "pre_tool_use", payload.HookValidation.InvalidHooks[0].Event)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json"}, []string{"--output-format", "json", "inspect"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, "config", payload.Kind)
	require.Equal(t, "inspect", payload.Action)
	require.Equal(t, "degraded", payload.Status)
}

func TestRenderConfigInspectionSurfacesParsedHookDiagnostics(t *testing.T) {
	cfg := redactedConfig(config.Config{
		Model:          "model-a",
		PermissionMode: "workspace-write",
		Hooks: config.HookConfig{
			PreToolUseCommands: []config.HookCommand{
				{Type: "command", Command: "echo ok"},
				{Type: "command", InvalidKind: "invalid_hooks_config", InvalidField: "entry", InvalidReason: "matcher must be a string"},
				{Matcher: "Write", Type: "command"},
			},
			PreToolUse: []string{"echo ok"},
		},
	})
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json"}, []string{"--output-format", "json", "show"}))

	var payload struct {
		Status         string                           `json:"status"`
		HookValidation localstatus.HookValidationStatus `json:"hook_validation"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, "degraded", payload.Status)
	require.Equal(t, 1, payload.HookValidation.ValidCount)
	require.Equal(t, 2, payload.HookValidation.InvalidCount)
	require.Equal(t, "invalid_hooks_config", payload.HookValidation.InvalidHooks[0].Kind)
	require.Equal(t, "entry", payload.HookValidation.InvalidHooks[0].ErrorField)
	require.Equal(t, "matcher must be a string", payload.HookValidation.InvalidHooks[0].Reason)
	require.Equal(t, "missing_command", payload.HookValidation.InvalidHooks[1].Kind)
	require.Equal(t, "Write", payload.HookValidation.InvalidHooks[1].Matcher)
}

func TestSettingsAliasRunsConfigInspection(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-test"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "settings", "paths", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.NotEmpty(t, report["paths"])
}

func TestConfigInspectionReportsFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	missingPath := filepath.Join(dir, "missing.json")
	badPath := filepath.Join(dir, "bad.json")
	projectPath := filepath.Join(dir, "project.json")
	require.NoError(t, os.WriteFile(userPath, []byte(`{"model":"haiku","modle":"typo","permissionMode":"read-only","env":{"A":"user","B":"user"},"hooks":{"PreToolUse":[]}}`), 0o644))
	require.NoError(t, os.WriteFile(badPath, []byte(`{`), 0o644))
	require.NoError(t, os.WriteFile(projectPath, []byte(`{"model":"sonnet","env":{"A":"project"},"mcpServers":{}}`), 0o644))

	cfg := redactedConfig(config.Config{Model: "sonnet", PermissionMode: "workspace-write"})
	var out bytes.Buffer
	require.NoError(t, renderConfigInspection(&out, cfg, []string{userPath, missingPath, badPath, projectPath}, []string{"inspect", "--json"}))

	var report struct {
		Kind   string                       `json:"kind"`
		Action string                       `json:"action"`
		Status string                       `json:"status"`
		Paths  []string                     `json:"paths"`
		Files  []configFileInspectionReport `json:"files"`
		Config config.Config                `json:"config"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "config", report.Kind)
	require.Equal(t, "inspect", report.Action)
	require.Equal(t, []string{userPath, missingPath, badPath, projectPath}, report.Paths)
	require.Len(t, report.Files, 4)

	require.Equal(t, "loaded", report.Files[0].Status)
	require.Equal(t, "explicit", report.Files[0].Source)
	require.True(t, report.Files[0].Loaded)
	require.Equal(t, 1, report.Files[0].PrecedenceRank)
	require.ElementsMatch(t, []string{"env", "hooks", "model", "modle", "permissionMode"}, report.Files[0].Keys)
	require.ElementsMatch(t, []string{"env.A", "env.B", "hooks.PreToolUse", "model", "modle", "permissionMode"}, report.Files[0].KeyPaths)
	require.ElementsMatch(t, []string{"env.B", "hooks.PreToolUse", "modle", "permissionMode"}, report.Files[0].WinsForKeys)
	require.ElementsMatch(t, []string{"env.A", "model"}, report.Files[0].ShadowedKeys)
	require.Equal(t, "warning", report.Files[0].ValidationStatus)
	require.Equal(t, 0, report.Files[0].ErrorCount)
	require.Equal(t, 2, report.Files[0].WarningCount)
	require.Len(t, report.Files[0].Warnings, 2)
	require.Equal(t, "modle", report.Files[0].Warnings[0].Field)
	require.Equal(t, "model", report.Files[0].Warnings[0].Suggestion)
	require.Equal(t, "permissionMode", report.Files[0].Warnings[1].Field)
	require.Equal(t, "permission_mode", report.Files[0].Warnings[1].Replacement)

	require.Equal(t, "not_found", report.Files[1].Status)
	require.Equal(t, "explicit", report.Files[1].Source)
	require.Equal(t, "not_found", report.Files[1].Reason)
	require.False(t, report.Files[1].Present)
	require.Equal(t, 2, report.Files[1].PrecedenceRank)

	require.Equal(t, "load_error", report.Files[2].Status)
	require.Equal(t, "explicit", report.Files[2].Source)
	require.True(t, report.Files[2].Present)
	require.Equal(t, "parse_error", report.Files[2].Reason)
	require.Contains(t, report.Files[2].Detail, "unexpected end of JSON input")
	require.Equal(t, "parse_error", report.Files[2].ErrorKind)
	require.Empty(t, report.Files[2].ValidationStatus)

	require.Equal(t, "loaded", report.Files[3].Status)
	require.Equal(t, "explicit", report.Files[3].Source)
	require.Equal(t, "warning", report.Files[3].ValidationStatus)
	require.Equal(t, 1, report.Files[3].WarningCount)
	require.Equal(t, "mcpServers", report.Files[3].Warnings[0].Field)
	require.Equal(t, "mcp_servers", report.Files[3].Warnings[0].Replacement)
	require.ElementsMatch(t, []string{"env", "mcpServers", "model"}, report.Files[3].Keys)
	require.ElementsMatch(t, []string{"env.A", "mcpServers", "model"}, report.Files[3].KeyPaths)
	require.ElementsMatch(t, []string{"env.A", "mcpServers", "model"}, report.Files[3].WinsForKeys)
	require.Empty(t, report.Files[3].ShadowedKeys)

	out.Reset()
	require.NoError(t, renderConfigInspection(&out, cfg, []string{userPath, projectPath}, []string{"paths", "--json"}))
	var pathsReport struct {
		Kind   string                       `json:"kind"`
		Action string                       `json:"action"`
		Paths  []string                     `json:"paths"`
		Files  []configFileInspectionReport `json:"files"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &pathsReport))
	require.Equal(t, "paths", pathsReport.Action)
	require.Equal(t, []string{userPath, projectPath}, pathsReport.Paths)
	require.Len(t, pathsReport.Files, 2)
	require.ElementsMatch(t, []string{"env.A", "model"}, pathsReport.Files[0].ShadowedKeys)

	sourceReports := inspectConfigFiles([]string{
		filepath.Join(dir, "config.json"),
		".claude/settings.json",
		".claude/settings.local.json",
		".claw/config.json",
		".omc/settings.local.json",
		".codog.json",
		".codog.local.json",
		"custom.json",
	})
	require.Equal(t, "user", sourceReports[0].Source)
	require.Equal(t, "project", sourceReports[1].Source)
	require.Equal(t, "local", sourceReports[2].Source)
	require.Equal(t, "project", sourceReports[3].Source)
	require.Equal(t, "local", sourceReports[4].Source)
	require.Equal(t, "project", sourceReports[5].Source)
	require.Equal(t, "local", sourceReports[6].Source)
	require.Equal(t, "explicit", sourceReports[7].Source)
}

func TestConfigValidateReportsDiagnostics(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bad-config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{\n  \"model\": 42,\n  \"modle\": \"opus\",\n  \"permissionMode\": \"plan\"\n}\n"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"config", "validate", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)

	var report struct {
		Kind         string `json:"kind"`
		Status       string `json:"status"`
		ErrorCount   int    `json:"error_count"`
		WarningCount int    `json:"warning_count"`
		Results      []struct {
			Status string `json:"status"`
			Errors []struct {
				Field    string `json:"field"`
				Kind     string `json:"kind"`
				Expected string `json:"expected"`
				Got      string `json:"got"`
			} `json:"errors"`
			Warnings []struct {
				Field       string `json:"field"`
				Kind        string `json:"kind"`
				Suggestion  string `json:"suggestion"`
				Replacement string `json:"replacement"`
			} `json:"warnings"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "config_validation", report.Kind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, 1, report.ErrorCount)
	require.Equal(t, 2, report.WarningCount)
	require.Equal(t, "error", report.Results[0].Status)
	require.Equal(t, "model", report.Results[0].Errors[0].Field)
	require.Equal(t, "wrong_type", report.Results[0].Errors[0].Kind)
	require.Equal(t, "a string", report.Results[0].Errors[0].Expected)
	require.Equal(t, "a number", report.Results[0].Errors[0].Got)
	require.Equal(t, "modle", report.Results[0].Warnings[0].Field)
	require.Equal(t, "model", report.Results[0].Warnings[0].Suggestion)
	require.Equal(t, "permissionMode", report.Results[0].Warnings[1].Field)
	require.Equal(t, "permission_mode", report.Results[0].Warnings[1].Replacement)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "config", "validate", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.NotContains(t, out, "config_load_failed")
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "config_validation", report.Kind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "model", report.Results[0].Errors[0].Field)

	textOut, textErr := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"config", "validate", "--path", configPath, "--output-format", "text"}, config.FlagOverrides{})
	})
	require.Error(t, textErr)
	require.Contains(t, textOut, "Config Validation")
	require.Contains(t, textOut, "warning:")
	require.Contains(t, textOut, "error:")
}

func TestRenderConfigInspectionMutatesConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "model", "model-b"}))
	require.Contains(t, out.String(), `"action": "set"`)
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "rate_limit.max_retries", "4"}))
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "rag_base_url", "http://127.0.0.1:8090"}))
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"unset", "model"}))
	require.Contains(t, out.String(), `"action": "unset"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"model"`)
	require.Contains(t, string(data), `"max_retries": 4`)
	require.Contains(t, string(data), `"rag_base_url": "http://127.0.0.1:8090"`)
}

func TestResetRAGConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"rag_base_url":"http://rag","rag_timeout_seconds":5,"rag_top_k_max":9,"model":"keep"}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "rag", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "rag", report.Section)
	require.ElementsMatch(t, []string{"rag_base_url", "rag_timeout_seconds", "rag_top_k_max"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "rag_base_url")
	require.NotContains(t, string(data), "rag_timeout_seconds")
	require.NotContains(t, string(data), "rag_top_k_max")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetSandboxConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"sandbox": {"strategy": "detect", "enabled": true},
		"future": {"sandbox_strategy": "bwrap", "sandbox": {"filesystem_mode": "allow-list"}}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "sandbox", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "sandbox", report.Section)
	require.ElementsMatch(t, []string{"sandbox", "future.sandbox_strategy", "future.sandbox"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"sandbox"`)
	require.NotContains(t, string(data), "sandbox_strategy")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetRemoteConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"remote": {"enabled": true, "auth_token": "secret-token", "lease_seconds": 60},
		"future": {"remote_enabled": true, "remote_auth_token": "legacy-token", "remote_lease_seconds": 30}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "remote", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "remote", report.Section)
	require.ElementsMatch(t, []string{"remote", "future.remote_enabled", "future.remote_auth_token", "future.remote_lease_seconds"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"remote"`)
	require.NotContains(t, string(data), "remote_enabled")
	require.NotContains(t, string(data), "remote_auth_token")
	require.NotContains(t, string(data), "remote_lease_seconds")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetEnterpriseConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"enterprise": {"policy": "policy.json", "policy_public_key": "public-key"},
		"future": {"enterprise_policy": "old-policy.json", "enterprise_policy_public_key": "old-key"}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "enterprise", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "enterprise", report.Section)
	require.ElementsMatch(t, []string{"enterprise", "future.enterprise_policy", "future.enterprise_policy_public_key"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"enterprise"`)
	require.NotContains(t, string(data), "enterprise_policy")
	require.NotContains(t, string(data), "enterprise_policy_public_key")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetEditorBridgeConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"editor_bridge": {"socket": "codog.sock", "token": "bridge-token"},
		"future": {"editor_bridge_socket": "legacy.sock", "editor_bridge_token": "legacy-token"}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "editor-bridge", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "editor-bridge", report.Section)
	require.ElementsMatch(t, []string{"editor_bridge", "future.editor_bridge_socket", "future.editor_bridge_token"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"editor_bridge"`)
	require.NotContains(t, string(data), "editor_bridge_socket")
	require.NotContains(t, string(data), "editor_bridge_token")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetMarketplaceConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"marketplace": {"sources": ["https://market.example/index.json"], "public_keys": {"https://market.example/index.json": "public-key"}},
		"future": {"plugin_marketplaces": ["https://old.example/index.json"], "plugin_marketplace_public_keys": {"https://old.example/index.json": "old-key"}}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "marketplace", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "marketplace", report.Section)
	require.ElementsMatch(t, []string{"marketplace", "future.plugin_marketplaces", "future.plugin_marketplace_public_keys"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"marketplace"`)
	require.NotContains(t, string(data), "plugin_marketplaces")
	require.NotContains(t, string(data), "plugin_marketplace_public_keys")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetUpdaterConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"updater": {"manifest_url": "https://updates.example/manifest.json"},
		"future": {"updater_manifest_url": "https://old.example/manifest.json"}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "updater", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "updater", report.Section)
	require.ElementsMatch(t, []string{"updater", "future.updater_manifest_url"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"updater"`)
	require.NotContains(t, string(data), "updater_manifest_url")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetPreferencesConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"preferences": {"chrome_default_enabled": true, "notifications_enabled": false, "ultrareview_enabled": true},
		"future": {"chrome_default_enabled": false, "notifications_enabled": true, "ultrareview_enabled": false}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "preferences", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "preferences", report.Section)
	require.ElementsMatch(t, []string{"preferences", "future.chrome_default_enabled", "future.notifications_enabled", "future.ultrareview_enabled"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"preferences"`)
	require.NotContains(t, string(data), "chrome_default_enabled")
	require.NotContains(t, string(data), "notifications_enabled")
	require.NotContains(t, string(data), "ultrareview_enabled")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetCompatibilityConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"compatibility": {"slack_app_install_count": 3, "sticker_order_count": 2, "extra_usage_visit_count": 4, "guest_pass_referral_url": "https://example.test/pass", "guest_pass_visit_count": 5, "guest_pass_eligibility_cache": {"org-123": {"eligible": true}}, "has_visited_passes": true, "passes_upsell_seen_count": 2, "passes_last_seen_remaining": 1},
		"future": {"slack_app_install_count": 1, "sticker_order_count": 1, "extra_usage_visit_count": 1, "guest_pass_referral_url": "https://old.example/pass", "guest_pass_visit_count": 1, "guest_pass_eligibility_cache": {"org-old": {"eligible": false}}, "has_visited_passes": true, "passes_upsell_seen_count": 1, "passes_last_seen_remaining": 1}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "compatibility", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "compatibility", report.Section)
	require.ElementsMatch(t, []string{"compatibility", "future.slack_app_install_count", "future.sticker_order_count", "future.extra_usage_visit_count", "future.guest_pass_referral_url", "future.guest_pass_eligibility_cache", "future.guest_pass_visit_count", "future.has_visited_passes", "future.passes_upsell_seen_count", "future.passes_last_seen_remaining"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"compatibility"`)
	require.NotContains(t, string(data), "slack_app_install_count")
	require.NotContains(t, string(data), "sticker_order_count")
	require.NotContains(t, string(data), "extra_usage_visit_count")
	require.NotContains(t, string(data), "guest_pass_referral_url")
	require.NotContains(t, string(data), "guest_pass_eligibility_cache")
	require.NotContains(t, string(data), "guest_pass_visit_count")
	require.NotContains(t, string(data), "has_visited_passes")
	require.NotContains(t, string(data), "passes_upsell_seen_count")
	require.NotContains(t, string(data), "passes_last_seen_remaining")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestResetBackgroundConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "keep",
		"background": {"state_path": ".codog/custom-worker-state.json"},
		"future": {"background_state_path": ".codog/old-worker-state.json"}
	}`), 0o644))

	report, changed, err := resetConfigAtPath(configPath, "background", "reset", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "background", report.Section)
	require.ElementsMatch(t, []string{"background", "future.background_state_path"}, report.ResetKeys)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"background"`)
	require.NotContains(t, string(data), "background_state_path")
	require.Contains(t, string(data), `"model": "keep"`)
}

func TestAllowedToolsSlashMutatesRuntimeAllowRules(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionRules: config.PermissionRules{Allow: []string{"read_file"}},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools", sess))
	require.Contains(t, out.String(), "read_file")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools add bash grep bash", sess))
	require.ElementsMatch(t, []string{"read_file", "bash", "grep"}, app.Config.PermissionRules.Allow)
	require.Contains(t, out.String(), "bash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools remove read_file", sess))
	require.ElementsMatch(t, []string{"bash", "grep"}, app.Config.PermissionRules.Allow)
	require.NotContains(t, out.String(), "read_file")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools clear", sess))
	require.Empty(t, app.Config.PermissionRules.Allow)
	require.Contains(t, out.String(), "no allow rules configured")
	require.Empty(t, errOut.String())
}

func TestAllowedToolsSlashRejectsUnknownToolRules(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionRules: config.PermissionRules{Allow: []string{"read_file"}},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools add teleport", sess))
	require.ElementsMatch(t, []string{"read_file"}, app.Config.PermissionRules.Allow)
	require.Empty(t, out.String())
	require.Contains(t, errOut.String(), "invalid_tool_name")
	require.Contains(t, errOut.String(), "teleport")

	out.Reset()
	errOut.Reset()
	require.True(t, app.handleSlash(context.Background(), "/allowed-tools ad bash", sess))
	require.ElementsMatch(t, []string{"read_file"}, app.Config.PermissionRules.Allow)
	require.Empty(t, out.String())
	require.Contains(t, errOut.String(), "unknown /allowed-tools action: ad")
	require.Contains(t, errOut.String(), "Did you mean: /allowed-tools add?")
}

func TestPlanCommandAndSlashEnforceReadOnlyPlanningMode(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Allow: []string{"write_file"},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Plan([]string{"inspect", "then", "edit"}))
	require.Contains(t, out.String(), "Status           active")
	require.Contains(t, out.String(), "inspect then edit")
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	effective := app.effectiveConfig()
	require.Equal(t, "read-only", effective.PermissionMode)
	require.Empty(t, effective.PermissionRules.Allow)
	require.Contains(t, app.systemPrompt(), "<codog_plan_mode")
	require.Contains(t, app.systemPrompt(), "inspect then edit")
	out.Reset()

	editorLog := filepath.Join(workspace, "plan-editor.log")
	editorScript := filepath.Join(workspace, "plan-editor.sh")
	require.NoError(t, os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755))
	t.Setenv("VISUAL", editorScript+" "+editorLog)

	require.NoError(t, app.Plan([]string{"open", "--json"}))
	require.Contains(t, out.String(), `"action": "open"`)
	require.Contains(t, out.String(), `"status": "opened"`)
	require.Contains(t, out.String(), `"opened": true`)
	openedPath, err := os.ReadFile(editorLog)
	require.NoError(t, err)
	require.Equal(t, planmode.Path(workspace)+"\n", string(openedPath))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/plan edit", sess))
	require.Contains(t, out.String(), "Opened           true")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/exit-plan", sess))
	require.Contains(t, out.String(), "Status           inactive")
	require.Empty(t, errOut.String())
	require.Equal(t, "workspace-write", app.effectiveConfig().PermissionMode)
	require.NotContains(t, app.systemPrompt(), "<codog_plan_mode")
	out.Reset()

	require.NoError(t, app.Plan([]string{"clear"}))
	out.Reset()

	require.NoError(t, app.Plan([]string{"open", "--json"}))
	require.Contains(t, out.String(), `"action": "open"`)
	require.Contains(t, out.String(), `"status": "missing"`)
	require.Contains(t, out.String(), `"editor_error": "no plan written yet"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ultraplan inspect the release", sess))
	require.Contains(t, out.String(), "Status           active")
	require.Contains(t, out.String(), "inspect the release")
}

func TestDoctorCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Prefer focused changes."), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Deny: []string{"Bash(rm:*)", "Bsh(echo:*)"},
			},
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Doctor(nil))
	require.Contains(t, out.String(), "Doctor")
	require.Contains(t, out.String(), "Auth")
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "Permissions")
	require.Contains(t, out.String(), "Permission rules")
	out.Reset()

	require.NoError(t, app.Doctor([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "doctor"`)
	require.Contains(t, out.String(), `"name": "Auth"`)
	var doctorReport doctor.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &doctorReport))
	require.Equal(t, "1.0", doctorReport.SchemaVersion)
	require.Contains(t, doctorReport.OutputFields, "check_names")
	require.Contains(t, doctorReport.CheckNames, "auth")
	require.Contains(t, doctorReport.CheckNames, "memory")
	require.Contains(t, doctorReport.CheckNames, "permission rules")
	require.Equal(t, []string{doctor.StatusOK, doctor.StatusWarn, doctor.StatusFail}, doctorReport.StatusValues)
	var sandboxCheck doctor.Check
	var permissionRulesCheck doctor.Check
	var memoryCheck doctor.Check
	for _, check := range doctorReport.Checks {
		if check.Name == "Sandbox" {
			sandboxCheck = check
		}
		if check.Name == "Permission rules" {
			permissionRulesCheck = check
		}
		if check.Name == "Memory" {
			memoryCheck = check
		}
	}
	require.Equal(t, "Memory", memoryCheck.Name)
	require.Equal(t, float64(1), memoryCheck.Data["file_count"])
	require.Equal(t, float64(3), memoryCheck.Data["total_words"])
	require.Equal(t, float64(len("Prefer focused changes.")), memoryCheck.Data["total_bytes"])
	require.Contains(t, strings.Join(memoryCheck.Details, "\n"), "words=3")
	require.Contains(t, strings.Join(memoryCheck.Details, "\n"), "bytes=23")
	require.Equal(t, doctor.StatusWarn, permissionRulesCheck.Status)
	require.Equal(t, float64(1), permissionRulesCheck.Data["unknown_count"])
	require.Equal(t, "Sandbox", sandboxCheck.Name)
	require.Contains(t, sandboxCheck.Data, "enabled")
	require.Contains(t, sandboxCheck.Data, "filesystem_mode")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/doctor", sess))
	require.Contains(t, out.String(), "Doctor")
	require.NotContains(t, errOut.String(), "unknown slash command")
}

func TestUnknownSlashInREPLShowsSuggestions(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/statuz", &session.Session{ID: "session"}))
	require.Contains(t, errOut.String(), "unknown slash command: /statuz")
	require.Contains(t, errOut.String(), "Did you mean:")
	require.Contains(t, errOut.String(), "/status")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/team/reveiw", &session.Session{ID: "session"}))
	require.Contains(t, errOut.String(), "unknown slash command: /team/reveiw")
	require.Contains(t, errOut.String(), "/team/review")
}

func TestHelpSlashInREPLRendersHelp(t *testing.T) {
	var errOut bytes.Buffer
	app := &App{Err: &errOut}

	require.True(t, app.handleSlash(context.Background(), "/help", &session.Session{ID: "session"}))
	require.Contains(t, errOut.String(), "/help")
	require.Contains(t, errOut.String(), "/status")
	require.Contains(t, errOut.String(), "/mock-parity")
	require.NotContains(t, errOut.String(), "unknown slash command")
}

func TestDirectCustomSlashRunsOMCCommand(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode", "hud.md"), []byte(`---
allowed-tools: Read
---
OMC HUD $ARGUMENTS`), 0o644))
	requests := []json.RawMessage{}
	server := httptest.NewServer(mockanthropic.Server{
		Text: "omc direct ok",
		OnRequest: func(data json.RawMessage) {
			requests = append(requests, data)
		},
	}.Handler())
	defer server.Close()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/oh-my-claudecode:hud", "session"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "prompt"`)
	require.Contains(t, out, "omc direct ok")
	require.Len(t, requests, 1)
	require.Contains(t, string(requests[0]), "OMC HUD session")
	require.NotContains(t, out, "unknown_slash_command")
}

func TestResumedCustomSlashRunsOMCCommand(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode", "hud.md"), []byte("Resumed OMC ${CLAUDE_SESSION_ID} $ARGUMENTS"), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("resume-omc", anthropic.TextMessage("user", "existing context")))
	requests := []json.RawMessage{}
	server := httptest.NewServer(mockanthropic.Server{
		Text: "resumed omc ok",
		OnRequest: func(data json.RawMessage) {
			requests = append(requests, data)
		},
	}.Handler())
	defer server.Close()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-omc", "--output-format", "json", "/oh-my-claudecode:hud", "panel"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "prompt"`)
	require.Contains(t, out, `"session_id": "resume-omc"`)
	require.Contains(t, out, "resumed omc ok")
	require.Len(t, requests, 1)
	require.Contains(t, string(requests[0]), "Resumed OMC resume-omc panel")
	opened, err := store.OpenExisting("resume-omc")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(opened.Messages), 3)
}

func TestDoctorReportsConfigValidationChecks(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
			MCPServers:     map[string]config.MCPServerConfig{"missing": {}},
			Hooks:          config.HookConfig{PreToolUseCommands: []config.HookCommand{{Type: "http"}}},
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Doctor([]string{"--json"}))
	var report doctor.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	mcpValidation := doctor.Check{}
	hookValidation := doctor.Check{}
	for _, check := range report.Checks {
		switch check.Name {
		case "MCP validation":
			mcpValidation = check
		case "Hook validation":
			hookValidation = check
		}
	}
	require.Equal(t, doctor.StatusWarn, mcpValidation.Status)
	require.Equal(t, float64(1), mcpValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(mcpValidation.Details, "\n"), "missing command")
	require.Equal(t, doctor.StatusWarn, hookValidation.Status)
	require.Equal(t, float64(1), hookValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(hookValidation.Details, "\n"), "pre_tool_use")
}

func TestDoctorDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "doctor"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var report doctor.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "doctor", report.Kind)
	require.Equal(t, doctor.StatusFail, report.Status)
	require.True(t, report.HasFailures)
	configCheck := doctor.Check{}
	for _, check := range report.Checks {
		if check.Name == "Config" {
			configCheck = check
			break
		}
	}
	require.Equal(t, "Config", configCheck.Name)
	require.Equal(t, doctor.StatusFail, configCheck.Status)
	require.Contains(t, configCheck.Summary, "failed to load")
	loadError, ok := configCheck.Data["load_error"].(string)
	require.True(t, ok)
	require.Contains(t, loadError, "broken.json")
	require.Equal(t, "config_load_failed", configCheck.Data["load_error_kind"])

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/doctor"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, doctor.StatusFail, report.Status)
}

func TestStatusCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0o644))
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Status memory."), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "status me")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Allow: []string{"read_file", "grep"},
				Deny:  []string{"Bash(rm:*)", "Bsh(echo:*)"},
			},
			MaxTokens:           1000,
			MaxTurns:            4,
			AutoCompactMessages: 20,
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Status(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Status")
	require.Contains(t, out.String(), "Model            claude-test")
	require.Contains(t, out.String(), "Memory files     1")
	require.Contains(t, out.String(), "Task lanes       active=0 blocked=0 finished=0")
	require.Contains(t, out.String(), "Tools            87")
	out.Reset()

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{Resume: "source"}))
	require.Contains(t, out.String(), `"kind": "status"`)
	require.Contains(t, out.String(), `"memory_file_count": 1`)
	require.Contains(t, out.String(), `"id": "source"`)
	require.Contains(t, out.String(), `"message_count": 1`)
	require.Contains(t, out.String(), `"lane_board": {`)
	require.Contains(t, out.String(), `"status_json_supported": true`)
	require.Contains(t, out.String(), `"transport_dead"`)
	require.Contains(t, out.String(), `"active_count": 0`)
	var statusReport struct {
		FormatSource     string `json:"format_source"`
		FormatRaw        string `json:"format_raw"`
		FormatOverridden bool   `json:"format_overridden"`
		Workspace        struct {
			MemoryFiles []struct {
				Name           string `json:"name"`
				Source         string `json:"source"`
				Origin         string `json:"origin"`
				ScopePath      string `json:"scope_path"`
				OutsideProject bool   `json:"outside_project"`
				Lines          int    `json:"lines"`
				Words          int    `json:"words"`
				SizeBytes      int64  `json:"size_bytes"`
				ModifiedAt     string `json:"modified_at"`
				AgeSeconds     int64  `json:"age_seconds"`
				Empty          bool   `json:"empty"`
				Contributes    bool   `json:"contributes"`
			} `json:"memory_files"`
		} `json:"workspace"`
		AllowedTools struct {
			Source     string            `json:"source"`
			Restricted bool              `json:"restricted"`
			Entries    []string          `json:"entries"`
			Available  []string          `json:"available"`
			Aliases    map[string]string `json:"aliases"`
		} `json:"allowed_tools"`
		Config struct {
			PermissionRules struct {
				Deny []struct {
					Raw              string `json:"raw"`
					Tool             string `json:"tool"`
					ResolvedToolName string `json:"resolved_tool_name"`
					Matcher          string `json:"matcher"`
					UnknownTool      bool   `json:"unknown_tool"`
				} `json:"deny"`
				UnknownCount int `json:"unknown_count"`
			} `json:"permission_rules"`
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "flag", statusReport.FormatSource)
	require.Equal(t, "json", statusReport.FormatRaw)
	require.False(t, statusReport.FormatOverridden)
	require.Len(t, statusReport.Workspace.MemoryFiles, 1)
	require.Equal(t, "AGENTS.md", statusReport.Workspace.MemoryFiles[0].Name)
	require.Equal(t, "agents_md", statusReport.Workspace.MemoryFiles[0].Source)
	require.Equal(t, "workspace", statusReport.Workspace.MemoryFiles[0].Origin)
	require.Equal(t, canonicalWorkspace, statusReport.Workspace.MemoryFiles[0].ScopePath)
	require.False(t, statusReport.Workspace.MemoryFiles[0].OutsideProject)
	require.Equal(t, 1, statusReport.Workspace.MemoryFiles[0].Lines)
	require.Equal(t, 2, statusReport.Workspace.MemoryFiles[0].Words)
	require.Equal(t, int64(len("Status memory.")), statusReport.Workspace.MemoryFiles[0].SizeBytes)
	require.NotEmpty(t, statusReport.Workspace.MemoryFiles[0].ModifiedAt)
	require.GreaterOrEqual(t, statusReport.Workspace.MemoryFiles[0].AgeSeconds, int64(0))
	require.False(t, statusReport.Workspace.MemoryFiles[0].Empty)
	require.True(t, statusReport.Workspace.MemoryFiles[0].Contributes)
	require.Equal(t, "configured", statusReport.AllowedTools.Source)
	require.True(t, statusReport.AllowedTools.Restricted)
	require.Equal(t, []string{"read_file", "grep"}, statusReport.AllowedTools.Entries)
	require.Contains(t, statusReport.AllowedTools.Available, "read_file")
	require.Equal(t, "web_fetch", statusReport.AllowedTools.Aliases["WebFetch"])
	require.Len(t, statusReport.Config.PermissionRules.Deny, 2)
	require.Equal(t, "Bash(rm:*)", statusReport.Config.PermissionRules.Deny[0].Raw)
	require.Equal(t, "Bash", statusReport.Config.PermissionRules.Deny[0].Tool)
	require.Equal(t, "bash", statusReport.Config.PermissionRules.Deny[0].ResolvedToolName)
	require.Equal(t, "rm", statusReport.Config.PermissionRules.Deny[0].Matcher)
	require.True(t, statusReport.Config.PermissionRules.Deny[1].UnknownTool)
	require.Equal(t, 1, statusReport.Config.PermissionRules.UnknownCount)
	out.Reset()

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{AllowedTools: []string{"read_file"}}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusReport))
	require.Equal(t, "flag", statusReport.AllowedTools.Source)
	require.True(t, statusReport.AllowedTools.Restricted)
	out.Reset()

	t.Setenv("CODOG_OUTPUT_FORMAT", "json")
	t.Setenv("CODOG_MODEL", "")
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-4-7")
	outText, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var envStatus localstatus.Snapshot
	require.NoError(t, json.Unmarshal([]byte(outText), &envStatus))
	require.Equal(t, "env", envStatus.FormatSource)
	require.Equal(t, "json", envStatus.FormatRaw)
	require.False(t, envStatus.FormatOverridden)
	require.Equal(t, "claude-opus-4-7", envStatus.Config.Model)
	require.Equal(t, "ANTHROPIC_MODEL", envStatus.Config.ModelEnvVar)

	outText, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(outText), &envStatus))
	require.Equal(t, "flag", envStatus.FormatSource)
	require.Equal(t, "json", envStatus.FormatRaw)
	require.True(t, envStatus.FormatOverridden)

	sess := &session.Session{ID: "source", Messages: []anthropic.Message{anthropic.TextMessage("user", "slash")}}
	require.True(t, app.handleSlash(context.Background(), "/status", sess))
	require.Contains(t, out.String(), "Session          source (1 messages)")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/STATUS", sess))
	require.Contains(t, out.String(), "Session          source (1 messages)")
	out.Reset()

	require.NoError(t, app.Statusline([]string{"--json"}, config.FlagOverrides{Resume: "source"}))
	require.Contains(t, out.String(), `"kind": "statusline"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	require.Contains(t, out.String(), `"model": "claude-test"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/statusline", sess))
	require.Contains(t, out.String(), "codog")
	require.Contains(t, out.String(), "claude-test")
	require.Contains(t, out.String(), "session=source(1)")
}

func TestStatusValidationReportsDegradedConfig(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Model:      "claude-test",
			MCPServers: map[string]config.MCPServerConfig{
				"missing": {},
				"ok":      {Command: "codog-test-mcp"},
			},
			Hooks: config.HookConfig{
				PreToolUseCommands: []config.HookCommand{
					{Type: "command", Command: "echo ok"},
					{Type: "command"},
					{Type: "http"},
					{Type: "webhook", Command: "echo no"},
					{Type: "prompt", Prompt: "summarize payload"},
					{Type: "agent", Prompt: "inspect payload"},
				},
				SessionStart: []string{"echo session"},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{}))
	var snapshot localstatus.Snapshot
	require.NoError(t, json.Unmarshal(out.Bytes(), &snapshot))
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, 2, snapshot.MCPValidation.TotalConfigured)
	require.Equal(t, 1, snapshot.MCPValidation.ValidCount)
	require.Equal(t, 0, snapshot.MCPValidation.RequiredCount)
	require.Equal(t, 2, snapshot.MCPValidation.OptionalCount)
	require.Equal(t, 1, snapshot.MCPValidation.InvalidCount)
	require.Len(t, snapshot.MCPValidation.InvalidServers, 1)
	require.Equal(t, "missing", snapshot.MCPValidation.InvalidServers[0].Name)
	require.Equal(t, "missing_command", snapshot.MCPValidation.InvalidServers[0].Kind)
	require.Equal(t, "command", snapshot.MCPValidation.InvalidServers[0].ErrorField)
	require.Equal(t, 4, snapshot.HookValidation.ValidCount)
	require.Equal(t, 3, snapshot.HookValidation.InvalidCount)
	require.Len(t, snapshot.HookValidation.InvalidHooks, 3)
	kinds := map[string]string{}
	for _, issue := range snapshot.HookValidation.InvalidHooks {
		require.Equal(t, "pre_tool_use", issue.Event)
		require.NotNil(t, issue.Index)
		require.NotNil(t, issue.HookIndex)
		kinds[issue.Kind] = issue.ErrorField
	}
	require.Equal(t, "command", kinds["missing_command"])
	require.Equal(t, "url", kinds["missing_url"])
	require.Equal(t, "type", kinds["unsupported_type"])
	out.Reset()

	require.NoError(t, app.Status(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "MCP validation   valid=1 invalid=1 required=0 optional=2")
	require.Contains(t, out.String(), "Hook validation  valid=4 invalid=3")
}

func TestStatusSurfacesConfigValidation(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":42}`), 0o644))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			Model:      "claude-test",
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{ConfigPath: configPath}))
	var snapshot localstatus.Snapshot
	require.NoError(t, json.Unmarshal(out.Bytes(), &snapshot))
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "error", snapshot.ConfigValidation.Status)
	require.Equal(t, 1, snapshot.ConfigValidation.FileCount)
	require.Equal(t, 1, snapshot.ConfigValidation.PresentCount)
	require.Equal(t, 1, snapshot.ConfigValidation.ErrorCount)
	require.Equal(t, []string{configPath}, snapshot.ConfigValidation.Paths)
	out.Reset()

	require.NoError(t, app.Status(nil, config.FlagOverrides{ConfigPath: configPath}))
	require.Contains(t, out.String(), "Config validation status=error files=1 present=1 errors=1 warnings=0")
}

func TestStatuslineReadsClaudeStdinContract(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	fastMode := true
	app := &App{
		Config: config.Config{
			ConfigHome:     t.TempDir(),
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			FastMode:       &fastMode,
		},
		Workspace: workspace,
		Out:       &out,
		In: strings.NewReader(`{
			"session_id": "claude-session",
			"session_name": "Readable Session",
			"transcript_path": "/tmp/claude-session.jsonl",
			"cwd": "` + filepath.ToSlash(workspace) + `",
			"permission_mode": "acceptEdits",
			"model": {"id": "claude-sonnet-4-5", "display_name": "Claude Sonnet 4.5"},
			"workspace": {
				"current_dir": "` + filepath.ToSlash(workspace) + `",
				"project_dir": "` + filepath.ToSlash(workspace) + `",
				"added_dirs": ["` + filepath.ToSlash(filepath.Join(workspace, "extra")) + `"]
			},
			"version": "1.0.71",
			"output_style": {"name": "default"},
			"cost": {"total_cost_usd": 0.1234},
			"context_window": {
				"total_input_tokens": 1200,
				"total_output_tokens": 300,
				"context_window_size": 200000,
				"remaining_percentage": 88.5
			},
			"agent": {"name": "statusline-setup"},
			"worktree": {"name": "feature-work", "branch": "feature/statusline"}
		}`),
	}

	require.NoError(t, app.Statusline([]string{"--json"}, config.FlagOverrides{}))
	jsonOut := out.String()
	require.Contains(t, jsonOut, `"source": "claude_statusline_stdin"`)
	require.Contains(t, jsonOut, `"session_id": "claude-session"`)
	require.Contains(t, jsonOut, `"session_name": "Readable Session"`)
	require.Contains(t, jsonOut, `"model": "Claude Sonnet 4.5"`)
	require.Contains(t, jsonOut, `"permission_mode": "acceptEdits"`)
	require.Contains(t, jsonOut, `"context_remaining_percentage": 88.5`)
	require.Contains(t, jsonOut, `"total_cost_usd": 0.1234`)

	out.Reset()
	app.In = strings.NewReader(`{"session_id":"claude-session","transcript_path":"/tmp/claude-session.jsonl","cwd":"` + filepath.ToSlash(workspace) + `","model":{"display_name":"Claude Sonnet 4.5"},"context_window":{"remaining_percentage":88.5}}`)
	require.NoError(t, app.Statusline(nil, config.FlagOverrides{}))
	line := out.String()
	require.Contains(t, line, "codog")
	require.Contains(t, line, filepath.Base(workspace))
	require.Contains(t, line, "Claude Sonnet 4.5")
	require.Contains(t, line, "session=claude-session")
	require.Contains(t, line, "context=88%-left")
}

func TestStatuslineRunsConfiguredCommand(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     t.TempDir(),
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			StatusLine: &config.StatusLineConfig{
				Type:    "command",
				Command: "echo custom-status",
			},
		},
		Workspace: workspace,
		Out:       &out,
	}

	require.NoError(t, app.Statusline(nil, config.FlagOverrides{}))
	require.Equal(t, "custom-status\n", out.String())

	out.Reset()
	require.NoError(t, app.Statusline([]string{"--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"source": "statusLine_command"`)
	require.Contains(t, out.String(), `"line": "custom-status"`)
}

func TestStatuslineSkipsConfiguredCommandWhenHooksDisabled(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	disabled := true
	app := &App{
		Config: config.Config{
			ConfigHome:      t.TempDir(),
			Model:           "claude-test",
			PermissionMode:  "workspace-write",
			DisableAllHooks: &disabled,
			StatusLine:      &config.StatusLineConfig{Type: "command", Command: "echo custom-status"},
		},
		Workspace: workspace,
		Out:       &out,
	}

	require.NoError(t, app.Statusline(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "codog")
	require.Contains(t, out.String(), "claude-test")
	require.NotContains(t, out.String(), "custom-status")
}

func TestStatusLineCommandInputUsesClaudeShape(t *testing.T) {
	workspace := t.TempDir()
	app := &App{
		Config: config.Config{
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			AdditionalDirs: []string{"extra"},
		},
		Workspace: workspace,
	}
	payload, err := app.statusLineCommandInput(statuslineReport{
		SessionID:      "session-123",
		Model:          "claude-test",
		PermissionMode: "workspace-write",
	}, claudeStatuslineInput{}, false)

	require.NoError(t, err)
	var input claudeStatuslineInput
	require.NoError(t, json.Unmarshal(payload, &input))
	require.Equal(t, "session-123", input.SessionID)
	require.Equal(t, "claude-test", input.Model.ID)
	require.Equal(t, "workspace-write", input.PermissionMode)
	require.Equal(t, workspace, input.Workspace.CurrentDir)
	require.Equal(t, workspace, input.Workspace.ProjectDir)
	require.Equal(t, []string{"extra"}, input.Workspace.AddedDirs)
	require.NotEmpty(t, input.Version)
}

func TestStatusDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var snapshot localstatus.Snapshot
	require.NoError(t, json.Unmarshal([]byte(out), &snapshot))
	require.Equal(t, "status", snapshot.Kind)
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "config_load_failed", snapshot.ConfigLoadErrorKind)
	require.Contains(t, snapshot.ConfigLoadError, "broken.json")
	require.Contains(t, snapshot.ConfigLoadError, "unexpected end of JSON input")
	require.NotEmpty(t, snapshot.Workspace.Path)
	require.NotEmpty(t, snapshot.Config.ConfigHome)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &snapshot))
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "config_load_failed", snapshot.ConfigLoadErrorKind)
}

func TestShellCompletionBypassesConfigLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "completion", "fish"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "complete -c codog")
	require.Contains(t, out, "__fish_seen_subcommand_from completion")
	require.Contains(t, out, "-l agents")
	require.Contains(t, out, "-l plugin-dir")
	require.Contains(t, out, "-l setting-sources")
	require.Contains(t, out, "-l ide")
	require.NotContains(t, out, "config_load_failed")

	for _, shell := range []string{"bash", "zsh"} {
		script, scriptErr := shellCompletionScript(shell)
		require.NoError(t, scriptErr)
		require.Contains(t, script, "--plugin-dir")
		require.Contains(t, script, "--setting-sources")
	}
}

func TestStatusIncludesBranchFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	runGit(t, workspace, "switch", "-c", "topic")
	runGit(t, workspace, "switch", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: main update")
	runGit(t, workspace, "switch", "topic")

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "claude-test", BaseURL: "https://api.example.test"},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"status": "warn"`)
	require.Contains(t, out.String(), `"head_sha":`)
	require.Contains(t, out.String(), `"head_short_sha":`)
	require.Contains(t, out.String(), `"head_ref": "topic"`)
	require.Contains(t, out.String(), `"is_detached": false`)
	require.Contains(t, out.String(), `"is_bare": false`)
	require.Contains(t, out.String(), `"is_worktree": false`)
	require.Contains(t, out.String(), `"freshness": {`)
	require.Contains(t, out.String(), `"status": "stale"`)
	require.Contains(t, out.String(), `"behind": 1`)
	require.Contains(t, out.String(), `"fix: main update"`)
}

func TestStatusIncludesBaseCommitCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	baseSHA := strings.TrimSpace(runGitOutput(t, workspace, "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog-base"), []byte(baseSHA+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "next.txt"), []byte("next\n"), 0o644))
	runGit(t, workspace, "add", "next.txt")
	runGit(t, workspace, "commit", "-m", "feat: next")

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "claude-test", BaseURL: "https://api.example.test"},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"status": "warn"`)
	require.Contains(t, out.String(), `"base_commit": {`)
	require.Contains(t, out.String(), `"status": "diverged"`)
	require.Contains(t, out.String(), `"matches": false`)
	require.Contains(t, out.String(), `"kind": "codog_file"`)
	require.Contains(t, out.String(), `"expected": "`+baseSHA+`"`)
	require.Contains(t, out.String(), "stale codebase")
	var snapshot struct {
		BootPreflight struct {
			Repo struct {
				Identity struct {
					HeadRef string `json:"head_ref"`
					HeadSHA string `json:"head_sha"`
				} `json:"identity"`
				BaseCommit struct {
					Status   string `json:"status"`
					Matches  bool   `json:"matches"`
					Expected string `json:"expected"`
				} `json:"base_commit"`
			} `json:"repo"`
		} `json:"boot_preflight"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &snapshot))
	require.Equal(t, "main", snapshot.BootPreflight.Repo.Identity.HeadRef)
	require.NotEmpty(t, snapshot.BootPreflight.Repo.Identity.HeadSHA)
	require.Equal(t, "diverged", snapshot.BootPreflight.Repo.BaseCommit.Status)
	require.False(t, snapshot.BootPreflight.Repo.BaseCommit.Matches)
	require.Equal(t, baseSHA, snapshot.BootPreflight.Repo.BaseCommit.Expected)
}

func TestHistoryCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.AppendInput("source", "first prompt"))
	require.NoError(t, store.AppendInput("source", "second prompt"))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.History([]string{"--session", "source", "--limit", "1"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Prompt history")
	require.Contains(t, out.String(), "Showing          1 most recent")
	require.Contains(t, out.String(), "second prompt")
	require.NotContains(t, out.String(), "first prompt")
	out.Reset()

	require.NoError(t, app.History([]string{"--session=source", "--json", "--offset", "0", "--limit", "1"}, config.FlagOverrides{}))
	var historyReport struct {
		Kind       string `json:"kind"`
		Total      int    `json:"total"`
		Showing    int    `json:"showing"`
		Limit      int    `json:"limit"`
		Offset     int    `json:"offset"`
		HasMore    bool   `json:"has_more"`
		NextOffset int    `json:"next_offset"`
		Entries    []struct {
			Index int    `json:"index"`
			Role  string `json:"role"`
			Text  string `json:"text"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &historyReport))
	require.Equal(t, "prompt_history", historyReport.Kind)
	require.Equal(t, 2, historyReport.Total)
	require.Equal(t, 1, historyReport.Showing)
	require.Equal(t, 1, historyReport.Limit)
	require.Equal(t, 0, historyReport.Offset)
	require.True(t, historyReport.HasMore)
	require.Equal(t, 1, historyReport.NextOffset)
	require.Len(t, historyReport.Entries, 1)
	require.Equal(t, 1, historyReport.Entries[0].Index)
	require.Equal(t, "user", historyReport.Entries[0].Role)
	require.Equal(t, "first prompt", historyReport.Entries[0].Text)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/history 1", &session.Session{ID: "source"}))
	require.Contains(t, out.String(), "second prompt")
	require.Empty(t, errOut.String())
}

func TestSearchCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n// TODO: search me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("TODO: docs\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Search(context.Background(), []string{"todo", "--ignore-case", "--glob", "*.go", "--limit", "1"}))
	require.Contains(t, out.String(), "Search")
	require.Contains(t, out.String(), "Matches          1")
	require.Contains(t, out.String(), "main.go:2:// TODO: search me")
	require.NotContains(t, out.String(), "README.md")
	out.Reset()

	require.NoError(t, app.Search(context.Background(), []string{"TODO", "--json"}))
	require.Contains(t, out.String(), `"kind": "search"`)
	require.Contains(t, out.String(), `"total": 2`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/search TODO --glob=*.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "README.md:1:TODO: docs")
	require.Empty(t, errOut.String())
}

func TestFilesCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".hidden", "secret.go"), []byte("package hidden\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("ignored.go\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.go"), []byte("package ignored\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Files([]string{"--glob", "*.go", "--json"}))
	require.Contains(t, out.String(), `"kind": "files"`)
	require.Contains(t, out.String(), `"scope_risk"`)
	require.Contains(t, out.String(), `"status": "clean"`)
	require.Contains(t, out.String(), `"path": "pkg/main.go"`)
	require.NotContains(t, out.String(), "ignored.go")
	require.NotContains(t, out.String(), "secret.go")
	out.Reset()

	respectGitignore := false
	app.Config.RespectGitignore = &respectGitignore
	require.NoError(t, app.Files([]string{"--glob", "*.go", "--json"}))
	require.Contains(t, out.String(), `"path": "ignored.go"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/files --glob=*.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Files")
	require.Contains(t, out.String(), "README.md")
	require.Empty(t, errOut.String())
}

func TestTUIFileReferenceCandidatesRespectGitignore(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("ignored.go\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.go"), []byte("package ignored\n"), 0o644))
	app := &App{Workspace: workspace}

	candidates := app.tuiFileReferenceCandidates()
	require.Contains(t, candidates, "README.md")
	require.Contains(t, candidates, "pkg/main.go")
	require.NotContains(t, candidates, "ignored.go")

	respectGitignore := false
	app.Config.RespectGitignore = &respectGitignore
	candidates = app.tuiFileReferenceCandidates()
	require.Contains(t, candidates, "ignored.go")
}

func TestScopeCommandAppliesAndRestoresSaferWorkspace(t *testing.T) {
	workspace := t.TempDir()
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	workspace = resolvedWorkspace
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "dist"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app", "main.go"), []byte("package app\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "debug.log"), []byte("trace\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Scope([]string{"preview", "--json"}))
	require.Contains(t, out.String(), `"kind": "safer_scope"`)
	require.Contains(t, out.String(), `"status": "actionable"`)
	require.Contains(t, out.String(), `"id": "workspace"`)
	out.Reset()

	require.NoError(t, app.Scope([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"action": "status"`)
	require.Contains(t, out.String(), `"status": "inactive"`)
	out.Reset()

	require.NoError(t, app.Scope([]string{"apply", "--json"}))
	require.Equal(t, filepath.Join(workspace, "app"), app.Workspace)
	require.Contains(t, out.String(), `"confirmed": true`)
	require.Contains(t, out.String(), `"active_workspace":`)
	out.Reset()

	require.NoError(t, app.Scope([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"action": "status"`)
	require.Contains(t, out.String(), `"status": "applied"`)
	require.Contains(t, out.String(), `"applied_choice": "workspace"`)
	require.Contains(t, out.String(), `"restore_command": "codog scope restore"`)
	out.Reset()

	require.NoError(t, app.Scope([]string{"restore", "--json"}))
	require.Equal(t, workspace, app.Workspace)
	require.Contains(t, out.String(), `"restored": true`)
	out.Reset()

	require.NoError(t, app.Scope([]string{"state", "--output-format", "text"}))
	require.Contains(t, out.String(), "Safer Scope")
	require.Contains(t, out.String(), "Status           inactive")
	require.Empty(t, errOut.String())
}

func TestScopeCommandAppliesAppendIgnoreBlockChoice(t *testing.T) {
	workspace := t.TempDir()
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	workspace = resolvedWorkspace
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app", "main.go"), []byte("package app\n"), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Scope([]string{"apply", "--choice", "append_ignore_block", "--json"}))
	require.Equal(t, workspace, app.Workspace)
	require.Contains(t, out.String(), `"applied_choice": "ignore"`)
	require.Contains(t, out.String(), `"action": "create_ignore_file"`)
	data, err := os.ReadFile(filepath.Join(workspace, ".codogignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), "node_modules/")
	require.Empty(t, errOut.String())
}

func TestRunAndProjectCommandSurfaces(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/cmdsurf\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "add.go"), []byte("package cmdsurf\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "add_test.go"), []byte("package cmdsurf\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"bad add\") } }\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.RunCommand(context.Background(), []string{"--json", "go", "version"}))
	require.Contains(t, out.String(), `"kind": "run"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	nodeCommand, err := languageCommand(workspace, "node", []string{"console.log(1)"})
	require.NoError(t, err)
	require.Equal(t, []string{"node", "-e", "console.log(1)"}, nodeCommand)
	scriptPath := filepath.Join(workspace, "script.py")
	require.NoError(t, os.WriteFile(scriptPath, []byte("print(1)\n"), 0o644))
	pythonCommand, err := languageCommand(workspace, "python", []string{"script.py", "arg"})
	require.NoError(t, err)
	require.Len(t, pythonCommand, 3)
	require.Equal(t, scriptPath, pythonCommand[1])
	require.Equal(t, "arg", pythonCommand[2])

	require.NoError(t, app.ProjectCommand(context.Background(), "test", nil))
	require.Contains(t, out.String(), "Command")
	require.Contains(t, out.String(), "go test ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/build", sess))
	require.Contains(t, out.String(), "go build ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/lint", sess))
	require.Contains(t, out.String(), "go vet ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/run go version", sess))
	require.Contains(t, out.String(), "go version")
	require.Empty(t, errOut.String())
}

func TestCodeIntelligenceCommandsAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/intel\n\ngo 1.22\n"), 0o644))
	source := "package intel\n\ntype Runner struct{}\n\nfunc Run() Runner { return Runner{} }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "runner.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package intel\n\nfunc messy(){return}\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Symbols(nil))
	require.Contains(t, out.String(), "runner.go:3:type Runner")
	require.Contains(t, out.String(), "runner.go:5:function Run")
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--output-format", "json",
			"symbols",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliSymbols symbolsReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliSymbols))
	require.Equal(t, "symbols", cliSymbols.Kind)
	require.GreaterOrEqual(t, cliSymbols.Total, 2)

	require.NoError(t, app.Definition([]string{"Run"}))
	require.Contains(t, out.String(), "Location         runner.go:5")
	out.Reset()

	require.NoError(t, app.References([]string{"Runner", "--limit", "2"}))
	require.Contains(t, out.String(), "References")
	require.Contains(t, out.String(), "runner.go:3:type Runner")
	out.Reset()

	require.NoError(t, app.Hover([]string{"Run", "--context", "1"}))
	require.Contains(t, out.String(), "Hover")
	require.Contains(t, out.String(), "func Run()")
	out.Reset()

	require.NoError(t, app.Teleport([]string{"Run"}))
	require.Contains(t, out.String(), "Teleport")
	require.Contains(t, out.String(), "Location         runner.go:5")
	require.Contains(t, out.String(), "func Run()")
	out.Reset()

	require.NoError(t, app.Teleport([]string{"Runner", "--json"}))
	require.Contains(t, out.String(), `"kind": "teleport"`)
	require.Contains(t, out.String(), `"mode": "symbol"`)
	require.Contains(t, out.String(), `"found": true`)
	out.Reset()

	require.NoError(t, app.Completion([]string{"Run", "--limit", "5"}))
	require.Contains(t, out.String(), "Completion")
	require.Contains(t, out.String(), "runner.go:5:function Run")
	out.Reset()

	require.NoError(t, app.Completion([]string{"bash"}))
	require.Contains(t, out.String(), "complete -F _codog_completion codog")
	require.Contains(t, out.String(), "bash zsh fish")
	out.Reset()

	completionPath := filepath.Join(t.TempDir(), "codog.zsh")
	require.NoError(t, app.Completion([]string{"zsh", "--output", completionPath}))
	require.Empty(t, out.String())
	completionData, err := os.ReadFile(completionPath)
	require.NoError(t, err)
	require.Contains(t, string(completionData), "#compdef codog")
	require.Contains(t, string(completionData), "_codog")

	require.NoError(t, app.Format([]string{"messy.go"}))
	require.Contains(t, out.String(), "Format")
	require.Contains(t, out.String(), "Changed          true")
	require.Contains(t, out.String(), "func messy()")
	data, err := os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func messy(){return}")
	out.Reset()

	require.NoError(t, app.Map([]string{"--depth", "1"}))
	require.Contains(t, out.String(), "Map")
	require.Contains(t, out.String(), "Files")
	require.Contains(t, out.String(), "Directories")
	require.Contains(t, out.String(), "Extensions")
	require.Contains(t, out.String(), "Top level")
	require.Contains(t, out.String(), "file\tgo.mod")
	out.Reset()

	require.NoError(t, app.Map([]string{"--depth", "2", "--limit", "2", "--json"}))
	var mapJSON mapReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &mapJSON))
	require.Equal(t, "map", mapJSON.Kind)
	require.Equal(t, 2, mapJSON.Total)
	require.Equal(t, 2, mapJSON.Limit)
	require.True(t, mapJSON.Truncated)
	require.Greater(t, mapJSON.FileCount+mapJSON.DirCount, 0)
	require.NotEmpty(t, mapJSON.TopLevel)
	out.Reset()

	require.NoError(t, app.Diagnostics(context.Background(), []string{"./..."}))
	require.Contains(t, out.String(), "Diagnostics")
	require.Contains(t, out.String(), "Total            0")
	out.Reset()

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"--output-format", "json",
			"diagnostics", "./...",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliDiagnostics diagnosticsReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliDiagnostics))
	require.Equal(t, "diagnostics", cliDiagnostics.Kind)
	require.Equal(t, 0, cliDiagnostics.Total)

	require.True(t, app.handleSlash(context.Background(), "/definition Runner", sess))
	require.Contains(t, out.String(), "Location         runner.go:3")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/references Run --limit=1", sess))
	require.Contains(t, out.String(), "References")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/symbols", sess))
	require.Contains(t, out.String(), "runner.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel symbols --json", sess))
	require.Contains(t, out.String(), `"kind": "symbols"`)
	require.Contains(t, out.String(), `"runner.go"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel definition Runner --json", sess))
	require.Contains(t, out.String(), `"kind": "definition"`)
	require.Contains(t, out.String(), `"found": true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel references Run --limit=1 --json", sess))
	require.Contains(t, out.String(), `"kind": "references"`)
	require.Contains(t, out.String(), `"total": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/teleport runner.go", sess))
	require.Contains(t, out.String(), "Mode             file")
	require.Contains(t, out.String(), "package intel")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/completion Run --limit=5", sess))
	require.Contains(t, out.String(), "Completion")
	require.Contains(t, out.String(), "Run")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/format messy.go --write", sess))
	require.Contains(t, out.String(), "Written          true")
	data, err = os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func messy()")
	require.Empty(t, errOut.String())
	out.Reset()

	notebook := `{
  "cells": [
    {"cell_type":"code","id":"cell-a","metadata":{},"source":["print(1)\n"],"outputs":[],"execution_count":null}
  ],
  "metadata": {"kernelspec":{"language":"python"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(notebook), 0o644))
	require.NoError(t, app.CodeIntel([]string{"notebook-read", "analysis.ipynb", "--include-outputs", "--json"}))
	require.Contains(t, out.String(), `"kind": "notebook_read"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	require.Contains(t, out.String(), `"language": "python"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel notebook-read analysis.ipynb --cell-index 0 --json", sess))
	require.Contains(t, out.String(), `"kind": "notebook_read"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/notebook-read analysis.ipynb --cell-index 0", sess))
	require.Contains(t, out.String(), "Notebook Read")
	require.Contains(t, out.String(), "Cell 0")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "--cell-id", "cell-a", "--source", "print(2)\n", "--json"}))
	require.Contains(t, out.String(), `"kind": "notebook_edit"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	require.Contains(t, out.String(), `"language": "python"`)
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "print(2)")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "--mode", "insert", "--cell-id", "cell-a", "--cell-type", "markdown", "--source", "# Notes"}))
	require.Contains(t, out.String(), "Notebook Edit")
	require.Contains(t, out.String(), "Index            1")
	require.Contains(t, out.String(), "Cell type        markdown")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/notebook-edit analysis.ipynb --mode insert --cell-id 1 --cell-type markdown --source=SlashNotes", sess))
	require.Contains(t, out.String(), "Notebook Edit")
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "SlashNotes")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-read", "analysis.ipynb", "--cell-index", "1", "--limit", "1"}))
	require.Contains(t, out.String(), "Notebook Read")
	require.Contains(t, out.String(), "Cell 1")
	require.Contains(t, out.String(), "# Notes")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "0", "markdown", "Legacy title"}))
	require.Contains(t, out.String(), "Notebook Edit")
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Legacy title")
	require.NotContains(t, string(data), `"outputs": []`)

	cliConfigPath := filepath.Join(t.TempDir(), "config.json")
	cliConfig, err := json.Marshal(map[string]any{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cliConfigPath, cliConfig, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	defer func() { require.NoError(t, os.Chdir(oldWD)) }()
	cliOut, cliErr := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", cliConfigPath, "--output-format", "json", "/notebook-read", "analysis.ipynb", "--cell-index", "0"}, config.FlagOverrides{})
	})
	require.NoError(t, cliErr)
	require.Contains(t, cliOut, `"kind": "notebook_read"`)
	require.Contains(t, cliOut, `"Legacy title"`)

	_, err = parseCodeIntelNotebookEditArgs([]string{"analysis.ipynb", "--mode", "insert"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "new_source is required")

	_, err = parseCodeIntelNotebookEditArgs([]string{"analysis.ipynb", "--mode", "replac", "--source", "title"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown notebook edit mode "replac"`)
	require.Contains(t, err.Error(), `did you mean "replace"`)

	_, err = parseCodeIntelNotebookReadArgs([]string{"analysis.ipynb", "--limit", "0"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive integer")
}
