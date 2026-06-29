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
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/session"
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
	out.Reset()

	require.NoError(t, app.DumpManifests(nil))
	require.Contains(t, out.String(), "Manifest Dump")
	require.Contains(t, out.String(), "Bootstrap phases")
	out.Reset()

	otherWorkspace := t.TempDir()
	require.NoError(t, app.DumpManifests([]string{"--manifests-dir", otherWorkspace, "--json"}))
	require.Contains(t, out.String(), otherWorkspace)

	err := app.DumpManifests([]string{"--manifests-dir", filepath.Join(t.TempDir(), "missing")})
	require.ErrorContains(t, err, "missing_manifests")
}

func TestBootstrapPlanCommand(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}

	require.NoError(t, app.BootstrapPlan([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "bootstrap-plan"`)
	require.Contains(t, out.String(), `"name": "load_config"`)
	out.Reset()

	require.NoError(t, app.BootstrapPlan(nil))
	require.Contains(t, out.String(), "Bootstrap Plan")
	require.Contains(t, out.String(), "load_config")
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

func TestSystemPromptAndToolDetailsSlashCommands(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use the project style."), 0o644))
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
	require.Contains(t, out.String(), "Tools            38")
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

	require.True(t, app.handleSlash(context.Background(), "/memory show AGENTS.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory")
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
}

func TestUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("usage-session", anthropic.TextMessage("user", "hello usage")))
	require.NoError(t, store.Append("usage-session", anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		}},
	}))
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
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stats", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/summary", sess))
	require.Contains(t, out.String(), "Summary")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Empty(t, errOut.String())
}

func TestCompactCommandPersistsCompactedSession(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "four")))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, AutoCompactMessages: 2},
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

	require.True(t, app.handleSlash(context.Background(), "/review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
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
	prePath := filepath.Join(workspace, "pre.json")
	postPath := filepath.Join(workspace, "post.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				PreToolUse:  []string{"cat > " + shellQuote(prePath)},
				PostToolUse: []string{"cat > " + shellQuote(postPath)},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Hooks(context.Background(), []string{"list", "--json"}))
	require.Contains(t, out.String(), `"pre_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "pre", "--tool", "read_file", "--input", `{"path":"README.md"}`}))
	require.Contains(t, out.String(), "Hook Run")
	data, err := os.ReadFile(prePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"pre_tool_use"`)
	require.Contains(t, string(data), `"tool":"read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post --tool=bash --output=done --error", sess))
	data, err = os.ReadFile(postPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use"`)
	require.Contains(t, string(data), `"is_error":true`)
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

	for _, command := range []string{"/agents", "/tasks", "/background", "/plugin", "/plugins", "/marketplace", "/providers"} {
		out.Reset()
		errOut.Reset()
		require.True(t, app.handleSlash(context.Background(), command, sess), command)
		require.NotEmpty(t, strings.TrimSpace(out.String()), command)
		require.Empty(t, errOut.String(), command)
	}
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
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, "<project_memory>")
	require.Contains(t, prompt, "AGENTS.md")
	require.Contains(t, prompt, "Always run focused tests.")
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

func TestLoginLogoutAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
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
