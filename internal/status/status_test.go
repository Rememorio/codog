package status

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OLLAMA_HOST",
		"XAI_API_KEY",
		"XAI_BASE_URL",
		"DASHSCOPE_API_KEY",
		"DASHSCOPE_BASE_URL",
		"CODOG_API_KEY",
		"CODOG_AUTH_TOKEN",
		"CODOG_BASE_URL",
	} {
		_ = os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

func TestBuildParsesGitStatus(t *testing.T) {
	snapshot := Build(Options{
		Version:              "test-version",
		FormatSource:         "flag",
		FormatRaw:            "json",
		FormatOverridden:     true,
		Workspace:            "/repo/codog",
		Model:                "claude-test",
		PermissionMode:       "workspace-write",
		PermissionModeRaw:    "acceptEdits",
		PermissionModeSource: "config",
		PermissionRules: config.PermissionRules{
			Allow:       []string{"Read", "mcp__demo__*"},
			Deny:        []string{"Bash(rm:*)", "Bsh(echo:*)"},
			Ask:         []string{"WebFetch"},
			DeniedTools: []string{"write_file"},
		},
		AuthConfigured: true,
		PlanActive:     true,
		PlanText:       "inspect first",
		PlanUpdatedAt:  "2026-01-01T00:00:00Z",
		MemoryFiles: []MemoryFileStatus{{
			Path:       "/repo/codog/AGENTS.md",
			Name:       "AGENTS.md",
			Scope:      "/repo/codog",
			Chars:      18,
			Lines:      1,
			Words:      3,
			SizeBytes:  24,
			ModifiedAt: "2026-07-01T12:00:00Z",
			AgeSeconds: 60,
		}},
		ToolNames:          []string{"bash", "read_file", "web_fetch", "write_file"},
		AllowedToolEntries: []string{"read_file", "grep"},
		ToolAliases:        map[string]string{"Read": "read_file", "WebFetch": "web_fetch"},
		LaneBoard: &background.LaneBoard{
			GeneratedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
			Active: []background.LaneBoardEntry{{
				TaskID:    "task-1",
				Status:    "running",
				Freshness: background.LaneFreshnessHealthy,
			}},
		},
		GitStatus: stringsJoinLines(
			"## main...origin/main [ahead 1]",
			" M README.md",
			"A  internal/status/status.go",
			"?? notes.txt",
			"UU conflict.txt",
		),
		SandboxOS:        "darwin",
		SandboxDefault:   "sandbox-exec",
		SandboxAvailable: true,
	})

	require.Equal(t, "ok", snapshot.Status)
	require.Equal(t, "flag", snapshot.FormatSource)
	require.Equal(t, "json", snapshot.FormatRaw)
	require.True(t, snapshot.FormatOverridden)
	require.Equal(t, "codog", snapshot.Workspace.Name)
	require.Equal(t, "workspace-write", snapshot.Config.PermissionMode)
	require.Equal(t, "acceptEdits", snapshot.Config.PermissionModeRaw)
	require.Equal(t, "config", snapshot.Config.PermissionModeSource)
	require.Equal(t, 1, snapshot.Workspace.MemoryFileCount)
	require.Equal(t, "AGENTS.md", snapshot.Workspace.MemoryFiles[0].Name)
	require.Equal(t, 3, snapshot.Workspace.MemoryFiles[0].Words)
	require.Equal(t, int64(24), snapshot.Workspace.MemoryFiles[0].SizeBytes)
	require.Equal(t, "2026-07-01T12:00:00Z", snapshot.Workspace.MemoryFiles[0].ModifiedAt)
	require.Equal(t, int64(60), snapshot.Workspace.MemoryFiles[0].AgeSeconds)
	require.Equal(t, "main", snapshot.Git.Branch)
	require.False(t, snapshot.Git.Clean)
	require.Equal(t, 1, snapshot.Git.Staged)
	require.Equal(t, 1, snapshot.Git.Unstaged)
	require.Equal(t, 1, snapshot.Git.Untracked)
	require.Equal(t, 1, snapshot.Git.Conflicts)
	require.Equal(t, 4, snapshot.Tools.Count)
	require.True(t, snapshot.AllowedTools.Restricted)
	require.Equal(t, "configured", snapshot.AllowedTools.Source)
	require.Equal(t, []string{"read_file", "grep"}, snapshot.AllowedTools.Entries)
	require.Equal(t, []string{"bash", "read_file", "web_fetch", "write_file"}, snapshot.AllowedTools.Available)
	require.Equal(t, "read_file", snapshot.AllowedTools.Aliases["Read"])
	require.Len(t, snapshot.Config.PermissionRules.Allow, 2)
	require.Equal(t, "Read", snapshot.Config.PermissionRules.Allow[0].Raw)
	require.Equal(t, "Read", snapshot.Config.PermissionRules.Allow[0].Tool)
	require.Equal(t, "read_file", snapshot.Config.PermissionRules.Allow[0].ResolvedToolName)
	require.Equal(t, "mcp__demo__*", snapshot.Config.PermissionRules.Allow[1].ResolvedToolName)
	require.Len(t, snapshot.Config.PermissionRules.Deny, 2)
	require.Equal(t, "Bash(rm:*)", snapshot.Config.PermissionRules.Deny[0].Raw)
	require.Equal(t, "Bash", snapshot.Config.PermissionRules.Deny[0].Tool)
	require.Equal(t, "bash", snapshot.Config.PermissionRules.Deny[0].ResolvedToolName)
	require.Equal(t, "rm", snapshot.Config.PermissionRules.Deny[0].Matcher)
	require.True(t, snapshot.Config.PermissionRules.Deny[1].UnknownTool)
	require.Equal(t, 1, snapshot.Config.PermissionRules.UnknownCount)
	require.Equal(t, "web_fetch", snapshot.Config.PermissionRules.Ask[0].ResolvedToolName)
	require.Equal(t, "write_file", snapshot.Config.PermissionRules.DeniedTools[0].ResolvedToolName)
	require.True(t, snapshot.Plan.Active)
	require.Equal(t, "inspect first", snapshot.Plan.Text)
	require.True(t, snapshot.LaneBoard.StatusJSONSupported)
	require.Equal(t, background.LaneFreshnessTransportDead, snapshot.LaneBoard.FreshnessStates[2])
	require.True(t, snapshot.LaneBoard.Available)
	require.Equal(t, 1, snapshot.LaneBoard.ActiveCount)
	require.Equal(t, "task-1", snapshot.LaneBoard.Active[0].TaskID)
}

func TestBuildReportsDefaultAllowedTools(t *testing.T) {
	snapshot := Build(Options{
		ToolNames:   []string{"agent", "bash"},
		ToolAliases: map[string]string{"WebFetch": "web_fetch"},
		GitStatus:   "## main",
	})

	require.Equal(t, "default", snapshot.FormatSource)
	require.Empty(t, snapshot.FormatRaw)
	require.False(t, snapshot.FormatOverridden)
	require.False(t, snapshot.AllowedTools.Restricted)
	require.Equal(t, "default", snapshot.AllowedTools.Source)
	require.Empty(t, snapshot.AllowedTools.Entries)
	require.Equal(t, []string{"agent", "bash"}, snapshot.AllowedTools.Available)
	require.Equal(t, "web_fetch", snapshot.AllowedTools.Aliases["WebFetch"])
}

func TestBuildMarksGitErrorDegraded(t *testing.T) {
	snapshot := Build(Options{
		Version:  "test-version",
		GitError: "not a git repository",
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.False(t, snapshot.Git.Available)
	require.Contains(t, snapshot.Git.Error, "not a git repository")
}

func TestBuildWarnsOnStaleBranchFreshness(t *testing.T) {
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## topic",
		GitFreshness: &gitops.BranchFreshness{
			Branch:       "topic",
			Base:         "main",
			Upstream:     "origin/topic",
			HasUpstream:  true,
			Status:       "stale",
			Fresh:        false,
			Ahead:        0,
			Behind:       2,
			MissingFixes: []string{"fix: resolve timeout"},
		},
	})

	require.Equal(t, "warn", snapshot.Status)
	require.NotNil(t, snapshot.Git.Freshness)
	require.Equal(t, "stale", snapshot.Git.Freshness.Status)
	require.True(t, snapshot.Git.Freshness.HasUpstream)
	require.Equal(t, "origin/topic", snapshot.Git.Freshness.Upstream)
	require.True(t, snapshot.Git.HasUpstream)
	require.Equal(t, "origin/topic", snapshot.Git.Upstream)
	require.Equal(t, 2, snapshot.Git.Freshness.Behind)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Git freshness    status=stale base=main upstream=origin/topic ahead=0 behind=2")
}

func TestBuildIncludesGitIdentity(t *testing.T) {
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## main",
		GitIdentity: &gitops.Identity{
			HeadSHA:      "1234567890abcdef1234567890abcdef12345678",
			HeadShortSHA: "1234567890ab",
			HeadRef:      "main",
			GitDir:       "/repo/.git",
		},
	})

	require.Equal(t, "1234567890abcdef1234567890abcdef12345678", snapshot.Git.HeadSHA)
	require.Equal(t, "1234567890ab", snapshot.Git.HeadShortSHA)
	require.Equal(t, "main", snapshot.Git.HeadRef)
	require.False(t, snapshot.Git.IsDetached)
	require.False(t, snapshot.Git.IsBare)
	require.False(t, snapshot.Git.IsWorktree)
	require.Equal(t, "/repo/.git", snapshot.Git.GitDir)
	require.NotNil(t, snapshot.BootPreflight.Repo.Identity)
	require.Equal(t, "1234567890abcdef1234567890abcdef12345678", snapshot.BootPreflight.Repo.Identity.HeadSHA)
	require.Equal(t, "main", snapshot.BootPreflight.Repo.Identity.HeadRef)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Git head         ref=main sha=1234567890ab detached=false bare=false worktree=false")
}

