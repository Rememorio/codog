package agent

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/todos"
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

func TestACPStatusCommandOutputsTextJSONAndUnsupported(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, renderACPStatus(&out, nil))
	require.Contains(t, out.String(), "ACP / Zed")
	require.Contains(t, out.String(), "Supported        true")
	require.Contains(t, out.String(), "stdio JSON-RPC")
	out.Reset()

	require.NoError(t, renderACPStatus(&out, []string{"serve", "--output-format", "json"}))
	var report acpStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "1.0", report.SchemaVersion)
	require.Equal(t, "acp", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.Supported)
	require.NotNil(t, report.LaunchCommand)
	require.Equal(t, "ACP/Zed", report.Protocol.Name)
	require.True(t, report.Protocol.JSONRPC)
	require.False(t, report.Protocol.Daemon)
	require.NotNil(t, report.Protocol.Endpoint)
	require.True(t, report.Protocol.ServeStartsDaemon)
	require.Equal(t, "unsupported_acp_invocation", report.Contracts.UnsupportedInvocationKind)
	require.Contains(t, report.Contracts.BlockingGates, "prompt")
	require.Contains(t, report.Aliases, "--acp")
	out.Reset()

	err := renderACPStatus(&out, []string{"start", "--json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported_acp_invocation")
	var unsupported acpUnsupportedReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "unsupported_acp_invocation", unsupported.Kind)
	require.Equal(t, "error", unsupported.Status)
	require.False(t, unsupported.Supported)
	require.Equal(t, []string{"start", "--json"}, unsupported.Invocation)
}

func TestParseACPGlobalInvocationSupportsOutputFormatBeforeCommand(t *testing.T) {
	args, ok, err := parseACPGlobalInvocation([]string{"--output-format", "json", "acp", "serve"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--output-format", "json", "serve"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--json", "acp"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--json"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--output-format=json", "acp"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--output-format=json"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--json", "prompt", "hello"})
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, args)
}

func TestParseFlagsSupportsPermissionSkipAliases(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--dangerously-skip-permissions", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.SkipPermissions)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--skip-permissions", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.SkipPermissions)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)
}

func TestParseFlagsSupportsPrintAliases(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, config.FlagOverrides{}, overrides)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--print", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
	require.False(t, overrides.SkipPermissions)
}

func TestParseFlagsSupportsToolRuleOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--allowed-tools", "read_file,grep",
		"--allowedTools", "glob",
		"--disallowed-tools", "bash",
		"--disallowedTools", "write_file,edit_file",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"read_file", "grep", "glob"}, overrides.AllowedTools)
	require.Equal(t, []string{"bash", "write_file", "edit_file"}, overrides.DisallowedTools)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestParseFlagsSupportsSystemPromptOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--system-prompt", "base",
		"--append-system-prompt", "extra",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "base", overrides.SystemPrompt)
	require.Equal(t, "extra", overrides.AppendPrompt)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestDumpManifestsCommand(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "helper.json"), []byte(`{"prompt":"help"}`), 0o644))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.DumpManifests([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "dump-manifests"`)
	require.Contains(t, out.String(), `"source": "go-resolver"`)
	require.Contains(t, out.String(), `"name": "review"`)
	require.Contains(t, out.String(), `"name": "/ant-trace"`)
	require.Contains(t, out.String(), `"implemented": false`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.Contains(t, out.String(), `"hidden": true`)
	out.Reset()

	require.NoError(t, app.DumpManifests(nil))
	require.Contains(t, out.String(), "Manifest Dump")
	out.Reset()

	otherWorkspace := t.TempDir()
	require.NoError(t, app.DumpManifests([]string{"--manifests-dir", otherWorkspace, "--json"}))
	require.Contains(t, out.String(), otherWorkspace)

	err := app.DumpManifests([]string{"--manifests-dir", filepath.Join(t.TempDir(), "missing")})
	require.ErrorContains(t, err, "missing_manifests")
}

func TestSystemPromptCommand(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			SystemPrompt:       "Custom base.",
			AppendSystemPrompt: "Extra instructions.",
		},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.SystemPromptCommand([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "system-prompt"`)
	require.Contains(t, out.String(), "Custom base.")
	out.Reset()

	require.NoError(t, app.SystemPromptCommand(nil))
	require.Contains(t, out.String(), "Custom base.")
	require.Contains(t, out.String(), "Extra instructions.")
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

func TestBackfillSessionsCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("user", "legacy prompt")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.BackfillSessions([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "backfill_sessions"`)
	require.Contains(t, out.String(), `"sessions_updated": 1`)
	require.Contains(t, out.String(), `"inputs_added": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/backfill-sessions", &session.Session{ID: "legacy"}))
	require.Contains(t, out.String(), "Backfill Sessions")
	require.Contains(t, out.String(), "Sessions scanned 1")
	require.Empty(t, errOut.String())
}

func TestRewindCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "first answer")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "second prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "second answer")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.Rewind([]string{"2", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "rewind"`)
	require.Contains(t, out.String(), `"removed_messages": 2`)
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 2)
	out.Reset()

	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "third prompt")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 3)

	require.True(t, app.handleSlash(context.Background(), "/rewind 1", sess))
	require.Len(t, sess.Messages, 2)
	require.Contains(t, out.String(), "Removed          1")
	require.Empty(t, errOut.String())
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

func TestRenameSessionCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "rename me")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.Rename([]string{"cli-renamed", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"old_id": "source"`)
	require.Contains(t, out.String(), `"new_id": "cli-renamed"`)
	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.False(t, ok)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"rename", "cli-renamed", "sessions-renamed"}))
	require.Contains(t, out.String(), `"new_id": "sessions-renamed"`)
	out.Reset()

	sess, err := store.Open("sessions-renamed")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/rename slash-renamed", sess))
	require.Equal(t, "slash-renamed", sess.ID)
	require.Contains(t, errOut.String(), "session renamed: sessions-renamed -> slash-renamed")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session rename final-renamed", sess))
	require.Equal(t, "final-renamed", sess.ID)
	require.Contains(t, errOut.String(), "session renamed: slash-renamed -> final-renamed")
	opened, err := store.Open("final-renamed")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "rename me", opened.Messages[0].Content[0].Text)
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

	require.True(t, app.handleSlash(context.Background(), "/acp --json", sess))
	require.Contains(t, out.String(), `"kind": "acp"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox", sess))
	require.Contains(t, out.String(), `"os":`)
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
	require.Equal(t, "detect", app.Config.Future.SandboxStrategy)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"sandbox_strategy": "detect"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox-toggle off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Sandbox Toggle")
	require.Contains(t, out.String(), "Configured       off")
	require.Equal(t, "off", app.Config.Future.SandboxStrategy)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.SandboxToggle([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"configured_strategy": ""`)
	require.Equal(t, "", app.Config.Future.SandboxStrategy)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
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

	require.NoError(t, app.Git([]string{"commit", "--all", "add", "notes"}))
	require.Contains(t, out.String(), `"commit":`)
	require.Contains(t, out.String(), "add notes")
	out.Reset()

	require.NoError(t, app.Git([]string{"log", "1"}))
	require.Contains(t, out.String(), "add notes")
	out.Reset()

	require.NoError(t, app.Changelog([]string{"1"}))
	require.Contains(t, out.String(), "add notes")
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	runGit(t, workspace, "tag", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: add feature")
	require.NoError(t, app.ReleaseNotes([]string{"--from", "v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "release_notes"`)
	require.Contains(t, out.String(), `"name": "Features"`)
	require.Contains(t, out.String(), `"subject": "feat: add feature"`)
	out.Reset()

	require.NoError(t, app.Git([]string{"blame", "notes.txt", "1"}))
	require.Contains(t, out.String(), "hello")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\nagain\n"), 0o644))
	require.NoError(t, app.Git([]string{"diff"}))
	require.Contains(t, out.String(), "+again")
	out.Reset()

	require.NoError(t, app.Stash([]string{"push", "agent stash"}))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.NoError(t, app.Git([]string{"stash", "list"}))
	require.Contains(t, out.String(), "agent stash")
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
		return RunCLI(context.Background(), []string{"--config", configPath, "blame", "notes.txt", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "hello cli")

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello cli\nagain\n"), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "+again")
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

	require.True(t, app.handleSlash(context.Background(), "/log 1", sess))
	require.Contains(t, out.String(), "slash commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/changelog 1", sess))
	require.Contains(t, out.String(), "slash commit")
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

	require.True(t, app.handleSlash(context.Background(), "/git status", sess))
	require.Contains(t, out.String(), "## ")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\nchanged\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/diff", sess))
	require.Contains(t, out.String(), "+changed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash push slash stash", sess))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash list", sess))
	require.Contains(t, out.String(), "slash stash")
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

func TestTagCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello tag\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add tag notes")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Tag([]string{"create", "v0.1.0", "--message", "release v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "tag"`)
	require.Contains(t, out.String(), `"name": "v0.1.0"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag list v0.*", sess))
	require.Contains(t, out.String(), "v0.1.0")
	out.Reset()

	require.NoError(t, app.Git([]string{"tag", "show", "v0.1.0"}))
	require.Contains(t, out.String(), "release v0.1.0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag delete v0.1.0", sess))
	require.Contains(t, out.String(), "Deleted tag")
	require.Empty(t, errOut.String())
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
	require.Contains(t, errOut.String(), "permission_mode=read-only")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions invalid", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
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
	require.Contains(t, out.String(), "permission_mode=workspace-write")
	out.Reset()

	require.NoError(t, app.AllowedTools([]string{"add", "grep"}))
	require.Contains(t, app.Config.PermissionRules.Allow, "grep")
	require.Contains(t, out.String(), "Allowed tools")
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
	require.Contains(t, candidates, "/session switch active-session")
	require.Contains(t, candidates, "/resume source")
	require.Contains(t, candidates, "/session switch source")
	require.Contains(t, candidates, "/permissions workspace-write")
	require.Contains(t, candidates, "/team/review ")
	require.Contains(t, candidates, "/team/audit ")
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
		MaxTokens:      100,
		MaxTurns:       3,
		PermissionMode: "workspace-write",
	})
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"get", "auth"}))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"paths"}))
	require.Contains(t, out.String(), "project.json")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"model"}))
	require.Contains(t, out.String(), `"model": "model-a"`)
}

func TestRenderConfigInspectionMutatesConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "model", "model-b"}))
	require.Contains(t, out.String(), `"action": "set"`)
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "rate_limit.max_retries", "4"}))
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"unset", "model"}))
	require.Contains(t, out.String(), `"action": "unset"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"model"`)
	require.Contains(t, string(data), `"max_retries": 4`)
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

	require.True(t, app.handleSlash(context.Background(), "/exit-plan", sess))
	require.Contains(t, out.String(), "Status           inactive")
	require.Empty(t, errOut.String())
	require.Equal(t, "workspace-write", app.effectiveConfig().PermissionMode)
	require.NotContains(t, app.systemPrompt(), "<codog_plan_mode")
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
	require.Contains(t, out.String(), "Tools            72")
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

func TestFilesCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".hidden", "secret.go"), []byte("package hidden\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Files([]string{"--glob", "*.go", "--json"}))
	require.Contains(t, out.String(), `"kind": "files"`)
	require.Contains(t, out.String(), `"path": "pkg/main.go"`)
	require.NotContains(t, out.String(), "secret.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/files --glob=*.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Files")
	require.Contains(t, out.String(), "README.md")
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
	require.Contains(t, out.String(), "file\tgo.mod")
	out.Reset()

	require.NoError(t, app.Diagnostics(context.Background(), []string{"./..."}))
	require.Contains(t, out.String(), "Diagnostics")
	require.Contains(t, out.String(), "Total            0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/definition Runner", sess))
	require.Contains(t, out.String(), "Location         runner.go:3")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/references Run --limit=1", sess))
	require.Contains(t, out.String(), "References")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/symbols", sess))
	require.Contains(t, out.String(), "runner.go")
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

	require.NoError(t, app.Memory([]string{"show", "AGENTS.md"}))
	require.Contains(t, out.String(), "Memory File")
	require.Contains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"add", "Use", "focused", "tests."}))
	require.Contains(t, out.String(), "Memory Updated")
	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Use focused tests.")
	out.Reset()

	require.NoError(t, app.Memory([]string{"path", "AGENTS.md", "--json"}))
	require.Contains(t, out.String(), `"action": "path"`)
	require.Contains(t, out.String(), "AGENTS.md")
	out.Reset()

	require.NoError(t, app.Memory([]string{"ensure", ".codog/instructions.md"}))
	require.Contains(t, out.String(), "Memory File")
	_, err = os.Stat(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	out.Reset()

	require.NoError(t, app.Memory([]string{"edit", ".codog/instructions.md", "--no-open", "--json"}))
	require.Contains(t, out.String(), `"action": "edit"`)
	require.Contains(t, out.String(), `"Editor launch skipped."`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory show AGENTS.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory edit --no-open", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory File")
}

func TestFocusCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("focus body\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Focus([]string{"notes.md"}))
	require.Contains(t, out.String(), "Focus")
	require.Contains(t, out.String(), "notes.md")
	require.FileExists(t, focus.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<focused_context>")
	require.Contains(t, app.systemPrompt(), "focus body")
	out.Reset()

	require.NoError(t, app.Focus([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "focus"`)
	require.Contains(t, out.String(), `"path": "notes.md"`)
	out.Reset()

	require.NoError(t, app.Focus([]string{"pkg"}))
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus notes.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Focus")
	require.NotContains(t, app.systemPrompt(), "focus body")
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus --all", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Entries          0")
	require.NotContains(t, app.systemPrompt(), "<focused_context>")
	require.Empty(t, errOut.String())
}

func TestAddDirCommandAndSlashUpdatesToolScope(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	extraFile := filepath.Join(extra, "notes.txt")
	require.NoError(t, os.WriteFile(extraFile, []byte("extra body\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AddDir([]string{extra, "--json"}))
	require.Contains(t, out.String(), `"kind": "additional_dirs"`)
	require.Contains(t, out.String(), extra)
	require.FileExists(t, pathscope.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<additional_directories>")
	out.Reset()

	input, _ := json.Marshal(map[string]string{"path": extraFile})
	toolOut, err := app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.NoError(t, err)
	require.Contains(t, toolOut, "extra body")

	require.True(t, app.handleSlash(context.Background(), "/add-dir remove "+extra, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Additional Directories")
	require.NotContains(t, app.systemPrompt(), "<additional_directories>")
	_, err = app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
	require.Empty(t, errOut.String())
}

func TestContextCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use focused tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("context body\n"), 0o644))
	_, err := focus.Add(workspace, []string{"notes.md"})
	require.NoError(t, err)
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("user", "hello")))
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("assistant", "done")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			MaxTokens:      4096,
			MaxTurns:       8,
			APIKey:         "test-key",
		},
		Sessions:  store,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Context([]string{"--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "context"`)
	require.Contains(t, out.String(), `"focused_paths": 1`)
	require.Contains(t, out.String(), `"message_count": 2`)
	require.Contains(t, out.String(), `"total_tokens":`)
	out.Reset()

	sess, err := store.Open("context-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/context", sess))
	require.Contains(t, out.String(), "Context")
	require.Contains(t, out.String(), "Session          context-session (2 messages)")
	require.Contains(t, out.String(), "Focused paths    1")
	require.Empty(t, errOut.String())
	out.Reset()

	vizPath := filepath.Join(workspace, "context.html")
	require.NoError(t, app.ContextViz([]string{"--output", vizPath, "--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "ctx_viz"`)
	require.Contains(t, out.String(), `"bytes":`)
	data, err := os.ReadFile(vizPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "Codog Context")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ctx_viz --output "+filepath.Join(workspace, "slash-context.html"), sess))
	require.Contains(t, out.String(), "Context Viz")
	require.FileExists(t, filepath.Join(workspace, "slash-context.html"))
	require.Empty(t, errOut.String())
}

func TestUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("usage-session", anthropic.TextMessage("user", "hello usage")))
	providerUsage := anthropic.Usage{InputTokens: 50, OutputTokens: 11, CacheReadInputTokens: 4}
	require.NoError(t, store.AppendWithUsage("usage-session", anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		}},
	}, &providerUsage))
	require.NoError(t, store.Append("usage-session", anthropic.ToolResultMessage("tool-1", "ok", false)))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-haiku"},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Usage([]string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	require.Contains(t, out.String(), `"kind": "usage"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"tool_results": 1`)
	require.Contains(t, out.String(), `"source": "actual"`)
	require.Contains(t, out.String(), `"input_tokens": 50`)
	require.Contains(t, out.String(), `"cache_read_input_tokens": 4`)
	out.Reset()

	require.NoError(t, app.Summary([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "summary"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"first_user":`)
	require.Contains(t, out.String(), `"hello usage"`)
	out.Reset()

	sess, err := store.Open("usage-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/usage", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Contains(t, out.String(), "Token source     actual")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stats", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	out.Reset()

	require.NoError(t, app.Insights([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "insights"`)
	require.Contains(t, out.String(), `"sessions": 1`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"name": "read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/insights --limit 1", sess))
	require.Contains(t, out.String(), "Insights")
	require.Contains(t, out.String(), "Recent prompts")
	out.Reset()

	thinkBackPath := filepath.Join(workspace, "think-back.html")
	require.NoError(t, app.ThinkBack([]string{"--year", "2026", "--output", thinkBackPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "think_back"`)
	require.Contains(t, out.String(), `"year": 2026`)
	_, err = os.Stat(thinkBackPath)
	require.NoError(t, err)
	out.Reset()

	slashThinkBackPath := filepath.Join(workspace, "slash-think-back.html")
	require.True(t, app.handleSlash(context.Background(), "/think-back --year 2026 --output "+slashThinkBackPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(slashThinkBackPath)
	require.NoError(t, err)
	out.Reset()

	playPath := filepath.Join(workspace, "thinkback-play.html")
	require.True(t, app.handleSlash(context.Background(), "/thinkback-play --year 2026 --output "+playPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(playPath)
	require.NoError(t, err)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/summary", sess))
	require.Contains(t, out.String(), "Summary")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Empty(t, errOut.String())
}

func TestCompactCommandPersistsCompactedSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	compactPath := filepath.Join(workspace, "compact-hook.json")
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "four")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			AutoCompactMessages: 2,
			Hooks: config.HookConfig{
				PreCompactCommands: []config.HookCommand{{Command: "cat > " + shellQuote(compactPath)}},
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Compact([]string{"--session", "compact-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"original_messages": 4`)
	require.Contains(t, out.String(), `"remaining_messages": 3`)
	opened, err := store.Open("compact-session")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 3)
	require.Contains(t, opened.Messages[0].Content[0].Text, "auto-compacted")
	require.Equal(t, "three", opened.Messages[1].Content[0].Text)
	require.Equal(t, "four", opened.Messages[2].Content[0].Text)
	hookPayload, err := os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(hookPayload, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"session_id":"compact-session"`)
}

func TestRateLimitOptionsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			RateLimit: config.RateLimitConfig{
				MaxRetries:       4,
				InitialBackoffMS: 250,
				MaxBackoffMS:     2000,
			},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.RateLimitOptions([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "rate_limit_options"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	require.Contains(t, out.String(), `"initial_backoff_ms": 250`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/rate-limit-options", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Rate Limit Options")
	require.Contains(t, out.String(), "Max retries      4")
	require.Contains(t, out.String(), "429,500,502,503,504")
	require.Empty(t, errOut.String())
}

func TestResetLimitsCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"test","rate_limit":{"max_retries":4,"initial_backoff_ms":250}}`), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			RateLimit:  config.RateLimitConfig{MaxRetries: 4, InitialBackoffMS: 250},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.ResetLimits([]string{"--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "reset_limits"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "rate_limit")
	out.Reset()

	require.NoError(t, os.WriteFile(configPath, []byte(`{"rate_limit":{"max_retries":3}}`), 0o644))
	app.Config.RateLimit = config.RateLimitConfig{MaxRetries: 3}
	require.True(t, app.handleSlash(context.Background(), "/reset-limits --path "+configPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Reset Limits")
	require.Contains(t, out.String(), "Previous retries 3")
	require.Empty(t, errOut.String())
}

func TestOutputStyleCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "output-styles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "output-styles", "brief.md"), []byte("Answer in one compact paragraph.\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.OutputStyle(nil))
	require.Contains(t, out.String(), "Output Style")
	require.Contains(t, out.String(), "brief")
	require.Contains(t, out.String(), "concise")
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"set", "brief", "--json"}))
	require.Contains(t, out.String(), `"active": "brief"`)
	require.FileExists(t, outputstyle.StatePath(workspace))
	require.Contains(t, app.systemPrompt(), `<output_style name="brief" source="user">`)
	require.Contains(t, app.systemPrompt(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style show brief", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Body")
	require.Contains(t, out.String(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Output Style")
	require.NotContains(t, app.systemPrompt(), "<output_style")
	require.Empty(t, errOut.String())
}

func TestThemeVimAndPrivacyCommandsPersistPreferences(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Theme([]string{"dark", "--json"}))
	require.Contains(t, out.String(), `"kind": "theme"`)
	require.Contains(t, out.String(), `"theme": "dark"`)
	require.Equal(t, "dark", app.Config.Theme)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "dark"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/color light", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "light", app.Config.Theme)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "light"`)
	out.Reset()

	require.NoError(t, app.Vim([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"kind": "vim"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Equal(t, "vim", app.Config.EditorMode)
	require.True(t, app.readlineVimMode())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"editorMode": "vim"`)
	out.Reset()

	require.NoError(t, app.Effort([]string{"high", "--json"}))
	require.Contains(t, out.String(), `"kind": "effort"`)
	require.Contains(t, out.String(), `"effort": "high"`)
	require.Equal(t, "high", app.Config.ReasoningEffort)
	require.Contains(t, app.systemPrompt(), "<codog_reasoning_effort>high</codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "high"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/fast on", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Fast Mode")
	require.NotNil(t, app.Config.FastMode)
	require.True(t, *app.Config.FastMode)
	require.Contains(t, app.systemPrompt(), "<codog_fast_mode>enabled</codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"fast_mode": true`)
	out.Reset()

	voiceCommand := os.Args[0]
	require.NoError(t, app.Voice([]string{"set-command", voiceCommand, "--json"}))
	require.Contains(t, out.String(), `"kind": "voice"`)
	require.Contains(t, out.String(), `"command_configured": true`)
	require.Contains(t, out.String(), `"command_available": true`)
	require.Equal(t, voiceCommand, app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Voice([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.VoiceEnabled)
	require.True(t, *app.Config.VoiceEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_enabled": true`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.Contains(t, out.String(), `"install_url": "https://claude.ai/chrome"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.True(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"chrome_default_enabled": true`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"permissions"}))
	require.Contains(t, out.String(), "Permissions URL")
	require.Contains(t, out.String(), "https://clau.de/chrome/permissions")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/chrome off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Chrome")
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.False(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"chrome_default_enabled": false`)
	out.Reset()

	require.NoError(t, app.PrivacySettings([]string{"set", "prompt-history", "off", "--json"}))
	require.Contains(t, out.String(), `"kind": "privacy_settings"`)
	require.Contains(t, out.String(), `"prompt_history_enabled": false`)
	require.False(t, app.promptHistoryEnabled())
	require.Empty(t, app.replHistoryFile())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"prompt_history_enabled": false`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/theme clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "", app.Config.Theme)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/effort clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Effort")
	require.Equal(t, "", app.Config.ReasoningEffort)
	require.NotContains(t, app.systemPrompt(), "<codog_reasoning_effort>")
	out.Reset()

	require.NoError(t, app.Fast([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "fast"`)
	require.Nil(t, app.Config.FastMode)
	require.NotContains(t, app.systemPrompt(), "<codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"fast_mode"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/voice clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Voice")
	require.Nil(t, app.Config.VoiceEnabled)
	require.Equal(t, "", app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"voice_enabled"`)
	require.NotContains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Nil(t, app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"chrome_default_enabled"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/privacy-settings enable prompt-history", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Privacy Settings")
	require.True(t, app.promptHistoryEnabled())
	require.Empty(t, errOut.String())
}

func TestKeybindingsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	configHome := t.TempDir()
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			EditorMode: "vim",
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.Keybindings([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "keybindings"`)
	require.Contains(t, out.String(), `"editor_mode": "vim"`)
	require.Contains(t, out.String(), `"vim_mode": true`)
	require.Contains(t, out.String(), `"keybindings_exists": false`)
	out.Reset()

	keybindingsPath := filepath.Join(configHome, "keybindings.json")
	require.NoError(t, app.Keybindings([]string{"path"}))
	require.Equal(t, keybindingsPath+"\n", out.String())
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--json"}))
	require.Contains(t, out.String(), `"status": "created"`)
	require.Contains(t, out.String(), `"created": true`)
	data, err := os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"context": "repl"`)
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte("custom\n"), 0o644))
	require.NoError(t, app.Keybindings([]string{"init"}))
	require.Contains(t, out.String(), "already exists")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(data))
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--force"}))
	require.Contains(t, out.String(), "Wrote keybindings template:")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"bindings"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/keybindings", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Keybindings")
	require.Contains(t, out.String(), "Editor mode      vim")
	require.Contains(t, out.String(), "REPL vim")
	require.Contains(t, out.String(), "Config exists    true")
	require.Empty(t, errOut.String())
}

func TestTerminalSetupCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	path := filepath.Join(t.TempDir(), ".zshrc")
	app := &App{Out: &out, Err: &errOut}

	require.NoError(t, app.TerminalSetup([]string{"install", "--shell", "zsh", "--path", path, "--json"}))
	require.Contains(t, out.String(), `"kind": "terminal_setup"`)
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "codog_statusline")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/terminal-setup status --shell zsh --path "+path, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Terminal Setup")
	require.Contains(t, out.String(), "Installed        true")
	require.Empty(t, errOut.String())
}

func TestRemoteEnvCommandPersistsSettings(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Out: &out, Err: &errOut}

	require.NoError(t, app.RemoteEnv([]string{"set", "--enabled", "on", "--auth-token", "secret-token", "--lease-seconds", "60", "--json"}))
	require.Contains(t, out.String(), `"kind": "remote_env"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 60, app.Config.Future.RemoteLeaseSeconds)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote_enabled": true`)
	require.Contains(t, string(data), `"remote_auth_token": "secret-token"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-env clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Environment")
	require.False(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 0, app.Config.Future.RemoteLeaseSeconds)
	require.Empty(t, errOut.String())
}

func TestRemoteSetupCommandPersistsAndReports(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.RemoteSetup([]string{"enable", "--addr", ":8799", "--auth-token", "secret-token", "--lease-seconds", "120", "--json"}, config.FlagOverrides{SessionID: "setup-session"}))
	require.Contains(t, out.String(), `"kind": "remote_setup"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"session_id": "setup-session"`)
	require.NotContains(t, out.String(), "secret-token")
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 120, app.Config.Future.RemoteLeaseSeconds)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote_enabled": true`)
	require.Contains(t, string(data), `"remote_auth_token": "secret-token"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-setup disable --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "Enabled          false")
	require.Contains(t, out.String(), "127.0.0.1:9999")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/web-setup status --addr 127.0.0.1:8888", &session.Session{ID: "web-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "127.0.0.1:8888")
	require.Contains(t, out.String(), "web-session")
	require.Empty(t, errOut.String())
}

func TestRunCLIRoutesWebSetupAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "web-setup", "status", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "remote_setup"`)
	require.Contains(t, out, `"remote_url": "http://127.0.0.1:8791"`)
}

func TestRunCLIRoutesRemoteControlAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	_, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "remote-control"}, config.FlagOverrides{})
	})
	require.ErrorContains(t, err, "usage: codog bridge serve")
}

func TestDesktopAndMobileHandoffCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("handoff-session", anthropic.TextMessage("user", "hello handoff")))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 90,
				EditorBridgeSocket: "codog.sock",
				EditorBridgeToken:  "bridge-secret",
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Desktop([]string{"--session", "handoff-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "desktop_handoff"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"command": "codog bridge serve"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	out.Reset()

	require.NoError(t, app.Mobile([]string{"ios", "--addr", ":8799", "--resume", "latest", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "mobile_handoff"`)
	require.Contains(t, out.String(), `"platform": "ios"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mobile android --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Mobile Handoff")
	require.Contains(t, out.String(), "android")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/desktop", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Desktop Handoff")
	require.Contains(t, out.String(), "codog bridge serve")
	require.Empty(t, errOut.String())
}

func TestPromptHistoryPreferenceSkipsInputRecords(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	disabled := false
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
			MCPServers:          map[string]config.MCPServerConfig{},
			Privacy:             config.PrivacyConfig{PromptHistoryEnabled: &disabled},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "private prompt", config.FlagOverrides{SessionID: "private-session"}))
	history, err := app.Sessions.PromptHistory("private-session")
	require.NoError(t, err)
	require.Empty(t, history)
	require.Contains(t, out.String(), "done")
}

func TestTodosCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Todos([]string{"add", "write", "tests", "--priority", "high", "--json"}))
	require.Contains(t, out.String(), `"kind": "todos"`)
	require.Contains(t, out.String(), `"priority": "high"`)
	require.FileExists(t, todos.Path(workspace))
	out.Reset()

	require.NoError(t, app.Todos([]string{"done", "todo-1"}))
	require.Contains(t, out.String(), "completed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/todos list", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Todos")
	require.Contains(t, out.String(), "write tests")
	require.Empty(t, errOut.String())
}

func TestSecurityReviewCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "install.sh"), []byte("curl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.SecurityReview([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "security_review"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/security-review --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Security Review")
	require.Contains(t, out.String(), "pipe-to-shell")
	require.Empty(t, errOut.String())
}

func TestBughunterCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc risky(v any) { _, _ = v.(string); panic(\"boom\") }\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Bughunter([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "bughunter"`)
	require.Contains(t, out.String(), `"rule": "ignored-return-value"`)
	require.Contains(t, out.String(), `"rule": "panic-in-runtime-path"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bughunter . --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bughunter")
	require.Contains(t, out.String(), "ignored-return-value")
	require.Empty(t, errOut.String())
}

func TestReviewCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\ncurl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Review([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "review"`)
	require.Contains(t, out.String(), `"status": "findings"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "ultrareview", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review"`)
	require.Contains(t, cliOut, `"rule": "pipe-to-shell"`)

	require.True(t, app.handleSlash(context.Background(), "/review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ultrareview", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
}

func TestFeedbackCommandAndSlashWritesReport(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "feedback context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Feedback([]string{"bug", "report", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "feedback"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Codog Feedback")
	require.Contains(t, string(data), "bug report")
	require.Contains(t, string(data), "source (1 messages)")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/feedback slash report", sess))
	require.Contains(t, out.String(), "Feedback")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 2)
}

func TestPullRequestAndIssueDraftCommands(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "draft context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.PullRequestDraft([]string{"ship", "readme", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "pr"`)
	require.Contains(t, out.String(), `"action": "draft"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "pr-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Pull Request Draft")
	require.Contains(t, string(data), "PR: ship readme")
	require.Contains(t, string(data), "README.md")
	require.Contains(t, string(data), "source (1 messages)")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/issue flaky workflow", sess))
	require.Contains(t, out.String(), "Issue Draft")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "issue-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err = os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Issue Draft")
	require.Contains(t, string(data), "Issue: flaky workflow")
}

func TestCommitPushPRDryRunCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "commit-push-pr", "feat: dry run", "--dry-run", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "commit_push_pr"`)
	require.Contains(t, cliOut, `"status": "planned"`)
	require.Contains(t, cliOut, `"pull_request"`)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.True(t, app.handleSlash(context.Background(), "/commit-push-pr feat: slash dry run --dry-run --no-pr", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Commit Push PR")
	require.Contains(t, out.String(), "Dry run          true")
	require.NotContains(t, out.String(), "pull_request")
	require.Empty(t, errOut.String())
}

func TestInstallGitHubAppCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow", "claude", "--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "install_github_app"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.False(t, fileExists(filepath.Join(workspace, ".github", "workflows", "claude.yml")))
	out.Reset()

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow=review", "--secret-name", "CLAUDE_KEY"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	path := filepath.Join(workspace, ".github", "workflows", "claude-code-review.yml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "anthropics/claude-code-action@v1")
	require.Contains(t, string(data), "${{ secrets.CLAUDE_KEY }}")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/install-github-app --workflow claude --dry-run", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	require.Empty(t, errOut.String())
}

func TestInstallSlackAppCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.InstallSlackApp([]string{"--json"}))
	require.Equal(t, slackAppURL, openedURL)
	require.Contains(t, out.String(), `"kind": "install_slack_app"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"install_count": 1`)
	require.Equal(t, 1, app.Config.Future.SlackAppInstallCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"slack_app_install_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/install-slack-app --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Slack App Setup")
	require.Contains(t, out.String(), slackAppURL)
	require.Equal(t, 2, app.Config.Future.SlackAppInstallCount)
	require.Empty(t, errOut.String())
}

func TestStickersCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.Stickers([]string{"--json"}))
	require.Equal(t, stickerOrderURL, openedURL)
	require.Contains(t, out.String(), `"kind": "stickers"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"order_count": 1`)
	require.Equal(t, 1, app.Config.Future.StickerOrderCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sticker_order_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/stickers --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Sticker Order")
	require.Contains(t, out.String(), stickerOrderURL)
	require.Equal(t, 2, app.Config.Future.StickerOrderCount)
	require.Empty(t, errOut.String())
}

func TestPassesCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	referralURL := "https://example.test/guest-pass"
	require.NoError(t, app.Passes([]string{"set-url", referralURL, "--json"}))
	require.Equal(t, referralURL, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), `"kind": "passes"`)
	require.Contains(t, out.String(), `"referral_url": "`+referralURL+`"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"guest_pass_referral_url": "`+referralURL+`"`)
	out.Reset()

	require.NoError(t, app.Passes([]string{"--json"}))
	require.Equal(t, referralURL, openedURL)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.GuestPassVisitCount)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/passes clear-url", &session.Session{ID: "session"}))
	require.Empty(t, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), "Guest Passes")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/passes --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), guestPassDocsURL)
	require.Equal(t, 2, app.Config.Future.GuestPassVisitCount)
}

func TestExtraUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.ExtraUsage([]string{"--admin", "--json"}))
	require.Equal(t, extraUsageAdminURL, openedURL)
	require.Contains(t, out.String(), `"kind": "extra_usage"`)
	require.Contains(t, out.String(), `"mode": "admin"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.ExtraUsageVisitCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"extra_usage_visit_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/extra-usage --personal --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Extra Usage")
	require.Contains(t, out.String(), extraUsagePersonalURL)
	require.Equal(t, 2, app.Config.Future.ExtraUsageVisitCount)
	require.Empty(t, errOut.String())
}

func TestProjectCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/project\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Project instructions."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "initial project")
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Project(nil))
	require.Contains(t, out.String(), "Project")
	require.Contains(t, out.String(), "Go module")
	require.Contains(t, out.String(), "Memory files     1")
	out.Reset()

	require.NoError(t, app.Project([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "project"`)
	require.Contains(t, out.String(), `"available": true`)
	require.Contains(t, out.String(), `"go_module":`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/project", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Project")
}

func TestEnvCommandRedactsSensitiveValues(t *testing.T) {
	report := buildEnvReport([]string{
		"ALPHA=visible",
		"CODOG_SECRET_TOKEN=hidden",
		"NO_EQUALS",
	})
	require.Equal(t, 2, report.Total)
	require.Equal(t, 1, report.Redacted)
	require.Equal(t, "ALPHA", report.Variables[0].Name)
	require.Equal(t, "visible", report.Variables[0].Value)
	require.Equal(t, "CODOG_SECRET_TOKEN", report.Variables[1].Name)
	require.Equal(t, "[redacted]", report.Variables[1].Value)

	t.Setenv("CODOG_SECRET_TOKEN", "codog-super-secret-value-123")
	var out bytes.Buffer
	app := &App{Out: &out, Err: io.Discard}
	require.NoError(t, app.Env([]string{"--json"}))
	require.Contains(t, out.String(), `"name": "CODOG_SECRET_TOKEN"`)
	require.Contains(t, out.String(), `"value": "[redacted]"`)
	require.NotContains(t, out.String(), "codog-super-secret-value-123")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/env", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Environment")
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

func TestInitVerifiersCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.InitVerifiers([]string{"--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "init_verifiers"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.NoError(t, app.InitVerifiers(nil))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init-verifiers --target codog --force", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "verifier-cli", "SKILL.md"))
	require.Empty(t, errOut.String())
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

func TestHooksCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "prompt.json")
	sessionPath := filepath.Join(workspace, "session.json")
	prePath := filepath.Join(workspace, "pre.json")
	postPath := filepath.Join(workspace, "post.json")
	postFailurePath := filepath.Join(workspace, "post-failure.json")
	stopPath := filepath.Join(workspace, "stop.json")
	compactPath := filepath.Join(workspace, "compact.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				UserPromptSubmit:   []string{"cat > " + shellQuote(promptPath)},
				SessionStart:       []string{"cat > " + shellQuote(sessionPath)},
				PreToolUse:         []string{"cat > " + shellQuote(prePath)},
				PostToolUse:        []string{"cat > " + shellQuote(postPath)},
				PostToolUseFailure: []string{"cat > " + shellQuote(postFailurePath)},
				Stop:               []string{"cat > " + shellQuote(stopPath)},
				PreCompact:         []string{"cat > " + shellQuote(compactPath)},
				UserPromptSubmitCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(promptPath)},
				},
				SessionStartCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(sessionPath)},
				},
				PreToolUseCommands: []config.HookCommand{
					{Matcher: "read_*", Command: "cat > " + shellQuote(prePath)},
				},
				PostToolUseCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postPath)},
				},
				PostToolUseFailureCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postFailurePath)},
				},
				StopCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(stopPath)},
				},
				PreCompactCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(compactPath)},
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Hooks(context.Background(), []string{"list", "--json"}))
	require.Contains(t, out.String(), `"user_prompt_submit"`)
	require.Contains(t, out.String(), `"session_start"`)
	require.Contains(t, out.String(), `"pre_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use_failure"`)
	require.Contains(t, out.String(), `"stop"`)
	require.Contains(t, out.String(), `"pre_compact"`)
	var hooksList hooksListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &hooksList))
	require.Contains(t, hooksList.UserPromptSubmitCommands[0].Command, "cat >")
	require.Contains(t, hooksList.SessionStartCommands[0].Command, "cat >")
	require.Equal(t, "read_*", hooksList.PreToolUseCommands[0].Matcher)
	require.Contains(t, hooksList.PreToolUseCommands[0].Command, "cat >")
	require.Equal(t, "bash", hooksList.PostToolUseFailureCommands[0].Matcher)
	require.Contains(t, hooksList.StopCommands[0].Command, "cat >")
	require.Contains(t, hooksList.PreCompactCommands[0].Command, "cat >")
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "user-prompt-submit", "--input", "hello"}))
	data, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"user_prompt_submit"`)
	require.Contains(t, string(data), `"input":"hello"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "session-start", "--input", `{"source":"startup","session_id":"session"}`}))
	data, err = os.ReadFile(sessionPath)
	require.NoError(t, err)
	var sessionHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &sessionHook))
	require.Equal(t, "session_start", sessionHook.Event)
	require.Contains(t, sessionHook.Input, `"session_id"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "pre", "--tool", "read_file", "--input", `{"path":"README.md"}`}))
	require.Contains(t, out.String(), "Hook Run")
	data, err = os.ReadFile(prePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"pre_tool_use"`)
	require.Contains(t, string(data), `"tool":"read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post --tool=bash --output=done --error", sess))
	data, err = os.ReadFile(postPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post-failure --tool=bash --output=failed --error", sess))
	data, err = os.ReadFile(postFailurePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use_failure"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run stop --output=done", sess))
	data, err = os.ReadFile(stopPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"stop"`)
	require.Contains(t, string(data), `"output":"done"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run pre-compact --input={\"source\":\"manual\"}", sess))
	data, err = os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"source"`)
	require.Empty(t, errOut.String())
}

