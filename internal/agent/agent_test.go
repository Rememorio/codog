package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/stretchr/testify/require"
)

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
	require.Contains(t, out.String(), `"type": "permission"`)
	require.Contains(t, out.String(), `"allowed": false`)
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
	require.Contains(t, out.String(), `"signature_valid": true`)
	require.Contains(t, out.String(), `"max_permission_mode": "read-only"`)
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
}

func TestSessionsCommandForkExistsAndDelete(t *testing.T) {
	configHome := t.TempDir()
	store := session.NewStore(configHome)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"exists", "source"}))
	require.Contains(t, out.String(), `"exists": true`)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"fork", "source", "branch"}))
	require.Contains(t, out.String(), `"ID":`)
	require.Contains(t, out.String(), "hello session")
	var forked session.Session
	require.NoError(t, json.Unmarshal(out.Bytes(), &forked))
	require.NotEmpty(t, forked.ID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"delete", forked.ID}))
	require.Contains(t, out.String(), `"deleted": true`)
}

func TestSessionSlashSwitchAndFork(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello slash")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("source")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session fork branch", sess))
	require.NotEqual(t, "source", sess.ID)
	require.Contains(t, errOut.String(), "session forked:")
	forkedID := sess.ID
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session switch source", sess))
	require.Equal(t, "source", sess.ID)
	require.Contains(t, errOut.String(), "session switched: source")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session delete "+forkedID, sess))
	require.Contains(t, errOut.String(), "session deleted: "+forkedID)
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

	require.True(t, app.handleSlash(context.Background(), "/clear", sess))
	require.NotEqual(t, "source", sess.ID)
	require.Empty(t, sess.Messages)
	require.Contains(t, errOut.String(), "session cleared:")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/resume source", sess))
	require.Equal(t, "source", sess.ID)
	require.Len(t, sess.Messages, 1)
	require.Contains(t, errOut.String(), "session resumed: source")
}

func TestRuntimeInfoSlashCommands(t *testing.T) {
	var out bytes.Buffer
	app := &App{Workspace: t.TempDir(), Out: &out, Err: io.Discard}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/version", sess))
	require.Contains(t, out.String(), "Codog")
	require.Contains(t, out.String(), "Version")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox", sess))
	require.Contains(t, out.String(), `"os":`)
}

func TestGitCommandStatusDiffAndCommit(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Git([]string{"status"}))
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	require.NoError(t, app.Git([]string{"commit", "--all", "add", "notes"}))
	require.Contains(t, out.String(), `"commit":`)
	require.Contains(t, out.String(), "add notes")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\nagain\n"), 0o644))
	require.NoError(t, app.Git([]string{"diff"}))
	require.Contains(t, out.String(), "+again")
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

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\nchanged\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/diff", sess))
	require.Contains(t, out.String(), "+changed")
}

func TestRuntimeConfigModelAndPermissionsSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
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

	require.True(t, app.handleSlash(context.Background(), "/permissions", sess))
	require.Contains(t, out.String(), `"permission_mode": "workspace-write"`)
	require.Contains(t, out.String(), `"bash:rm"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions read-only", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, errOut.String(), "permission_mode=read-only")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions invalid", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, errOut.String(), "unknown permission mode: invalid")
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
	out.Reset()

	require.NoError(t, app.Doctor([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "doctor"`)
	require.Contains(t, out.String(), `"name": "Auth"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/doctor", sess))
	require.Contains(t, out.String(), "Doctor")
	require.NotContains(t, errOut.String(), "unknown slash command")
}

func TestStatusCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Status memory."), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "status me")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "claude-test",
			BaseURL:             "https://api.example.test",
			APIKey:              "secret",
			PermissionMode:      "workspace-write",
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
	require.Contains(t, out.String(), "Tools            6")
	out.Reset()

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{Resume: "source"}))
	require.Contains(t, out.String(), `"kind": "status"`)
	require.Contains(t, out.String(), `"memory_file_count": 1`)
	require.Contains(t, out.String(), `"id": "source"`)
	require.Contains(t, out.String(), `"message_count": 1`)
	out.Reset()

	sess := &session.Session{ID: "source", Messages: []anthropic.Message{anthropic.TextMessage("user", "slash")}}
	require.True(t, app.handleSlash(context.Background(), "/status", sess))
	require.Contains(t, out.String(), "Session          source (1 messages)")
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

	require.NoError(t, app.History([]string{"--session=source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "prompt_history"`)
	require.Contains(t, out.String(), `"total": 2`)
	require.Contains(t, out.String(), `"text": "first prompt"`)
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

func TestMemoryCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Memory first line\nsecret body"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Memory(nil))
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "Instruction files 1")
	require.Contains(t, out.String(), "preview=Memory first line")
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "memory"`)
	require.Contains(t, out.String(), `"instruction_files": 1`)
	require.Contains(t, out.String(), `"preview": "Memory first line"`)
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory")
}

func TestInitCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Init(nil))
	require.Contains(t, out.String(), "Init")
	require.Contains(t, out.String(), ".codog/instructions.md")
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	out.Reset()

	require.NoError(t, app.Init([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "init"`)
	require.Contains(t, out.String(), `"already_initialized": true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Init")
}

func TestStateCommandAndREPLWritesWorkerState(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		In:        strings.NewReader("/exit\n"),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.REPL(context.Background(), config.FlagOverrides{SessionID: "session-1"}))
	require.FileExists(t, workerstate.Path(workspace))
	loaded, err := workerstate.Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "repl", loaded.Mode)
	require.Equal(t, "idle", loaded.Status)
	require.Equal(t, "session-1", loaded.SessionID)

	require.NoError(t, app.State(nil))
	require.Contains(t, out.String(), "State")
	require.Contains(t, out.String(), "Worker")
	out.Reset()

	require.NoError(t, app.State([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "worker_state"`)
	require.Contains(t, out.String(), `"mode": "repl"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/state", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), "State")
}

func TestPromptWritesCompletedWorkerState(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
			EnabledSkills:       nil,
			Hooks:               config.HookConfig{},
			Future:              config.FutureConfig{},
			AuthToken:           "",
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "hello", config.FlagOverrides{SessionID: "prompt-session"}))
	loaded, err := workerstate.Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "prompt", loaded.Mode)
	require.Equal(t, "completed", loaded.Status)
	require.Equal(t, "prompt-session", loaded.SessionID)
	require.Contains(t, out.String(), "done")
	history, err := app.Sessions.PromptHistory("prompt-session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "hello", history[0].Text)
}

func TestSystemPromptIncludesProjectMemory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Always run focused tests."), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, "<project_memory>")
	require.Contains(t, prompt, "AGENTS.md")
	require.Contains(t, prompt, "Always run focused tests.")
}

func TestExportCommandWritesFormats(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "export me")))
	var out bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Export([]string{"--session", "source"}))
	require.Contains(t, out.String(), "# Conversation Export")
	require.Contains(t, out.String(), "export me")
	out.Reset()

	output := filepath.Join(workspace, "transcript.json")
	require.NoError(t, app.Export([]string{"--session=source", "--format=json", "--output", output}))
	require.Contains(t, out.String(), `"format": "json"`)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
}

func TestExportSlashWritesCurrentSession(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "slash export")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.True(t, app.handleSlash(context.Background(), "/export notes.md", sess))
	require.Contains(t, errOut.String(), "exported session source")
	data, err := os.ReadFile(filepath.Join(workspace, "notes.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "slash export")
}

func TestBuildAgentCommandQuotesPrompt(t *testing.T) {
	command := buildAgentCommand("/tmp/codog", agentdefs.Definition{
		Name:   "reviewer",
		Model:  "mock-model",
		Prompt: "review carefully",
	}, "check '$HOME'")

	require.Contains(t, command, "'/tmp/codog'")
	require.Contains(t, command, "--model 'mock-model'")
	require.Contains(t, command, "prompt 'review carefully")
	require.Contains(t, command, "'\"'\"'$HOME'\"'\"'")
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	return workspace
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}

func TestParseAgentRunArgs(t *testing.T) {
	req, err := parseAgentRunArgs([]string{"--worktree", "reviewer", "check", "this"})
	require.NoError(t, err)
	require.True(t, req.Worktree)
	require.Equal(t, "reviewer", req.Name)
	require.Equal(t, "check this", req.Prompt)

	_, err = parseAgentRunArgs([]string{"--worktree", "reviewer"})
	require.Error(t, err)
}

func TestBackgroundWatchCommandOutputsJSONLEvents(t *testing.T) {
	configHome := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.Run("echo cli-watch", t.TempDir())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "cli-watch")
	}, 2*time.Second, 50*time.Millisecond)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Background([]string{"watch", task.ID}))
	require.Contains(t, out.String(), `"type":"status"`)
	require.Contains(t, out.String(), `"type":"log"`)
	require.Contains(t, out.String(), "cli-watch")
}

func TestBackgroundRunAttachesSessionFromOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"run", "echo", "attached"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"list"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
}

func TestParseBackgroundRunArgsWithRestartPolicy(t *testing.T) {
	command, options, err := parseBackgroundRunArgs([]string{"--restart=always", "--restart-limit", "2", "--restart-delay", "5", "echo", "restart"})
	require.NoError(t, err)
	require.Equal(t, "echo restart", command)
	require.NotNil(t, options.RestartPolicy)
	require.True(t, options.RestartPolicy.Enabled)
	require.Equal(t, "always", options.RestartPolicy.Mode)
	require.Equal(t, 2, options.RestartPolicy.MaxAttempts)
	require.Equal(t, 5, options.RestartPolicy.DelaySeconds)

	_, _, err = parseBackgroundRunArgs([]string{"--restart=never", "echo"})
	require.Error(t, err)
}

func TestCodeIntelLSPCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.CodeIntel([]string{"lsp", "discover"}))
	require.Contains(t, out.String(), `"language": "go"`)
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "start", "go", "sh", "-c", "sleep 30"}))
	require.Contains(t, out.String(), `"language": "go"`)
	require.Contains(t, out.String(), `"status": "running"`)
	t.Cleanup(func() { _ = app.CodeIntel([]string{"lsp", "stop", "go"}) })
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "list"}))
	require.Contains(t, out.String(), `"language": "go"`)
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "stop", "go"}))
	require.Contains(t, out.String(), `"status": "stopped"`)
}

func TestOAuthTokenCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, app.OAuth([]string{"token", "save", "access-token-1234", "refresh-token-1234", expiresAt}))
	require.Contains(t, out.String(), `"access_token": "acce...1234"`)
	require.NotContains(t, out.String(), "access-token-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "show"}))
	require.Contains(t, out.String(), `"expired": false`)

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "delete"}))
	require.Contains(t, out.String(), `"deleted":true`)
}

func TestOAuthDiscoverCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.OAuth([]string{"discover", server.URL}))
	require.Contains(t, out.String(), `"authorization_endpoint": "https://auth.example/authorize"`)
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
}

func TestOAuthDeviceCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"device_authorization_endpoint":"` + server.URL + `/device","token_endpoint":"` + server.URL + `/token"}`))
		case "/device":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			require.Equal(t, "profile", r.Form.Get("scope"))
			_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"` + server.URL + `/verify","expires_in":600,"interval":1}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, oauth.DeviceCodeGrantType, r.Form.Get("grant_type"))
			require.Equal(t, "device-1", r.Form.Get("device_code"))
			_, _ = w.Write([]byte(`{"access_token":"device-access-1234","refresh_token":"device-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)
	require.NoError(t, app.OAuth([]string{"device", "start", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "start", "default"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", server.URL, "client-1", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", "default", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "device-access-1234", loaded.AccessToken)
}

func TestOAuthProviderCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: t.TempDir()},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"provider", "save", "default", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"name": "default"`)
	require.Contains(t, out.String(), `"client_id": "client-1"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "list"}))
	require.Contains(t, out.String(), `"name": "default"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "show", "default"}))
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "delete", "default"}))
	require.Contains(t, out.String(), `"deleted": true`)
}

func TestOAuthBrowserCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			require.Equal(t, "code-1", r.Form.Get("code"))
			require.Equal(t, "verifier-1", r.Form.Get("code_verifier"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"browser-access-1234","refresh_token":"browser-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"browser", "start", "default", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"authorization_url":`)
	require.Contains(t, out.String(), "client_id=client-1")
	require.Contains(t, out.String(), "scope=profile")
	require.Contains(t, out.String(), `"code_verifier":`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"browser", "exchange", "default", "code-1", "verifier-1", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"access_token": "brow...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "browser-access-1234", loaded.AccessToken)
}

func TestOAuthTokenRefreshCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "refresh"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", loaded.AccessToken)
}

func TestOAuthStatusCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "status-access-1234",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"status"}))
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.Contains(t, out.String(), `"access_token": "stat...1234"`)
	require.Contains(t, out.String(), `"can_refresh": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.NotContains(t, out.String(), "status-access-1234")
}

func TestOAuthTokenRevokeAndLogoutCommands(t *testing.T) {
	server, revoked := oauthRevocationTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-1", RefreshToken: "refresh-1"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "revoke", "default", "refresh"}))
	require.Contains(t, out.String(), `"revoked": true`)
	require.Contains(t, out.String(), `"token": "refresh"`)
	require.Contains(t, *revoked, "refresh_token:refresh-1")
	out.Reset()

	require.NoError(t, app.OAuth([]string{"logout"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.Contains(t, out.String(), `"access_revoked": true`)
	require.Contains(t, *revoked, "access_token:access-1")
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
}

func TestApplyStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken: "stored-token",
		ExpiresAt:   now.Add(time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "stored-token", cfg.AuthToken)

	cfg = config.Config{ConfigHome: configHome, AuthToken: "explicit-token"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "explicit-token", cfg.AuthToken)
}

func TestApplyStoredOAuthTokenRefreshesExpiredToken(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "refreshed-access", cfg.AuthToken)
}

func oauthRefreshTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"refreshed-access","refresh_token":"refresh-2","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func oauthRevocationTestServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	revoked := []string{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"revocation_endpoint":"` + server.URL + `/revoke","token_endpoint":"` + server.URL + `/token"}`))
		case "/revoke":
			require.NoError(t, r.ParseForm())
			revoked = append(revoked, r.Form.Get("token_type_hint")+":"+r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return server, &revoked
}