func TestBuildWarnsOnDivergedBaseCommit(t *testing.T) {
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## main",
		GitFreshness: &gitops.BranchFreshness{
			Branch: "main",
			Base:   "main",
			Status: "fresh",
			Fresh:  true,
		},
		GitBaseCommit: &gitops.BaseCommitCheck{
			Status:   "diverged",
			Matches:  false,
			Source:   &gitops.BaseCommitSource{Kind: "codog_file", Value: "abc123"},
			Expected: "abc123",
			Actual:   "def456",
			Warning:  "warning: stale-base",
		},
	})

	require.Equal(t, "warn", snapshot.Status)
	require.NotNil(t, snapshot.Git.BaseCommit)
	require.Equal(t, "diverged", snapshot.Git.BaseCommit.Status)
	require.False(t, snapshot.Git.BaseCommit.Matches)
	require.Equal(t, "abc123", snapshot.Git.BaseCommit.Expected)
	require.Equal(t, "def456", snapshot.Git.BaseCommit.Actual)
	require.NotNil(t, snapshot.BootPreflight.Repo.BaseCommit)
	require.Equal(t, "diverged", snapshot.BootPreflight.Repo.BaseCommit.Status)
	require.Equal(t, "abc123", snapshot.BootPreflight.Repo.BaseCommit.Expected)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Git base         status=diverged matches=false expected=abc123 actual=def456")
}