func TestMCPCommandToolsCallAndResources(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:     []string{"CODOG_AGENT_MCP_HELPER=1"},
	}
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{"test": server}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"tools", "test"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	require.Contains(t, out.String(), `"input_schema"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"auth", "test"}))
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"tool_count": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp tools test", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"call", "test", "echo", `{"text":"hi"}`}))
	require.Contains(t, out.String(), `"text": "hi"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resources", "test"}))
	require.Contains(t, out.String(), "codog://note")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resource-templates", "test"}))
	require.Contains(t, out.String(), "codog://notes/{name}")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"read", "test", "codog://note"}))
	require.Contains(t, out.String(), "note body")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompts", "test"}))
	require.Contains(t, out.String(), `"name": "review"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompt", "test", "review", `{"topic":"hooks"}`}))
	require.Contains(t, out.String(), "Review hooks")
}

func TestMCPConfigCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"add", "demo", "demo-server", "--env", "A=B", "--env=C=D", "arg1", "arg2"}))
	require.Contains(t, out.String(), `"action": "add"`)
	require.Contains(t, out.String(), `"name": "demo"`)
	require.Equal(t, config.MCPServerConfig{Command: "demo-server", Args: []string{"arg1", "arg2"}, Env: []string{"A=B", "C=D"}}, app.Config.MCPServers["demo"])
	configData, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `"mcp_servers"`)
	require.Contains(t, string(configData), `"demo-server"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "demo"}))
	require.Contains(t, out.String(), `"action": "show"`)
	require.Contains(t, out.String(), `"command": "demo-server"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"remove", "demo"}))
	require.Contains(t, out.String(), `"removed": true`)
	_, ok := app.Config.MCPServers["demo"]
	require.False(t, ok)
	configData, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(configData), `"demo"`)

	err = app.MCP(context.Background(), []string{"add", "bad.name", "cmd"})
	require.ErrorContains(t, err, "invalid MCP server name")
}

func TestMCPServeCommand(t *testing.T) {
	workspace := t.TempDir()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{PermissionMode: "workspace-write"},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		In:        input,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"serve"}))
	require.Contains(t, out.String(), `"tools"`)
	require.Contains(t, out.String(), `"read_file"`)
}

func TestSlashAliasesForExistingSurfaces(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	for _, command := range []string{"/ide", "/agents", "/tasks", "/bashes", "/background", "/plugin", "/plugins", "/marketplace", "/providers"} {
		out.Reset()
		errOut.Reset()
		require.True(t, app.handleSlash(context.Background(), command, sess), command)
		require.NotEmpty(t, strings.TrimSpace(out.String()), command)
		require.Empty(t, errOut.String(), command)
	}
}

func TestIDECommandReportsAndClearsEditorState(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	statePath := filepath.Join(configHome, "bridge", "editor-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	state := bridge.EditorState{
		Identity: &bridge.EditorIdentity{
			Editor:    "VS Code",
			Version:   "1.0",
			Workspace: workspace,
			Trusted:   true,
			TrustedAt: time.Now().UTC(),
		},
		OpenFile: &bridge.EditorOpenFile{Path: "main.go", OpenedAt: time.Now().UTC()},
		Selection: &bridge.EditorSelection{
			Path:      "main.go",
			StartLine: 3,
			EndLine:   4,
			Text:      "func main() {}",
			UpdatedAt: time.Now().UTC(),
		},
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0o644))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				EditorBridgeSocket: "codog.sock",
				EditorBridgeToken:  "secret",
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.IDE([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "ide"`)
	require.Contains(t, out.String(), `"editor": "VS Code"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	require.Contains(t, out.String(), `"path": "main.go"`)

	out.Reset()
	require.NoError(t, app.IDE([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"cleared": true`)
	require.NoFileExists(t, statePath)

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/ide", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "IDE Bridge")
	require.Contains(t, out.String(), "Trusted editor   none")
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "bridge_kick"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bridge-kick poll 404", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bridge Kick")
	require.Contains(t, out.String(), "Status           ok")
	require.Contains(t, out.String(), "Recorded         poll 404")
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"action": "poll"`)
	require.Contains(t, out.String(), `"404"`)
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"cleared": true`)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestBriefCommandUsesToolPayloadAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("brief attachment\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Brief([]string{"Build", "passed", "--status", "proactive", "--attach", "notes.md", "--json"}))
	require.Contains(t, out.String(), `"message": "Build passed"`)
	require.Contains(t, out.String(), `"status": "proactive"`)
	require.Contains(t, out.String(), `"is_image": false`)

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/brief Ready for review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Ready for review")
	require.Contains(t, out.String(), "status: normal")
	require.Empty(t, errOut.String())
}

func TestAgentMCPHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_AGENT_MCP_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		line := reader.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeAgentMCP(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "test", "version": "0.0.0"},
			})
		case "tools/list":
			writeAgentMCP(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string"},
					},
				},
			}}})
		case "tools/call":
			writeAgentMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
		case "resources/list":
			writeAgentMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			writeAgentMCP(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			writeAgentMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			writeAgentMCP(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
				"arguments": []map[string]any{{
					"name":     "topic",
					"required": true,
				}},
			}}})
		case "prompts/get":
			writeAgentMCP(id, map[string]any{"messages": []map[string]any{{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": "Review hooks",
				},
			}}})
		}
	}
	os.Exit(0)
}

func writeAgentMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func TestPromptWritesCompletedWorkerState(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var gotHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	var hookDecodeErr error
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookDecodeErr = json.NewDecoder(r.Body).Decode(&gotHook)
		w.WriteHeader(http.StatusOK)
	}))
	defer hookServer.Close()
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
			Hooks: config.HookConfig{
				SessionStartCommands: []config.HookCommand{{Type: "http", URL: hookServer.URL}},
			},
			Future:    config.FutureConfig{},
			AuthToken: "",
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
	require.NoError(t, hookDecodeErr)
	require.Equal(t, "session_start", gotHook.Event)
	require.Contains(t, gotHook.Input, `"source":"resume"`)
	history, err := app.Sessions.PromptHistory("prompt-session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "hello", history[0].Text)
}

func TestBTWUsesForkedSideSession(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "side done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
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
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	require.NoError(t, app.Sessions.Append("main-session", anthropic.TextMessage("user", "source context")))
	sess, err := app.Sessions.Open("main-session")
	require.NoError(t, err)
	sourceMessages := len(sess.Messages)

	require.True(t, app.handleSlash(context.Background(), "/btw answer a side question", sess))
	require.Contains(t, out.String(), "side done")
	require.Contains(t, errOut.String(), "btw session:")
	require.Contains(t, errOut.String(), "source session: main-session")
	require.Len(t, sess.Messages, sourceMessages)

	source, err := app.Sessions.Open("main-session")
	require.NoError(t, err)
	require.Len(t, source.Messages, sourceMessages)
	sideID := extractLineValue(errOut.String(), "btw session:")
	require.NotEmpty(t, sideID)
	side, err := app.Sessions.Open(sideID)
	require.NoError(t, err)
	require.Len(t, side.Messages, sourceMessages+2)
	require.Equal(t, "answer a side question", side.Messages[sourceMessages].Content[0].Text)
	require.Contains(t, side.Messages[sourceMessages+1].Content[0].Text, "side done")
}

func extractLineValue(text string, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func TestPromptExpandsFileReferencesForModelInput(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("note body"), 0o644))
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
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "summarize @notes.md", config.FlagOverrides{SessionID: "prompt-refs"}))
	loaded, err := app.Sessions.Open("prompt-refs")
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "<codog_file_references>")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "note body")
	history, err := app.Sessions.PromptHistory("prompt-refs")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "summarize @notes.md", history[0].Text)
}

func TestSystemPromptIncludesProjectMemory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Always run focused tests."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "CLAUDE.md"), []byte("Prefer Claude-compatible workflows."), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, "<project_memory>")
	require.Contains(t, prompt, "AGENTS.md")
	require.Contains(t, prompt, "Always run focused tests.")
	require.Contains(t, prompt, ".claude/CLAUDE.md")
	require.Contains(t, prompt, "Prefer Claude-compatible workflows.")
}

func TestSystemPromptSupportsOverrideAndAppend(t *testing.T) {
	app := &App{
		Config: config.Config{
			SystemPrompt:       "Custom base.",
			AppendSystemPrompt: "Extra instructions.",
		},
		Workspace: t.TempDir(),
	}

	prompt := app.systemPrompt()
	require.True(t, strings.HasPrefix(prompt, "Custom base."))
	require.Contains(t, prompt, "Extra instructions.")
	require.NotContains(t, prompt, "You are Codog")
}