func TestMarketplaceDisableSkipsPluginToolRegistration(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat"}]}`), 0o644))

	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"disable", "demo"}))
	require.Contains(t, out.String(), `"enabled": false`)

	require.NoError(t, app.RegisterPluginTools())
	require.False(t, app.Tools.Has("demo_tool"))
}

func TestMarketplaceInstallRemoteCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.1.0"}`,
		"demo/tool.sh":     "echo ok\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"install-remote", "demo"}))
	require.Contains(t, out.String(), `"checksum_valid": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))
}

func TestMarketplaceUpdateCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("echo old\n"), 0o755))
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.2.0"}`,
		"demo/tool.sh":     "echo new\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", Version: "0.2.0", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
	}

	var out bytes.Buffer
	app.Out = &out
	require.NoError(t, app.Marketplace([]string{"updates"}))
	require.Contains(t, out.String(), `"latest_version": "0.2.0"`)

	out.Reset()
	require.NoError(t, app.Marketplace([]string{"update", "demo"}))
	require.Contains(t, out.String(), `"updated": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	data, err := os.ReadFile(filepath.Join(dir, "tool.sh"))
	require.NoError(t, err)
	require.Equal(t, "echo new\n", string(data))
}

func TestUpdaterInstallAndRollbackCommands(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "codog-new")
	target := filepath.Join(dir, "codog")
	require.NoError(t, os.WriteFile(artifact, []byte("new"), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"install", artifact, target}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))

	out.Reset()
	require.NoError(t, app.Updater(context.Background(), []string{"rollback", target}))
	require.Contains(t, out.String(), `"rolled_back": true`)
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
}

func TestUpdaterVerifyCommand(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		manifest := updater.Manifest{Version: "0.2.0"}
		data, err := json.Marshal(manifest)
		require.NoError(t, err)
		manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
		require.NoError(t, json.NewEncoder(w).Encode(manifest))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"verify", server.URL, base64.StdEncoding.EncodeToString(publicKey)}))
	require.Contains(t, out.String(), `"signature_valid": true`)
}

func makeAgentPluginZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		file, err := writer.CreateHeader(header)
		require.NoError(t, err)
		_, err = file.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}