func TestBuildMarksInvalidValidationDegraded(t *testing.T) {
	index := 0
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## main",
		MCPValidation: MCPValidationStatus{
			TotalConfigured: 1,
			RequiredCount:   1,
			InvalidCount:    1,
			InvalidServers: []ValidationIssue{{
				Name:       "bad",
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command",
				Valid:      false,
			}},
		},
		HookValidation: HookValidationStatus{
			ValidCount:   1,
			InvalidCount: 1,
			InvalidHooks: []ValidationIssue{{
				Event:      "pre_tool_use",
				Index:      &index,
				HookIndex:  &index,
				Kind:       "unsupported_type",
				ErrorField: "type",
				Reason:     "unsupported hook type webhook",
				Valid:      false,
			}},
		},
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, 1, snapshot.MCPValidation.InvalidCount)
	require.Equal(t, 1, snapshot.HookValidation.InvalidCount)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "MCP validation   valid=0 invalid=1 required=1 optional=0")
	require.Contains(t, out.String(), "Hook validation  valid=1 invalid=1")
}

func TestBuildMarksInvalidConfigValidationDegraded(t *testing.T) {
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## main",
		ConfigValidation: ConfigValidationStatus{
			Status:       "error",
			FileCount:    1,
			PresentCount: 1,
			ErrorCount:   1,
			Paths:        []string{".codog.json"},
		},
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, 1, snapshot.ConfigValidation.ErrorCount)
	require.Equal(t, []string{".codog.json"}, snapshot.ConfigValidation.Paths)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Config validation status=error files=1 present=1 errors=1 warnings=0")
}