func TestSystemPromptIncludesSkillFrontmatterMetadata(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	skillDir := filepath.Join(workspace, ".codog", "skills", "review")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: Reviews changed Go files.
allowed-tools: Read, Bash(go test:*)
argument-hint: FILE
paths:
  - internal/**
---
Review body.
`), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome, EnabledSkills: []string{"review"}},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, `<skill name="review"`)
	require.Contains(t, prompt, "Description: Reviews changed Go files.")
	require.Contains(t, prompt, "Allowed tools: Read, Bash(go test:*)")
	require.Contains(t, prompt, "Argument hint: FILE")
	require.Contains(t, prompt, "Paths: internal")
	require.Contains(t, prompt, "Review body.")
	require.NotContains(t, prompt, "allowed-tools:")
	require.NotContains(t, prompt, "---")
}

func TestSystemPromptActivatesSkillsMatchingPromptAndFocusPaths(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	internalSkill := filepath.Join(workspace, ".codog", "skills", "internal-review")
	docsSkill := filepath.Join(workspace, ".codog", "skills", "docs-review")
	otherSkill := filepath.Join(workspace, ".codog", "skills", "script-review")
	require.NoError(t, os.MkdirAll(internalSkill, 0o755))
	require.NoError(t, os.MkdirAll(docsSkill, 0o755))
	require.NoError(t, os.MkdirAll(otherSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(internalSkill, "SKILL.md"), []byte(`---
paths:
  - internal/**
---
Internal review body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(docsSkill, "SKILL.md"), []byte(`---
paths:
  - docs/**/*.md
---
Docs review body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte(`---
paths:
  - scripts/**
---
Script review body.
`), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
	}

	prompt := app.systemPromptForInput("inspect @internal/agent.go")
	require.Contains(t, prompt, "Internal review body.")
	require.NotContains(t, prompt, "Docs review body.")
	require.NotContains(t, prompt, "Script review body.")

	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "guide.md"), []byte("guide"), 0o644))
	_, err := focus.Add(workspace, []string{"docs/guide.md"})
	require.NoError(t, err)

	prompt = app.systemPromptForInput("")
	require.Contains(t, prompt, "Docs review body.")
	require.NotContains(t, prompt, "Script review body.")
}

func TestSkillFrontmatterControlsInvocationAndSystemPrompt(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	visibleDir := filepath.Join(workspace, ".codog", "skills", "visible")
	hiddenDir := filepath.Join(workspace, ".codog", "skills", "hidden")
	disabledDir := filepath.Join(workspace, ".codog", "skills", "disabled")
	require.NoError(t, os.MkdirAll(visibleDir, 0o755))
	require.NoError(t, os.MkdirAll(hiddenDir, 0o755))
	require.NoError(t, os.MkdirAll(disabledDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(visibleDir, "SKILL.md"), []byte("Visible body."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenDir, "SKILL.md"), []byte(`---
user-invocable: false
---
Hidden body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(disabledDir, "SKILL.md"), []byte(`---
disable-model-invocation: true
---
Disabled body.
`), 0o644))
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, EnabledSkills: []string{"visible", "disabled"}},
		Workspace: workspace,
		Err:       &errOut,
	}

	require.Contains(t, app.expandSkillInvocation("visible review this"), `<skill name="visible"`)
	require.Equal(t, "hidden review this", app.expandSkillInvocation("hidden review this"))
	require.False(t, app.handleSkillSlash(context.Background(), "/hidden review this", &session.Session{ID: "session"}))
	require.Empty(t, errOut.String())

	candidates := app.customSlashCompletionCandidates()
	require.Contains(t, candidates, "/visible ")
	require.NotContains(t, candidates, "/hidden ")

	prompt := app.systemPrompt()
	require.Contains(t, prompt, "Visible body.")
	require.NotContains(t, prompt, "Disabled body.")
}

func TestSkillAllowedToolsApplyOnlyToActiveTurn(t *testing.T) {
	workspace := t.TempDir()
	app := &App{
		Config: config.Config{
			PermissionMode: "read-only",
		},
		Workspace: workspace,
		Err:       io.Discard,
	}
	active := &skills.Skill{AllowedTools: []string{"Bash(go test:*)", "Read"}}

	prompter := app.prompterWithSkill("session", active)
	require.Contains(t, prompter.AllowRules, "bash:go test")
	require.Contains(t, prompter.AllowRules, "read_file")
	require.NoError(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go test ./..."}`)))
	require.Error(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go build ./..."}`)))
	require.Empty(t, app.Config.PermissionRules.Allow)
}

func TestSkillAllowedToolsDoNotBypassPlanMode(t *testing.T) {
	workspace := t.TempDir()
	_, err := planmode.Enter(workspace, "inspect before changing anything")
	require.NoError(t, err)
	app := &App{
		Config: config.Config{
			PermissionMode: "workspace-write",
		},
		Workspace: workspace,
		Err:       io.Discard,
	}
	active := &skills.Skill{AllowedTools: []string{"Bash(go test:*)"}}

	prompter := app.prompterWithSkill("session", active)

	require.Equal(t, tools.PermissionReadOnly, prompter.Mode)
	require.NotContains(t, prompter.AllowRules, "bash:go test")
	require.Error(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go test ./..."}`)))
}

func TestSkillsCommandSlashAndBareInvocation(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "skill done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review skill body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill body"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
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
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Skills([]string{"list", "--json"}))
	require.Contains(t, out.String(), `"name": "team:audit"`)
	out.Reset()

	require.NoError(t, app.Skills([]string{"show", "review"}))
	require.Equal(t, "Review skill body\n", out.String())
	out.Reset()

	require.NoError(t, app.Skills([]string{"invoke", "team:audit", "auth"}))
	require.Contains(t, out.String(), `<skill name="team:audit"`)
	require.Contains(t, out.String(), "User request: auth")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills show team:audit", &session.Session{ID: "session"}))
	require.Equal(t, "Audit skill body\n", out.String())
	out.Reset()

	require.NoError(t, app.Prompt(context.Background(), "review auth flow", config.FlagOverrides{SessionID: "skill-session"}))
	require.Contains(t, out.String(), "skill done")
	loaded, err := app.Sessions.Open("skill-session")
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)
	require.Contains(t, loaded.Messages[0].Content[0].Text, `<skill name="review"`)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "Review skill body")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "User request: auth flow")
	require.Contains(t, errOut.String(), "session: skill-session")
}

func TestSkillsInstallAndUninstallCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourceFile := filepath.Join(sourceRoot, "review.md")
	sourceDir := filepath.Join(sourceRoot, "audit")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review body"), 0o644))
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("Audit body"), 0o644))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Skills([]string{"install", sourceFile, "--json"}))
	require.Contains(t, out.String(), `"action": "install"`)
	require.Contains(t, out.String(), `"target": "user"`)
	require.FileExists(t, filepath.Join(configHome, "skills", "review.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"install", "--claude", "--name", "team:audit-copy", sourceDir}))
	require.Contains(t, out.String(), "Skill Installed")
	require.FileExists(t, filepath.Join(workspace, ".claude", "skills", "team", "audit-copy", "SKILL.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills install --project "+sourceFile+" --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"target": "workspace"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "review.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"uninstall", "review", "--project", "--json"}))
	require.Contains(t, out.String(), `"removed": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "skills", "review.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills uninstall team:audit-copy --claude", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Skill Uninstalled")
	require.NoDirExists(t, filepath.Join(workspace, ".claude", "skills", "team", "audit-copy"))
}

func TestTemplatesCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "templates"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "templates", "review.md"), []byte("Review {{target}} as {{role}}."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "templates", "plan.md"), []byte("Plan {{topic}}."), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Templates(nil))
	require.Contains(t, out.String(), "plan\tworkspace")
	require.Contains(t, out.String(), "review\tuser")
	out.Reset()

	require.NoError(t, app.Templates([]string{"show", "review"}))
	require.Contains(t, out.String(), "Review {{target}} as {{role}}.")
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "review", "--var", "target=auth", "role=reviewer"}))
	require.Equal(t, "Review auth as reviewer.\n", out.String())
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "plan", "--json", "--var=topic=tests"}))
	require.Contains(t, out.String(), `"kind": "template_apply"`)
	require.Contains(t, out.String(), `"rendered": "Plan tests."`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates apply plan topic=release", &session.Session{ID: "session"}))
	require.Equal(t, "Plan release.\n", out.String())
	require.Empty(t, errOut.String())
}

func TestCommandsCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "commands", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "fix.md"), []byte("Claude fix {{args}}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "fix.md"), []byte("Codog fix {{ ARGUMENTS }}"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Commands([]string{"list"}))
	require.Contains(t, out.String(), "fix\tclaude")
	require.Contains(t, out.String(), "fix\tworkspace")
	require.Contains(t, out.String(), "review\tuser")
	out.Reset()

	require.NoError(t, app.Commands([]string{"show", "fix", "--json"}))
	require.Contains(t, out.String(), `"source": "workspace"`)
	out.Reset()

	require.NoError(t, app.Commands([]string{"run", "fix", "bug", "123"}))
	require.Equal(t, "Codog fix bug 123\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands run review file.go", &session.Session{ID: "session"}))
	require.Equal(t, "Review file.go\n", out.String())
	require.Empty(t, errOut.String())
}

func TestCustomSlashRunsRenderedPrompt(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "custom done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "review.md"), []byte("Review this target: $ARGUMENTS"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
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
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("custom-slash")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/team/review target.go", sess))
	require.Contains(t, out.String(), "custom done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "user", sess.Messages[0].Role)
	require.Equal(t, "Review this target: target.go", sess.Messages[0].Content[0].Text)
	history, err := app.Sessions.PromptHistory("custom-slash")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "Review this target: target.go", history[0].Text)
}

func TestCustomSlashAllowedToolsApplyToActiveTurn(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Turns: []mockanthropic.Turn{
		{ToolUses: []mockanthropic.ToolUse{{
			ID:    "toolu_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"echo command-ok"}`),
		}}},
		{Text: "custom done"},
	}}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "check.md"), []byte(`---
allowed-tools: Bash(echo:*)
---
Check this target: $ARGUMENTS`), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            2,
			AutoCompactMessages: 40,
			PermissionMode:      "read-only",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("custom-allowed")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/check target.go", sess))

	require.Contains(t, out.String(), "custom done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 4)
	require.Equal(t, "tool_result", sess.Messages[2].Content[0].Type)
	require.False(t, sess.Messages[2].Content[0].IsError)
	require.Contains(t, sess.Messages[2].Content[0].Content, "command-ok")
}

func TestSkillSlashRunsRenderedPrompt(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "skill done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill body"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
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
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("skill-slash")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/team/audit auth", sess))
	require.Contains(t, out.String(), "skill done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "user", sess.Messages[0].Role)
	require.Contains(t, sess.Messages[0].Content[0].Text, `<skill name="team:audit"`)
	require.Contains(t, sess.Messages[0].Content[0].Text, "User request: auth")
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
	out.Reset()

	htmlOutput := filepath.Join(workspace, "transcript.html")
	require.NoError(t, app.Export([]string{"--session=source", "--format=html", "--output", htmlOutput}))
	require.Contains(t, out.String(), `"format": "html"`)
	data, err = os.ReadFile(htmlOutput)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "export me")
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
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/export --format html", sess))
	require.Contains(t, errOut.String(), "exported session source")
	data, err = os.ReadFile(filepath.Join(workspace, "slash-export.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
}

func TestShareCommandAndSlashWritesLocalArtifact(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "share me")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Share([]string{"--session", "source", "--format=json", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "share"`)
	require.Contains(t, out.String(), `"format": "json"`)
	sharedJSON := filepath.Join(workspace, ".codog", "share", "source.json")
	data, err := os.ReadFile(sharedJSON)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
	out.Reset()

	require.NoError(t, app.Share([]string{"--session", "source", "--format=html", "html-share"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Shared session source")
	data, err = os.ReadFile(filepath.Join(workspace, "html-share", "source.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/share shared", sess))
	require.Empty(t, errOut.String())
	require.Contains(t, out.String(), "Shared session source")
	sharedMarkdown := filepath.Join(workspace, "shared", "source.md")
	data, err = os.ReadFile(sharedMarkdown)
	require.NoError(t, err)
	require.Contains(t, string(data), "share me")
}

func TestCopyCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "copy prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "copy response")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "copy followup")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "latest copy response")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var copied []byte
	previousClipboard := writeClipboard
	writeClipboard = func(_ context.Context, data []byte) (string, error) {
		copied = append([]byte(nil), data...)
		return "test-clipboard", nil
	}
	t.Cleanup(func() { writeClipboard = previousClipboard })
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Copy(context.Background(), []string{"last", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Equal(t, "latest copy response\n", string(copied))
	require.Contains(t, out.String(), `"clipboard": "test-clipboard"`)
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"2", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Equal(t, "copy response\n", string(copied))
	require.Contains(t, out.String(), `"scope": "nth"`)
	require.Contains(t, out.String(), `"nth": 2`)
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"all", "--session=source", "--format=json"}, config.FlagOverrides{}))
	require.Contains(t, string(copied), `"id": "source"`)
	require.Contains(t, out.String(), "Copied all")
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"all", "--session=source", "--format=html"}, config.FlagOverrides{}))
	require.Contains(t, string(copied), "<!doctype html>")
	require.Contains(t, out.String(), "Copied all")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/copy 2", sess))
	require.Equal(t, "copy response\n", string(copied))
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/copy all", sess))
	require.Contains(t, string(copied), "# Conversation Export")
	require.Empty(t, errOut.String())
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

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	require.NoError(t, writer.Close())
	data, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	require.NoError(t, reader.Close())
	return string(data), runErr
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

func TestBackgroundSlashAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/bashes list", &session.Session{ID: "session-1"}))
	require.Equal(t, "[]\n", out.String())
	require.Empty(t, errOut.String())
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

	require.NoError(t, app.CodeIntel([]string{"lsp", "start", "go", "sleep", "30"}))
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

func TestLoginLogoutAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	err := app.Login(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth browser login PROFILE")

	err = app.Login([]string{"device"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth device")

	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-token-1234"})
	require.NoError(t, err)
	require.NoError(t, app.Logout(nil))
	require.Contains(t, out.String(), `"deleted": true`)
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)

	out.Reset()
	require.Empty(t, errOut.String())
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "slash-access-1234"})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/logout", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.Empty(t, errOut.String())
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
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

func TestProvidersStatusRedactsAuth(t *testing.T) {
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{AccessToken: "stored-access-token"})
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      "claude-sonnet-4-5",
			MaxTokens:  4096,
			MaxTurns:   8,
			APIKey:     "api-key-secret",
			AuthToken:  "stored-access-token",
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"name": "anthropic"`)
	require.Contains(t, out.String(), `"stored_oauth"`)
	require.Contains(t, out.String(), `"api_key": true`)
	require.NotContains(t, out.String(), "api-key-secret")
	require.NotContains(t, out.String(), "stored-access-token")
}

func TestProvidersSetWritesConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "provider.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      config.DefaultModel,
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"set", "custom", "--base-url", "http://127.0.0.1:8080", "--model", "claude-local", "--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "custom"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "http://127.0.0.1:8080"`)
	require.Contains(t, string(data), `"model": "claude-local"`)
}

func TestProvidersShowCurrent(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			BaseURL:    "https://provider.example",
			Model:      "claude-compatible",
			MaxTokens:  2048,
			MaxTurns:   4,
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "custom"`)
	require.Contains(t, out.String(), `"base_url": "https://provider.example"`)
	require.Contains(t, out.String(), `"model": "claude-compatible"`)
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
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, app.OAuthRefresh([]string{"--json"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/oauth-refresh", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
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

func TestReloadPluginsRebuildsCurrentToolRegistry(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Out:       &out,
		Err:       &errOut,
	}
	require.False(t, app.Tools.Has("demo_tool"))

	require.NoError(t, app.ReloadPlugins([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "reload_plugins"`)
	require.Contains(t, out.String(), `"plugins": 1`)
	require.Contains(t, out.String(), `"plugin_tools": 1`)
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	require.NoError(t, app.ReloadPlugins(nil))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/reload-plugins", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.Empty(t, errOut.String())
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

	aliasArtifact := filepath.Join(dir, "codog-alias-new")
	aliasTarget := filepath.Join(dir, "codog-alias")
	require.NoError(t, os.WriteFile(aliasArtifact, []byte("alias-new"), 0o755))
	require.NoError(t, os.WriteFile(aliasTarget, []byte("alias-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Install(context.Background(), []string{aliasArtifact, aliasTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(aliasTarget)
	require.NoError(t, err)
	require.Equal(t, "alias-new", string(data))

	upgradeArtifact := filepath.Join(dir, "codog-upgrade-new")
	upgradeTarget := filepath.Join(dir, "codog-upgrade")
	require.NoError(t, os.WriteFile(upgradeArtifact, []byte("upgrade-new"), 0o755))
	require.NoError(t, os.WriteFile(upgradeTarget, []byte("upgrade-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Upgrade(context.Background(), []string{"install", upgradeArtifact, upgradeTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(upgradeTarget)
	require.NoError(t, err)
	require.Equal(t, "upgrade-new", string(data))
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