func TestBuildBootPreflightReportsStartupReadiness(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0o755))
	executable := filepath.Join(workspace, "codog-test-bin")
	require.NoError(t, os.WriteFile(executable, []byte("bin"), 0o755))

	snapshot := Build(Options{
		Version:              "test-version",
		Workspace:            workspace,
		Executable:           executable,
		GitStatus:            "## main...origin/main",
		TrustedRoots:         []string{filepath.Dir(workspace), "  "},
		InstalledPluginCount: 2,
		MCPValidation: MCPValidationStatus{
			TotalConfigured: 1,
			ValidCount:      1,
			OptionalCount:   1,
		},
		LastFailedBootReason: "previous mcp discovery timeout",
	})

	require.Equal(t, "ok", snapshot.Status)
	require.True(t, snapshot.BootPreflight.Repo.WorkspaceExists)
	require.True(t, snapshot.BootPreflight.Repo.GitDirExists)
	require.True(t, snapshot.BootPreflight.Repo.GitAvailable)
	require.True(t, snapshot.BootPreflight.Trust.Allowed)
	require.Equal(t, 1, snapshot.BootPreflight.Trust.TrustedRootsCount)
	require.True(t, snapshot.BootPreflight.MCPStartup.Eligible)
	require.Equal(t, 1, snapshot.BootPreflight.MCPStartup.Configured)
	require.Equal(t, 1, snapshot.BootPreflight.MCPStartup.Loaded)
	require.True(t, snapshot.BootPreflight.PluginStartup.Eligible)
	require.Equal(t, 2, snapshot.BootPreflight.PluginStartup.Configured)
	require.Len(t, snapshot.BootPreflight.RequiredBinaries, 3)
	codogBinary := bootBinaryByName(snapshot.BootPreflight.RequiredBinaries, "codog")
	require.True(t, codogBinary.Available)
	require.Equal(t, executable, codogBinary.Path)
	require.Equal(t, "git", bootBinaryByName(snapshot.BootPreflight.RequiredBinaries, "git").Name)
	require.Equal(t, "tmux", bootBinaryByName(snapshot.BootPreflight.RequiredBinaries, "tmux").Name)
	require.Equal(t, "previous mcp discovery timeout", snapshot.BootPreflight.LastFailedBootReason)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Boot preflight   repo=true trust=true mcp=true plugins=true")
	require.Contains(t, out.String(), "Required bins    available=")
	require.Contains(t, out.String(), "Last boot error  previous mcp discovery timeout")
}

func bootBinaryByName(binaries []BootBinaryPreflightStatus, name string) BootBinaryPreflightStatus {
	for _, binary := range binaries {
		if binary.Name == name {
			return binary
		}
	}
	return BootBinaryPreflightStatus{}
}

func TestBuildBootPreflightPluginLoadErrorDegrades(t *testing.T) {
	snapshot := Build(Options{
		Version:         "test-version",
		GitStatus:       "## main",
		PluginLoadError: "plugin manifest unreadable",
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.False(t, snapshot.BootPreflight.PluginStartup.Eligible)
	require.Equal(t, "plugin manifest unreadable", snapshot.BootPreflight.PluginStartup.Error)
}

func TestBuildMarksConfigLoadErrorDegraded(t *testing.T) {
	snapshot := Build(Options{
		Version:             "test-version",
		GitStatus:           "## main",
		ConfigLoadError:     "broken.json: unexpected end of JSON input",
		ConfigLoadErrorKind: "config_load_failed",
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "broken.json: unexpected end of JSON input", snapshot.ConfigLoadError)
	require.Equal(t, "config_load_failed", snapshot.ConfigLoadErrorKind)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Config load      degraded: broken.json")
}

func TestBuildReportsProviderEndpointProvenance(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:11434/v1")
	snapshot := Build(Options{
		Version:               "test-version",
		GitStatus:             "## main",
		Model:                 "qwen3:8b",
		RuntimeProvider:       modelrouting.ProviderOpenAI,
		RuntimeProviderSource: "OPENAI_BASE_URL",
		BaseURL:               "http://127.0.0.1:11434/v1",
	})

	require.Equal(t, "ok", snapshot.Status)
	require.Equal(t, modelrouting.ProviderOpenAI, snapshot.Config.RuntimeProvider)
	require.Equal(t, "OPENAI_BASE_URL", snapshot.Config.RuntimeProviderSource)
	require.Equal(t, modelrouting.ProviderOpenAI, snapshot.Config.ProviderEndpoint.Provider)
	require.Equal(t, "OPENAI_BASE_URL", snapshot.Config.ProviderEndpoint.Env)
	require.Equal(t, "env", snapshot.Config.ProviderEndpoint.Source)
	require.Equal(t, "http", snapshot.Config.ProviderEndpoint.Scheme)
	require.Equal(t, "127.0.0.1", snapshot.Config.ProviderEndpoint.Host)
	require.True(t, snapshot.Config.ProviderEndpoint.Valid)
	require.True(t, snapshot.Config.ProviderEndpoint.Local)
}

func TestBuildMarksInvalidProviderEndpointDegraded(t *testing.T) {
	snapshot := Build(Options{
		Version:         "test-version",
		GitStatus:       "## main",
		Model:           "openai/gpt-4.1-mini",
		RuntimeProvider: modelrouting.ProviderOpenAI,
		BaseURL:         "ftp://example.com/v1",
	})

	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, modelrouting.ProviderOpenAI, snapshot.Config.ProviderEndpoint.Provider)
	require.Equal(t, "OPENAI_BASE_URL", snapshot.Config.ProviderEndpoint.Env)
	require.Equal(t, "configured", snapshot.Config.ProviderEndpoint.Source)
	require.False(t, snapshot.Config.ProviderEndpoint.Valid)
	require.Equal(t, "unsupported_scheme", snapshot.Config.ProviderEndpoint.ErrorKind)
	require.Equal(t, "ftp", snapshot.Config.ProviderEndpoint.Scheme)
	require.Equal(t, "example.com", snapshot.Config.ProviderEndpoint.Host)
}

func TestBuildMirrorsProviderAuthWarning(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-real")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-bearer")
	snapshot := Build(Options{
		Version:        "test-version",
		GitStatus:      "## main",
		Model:          "claude-test",
		BaseURL:        modelrouting.DefaultAnthropicBaseURL,
		APIKey:         "sk-ant-api03-real",
		AuthToken:      "stale-bearer",
		AuthConfigured: true,
	})

	require.Equal(t, "warn", snapshot.Status)
	require.True(t, snapshot.Config.AuthConfigured)
	require.Equal(t, modelrouting.ProviderAnthropic, snapshot.Config.Auth.SelectedProvider)
	require.Equal(t, "api_key_and_bearer", snapshot.Config.Auth.EffectiveAuthSource)
	require.Equal(t, []string{"x-api-key", "authorization_bearer"}, snapshot.Config.Auth.HeadersSent)
	require.True(t, snapshot.Config.Auth.BothAnthropicAuthEnvVarsPresent)
	require.True(t, snapshot.Config.Auth.SelectedProviderBothAuthPresent)
	require.Contains(t, snapshot.Config.Auth.Warning, "both ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN")
}

func TestBuildParsesInitialBranch(t *testing.T) {
	snapshot := Build(Options{GitStatus: "## No commits yet on main"})

	require.Equal(t, "main", snapshot.Git.Branch)
	require.True(t, snapshot.Git.Clean)
}

func TestRenderText(t *testing.T) {
	snapshot := Build(Options{
		Version:                "test-version",
		Workspace:              "/repo/codog",
		Model:                  "claude-test",
		PermissionMode:         "read-only",
		AuthConfigured:         true,
		SessionID:              "session-1",
		SessionMessages:        3,
		SessionParentSessionID: "parent-1",
		SessionBranchName:      "investigation",
		ToolNames:              []string{"bash"},
		LaneBoard:              &background.LaneBoard{},
		GitStatus:              "## main",
		SandboxDefault:         "sandbox-exec",
	})

	var out bytes.Buffer
	RenderText(&out, snapshot)

	require.Contains(t, out.String(), "Status")
	require.Contains(t, out.String(), "Version          test-version")
	require.Contains(t, out.String(), "Memory files     0")
	require.Contains(t, out.String(), "Plan             inactive")
	require.Contains(t, out.String(), "Session          session-1")
	require.Contains(t, out.String(), "Session state    saved only")
	require.Contains(t, out.String(), "Session parent   parent-1")
	require.Contains(t, out.String(), "Session branch   investigation")
	require.Contains(t, out.String(), "Git              branch=main")
	require.Contains(t, out.String(), "Task lanes       active=0 blocked=0 finished=0")
	require.Contains(t, out.String(), "Tools            1")
}

func stringsJoinLines(lines ...string) string {
	var out string
	for i, line := range lines {
		if i != 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
