package status

import (
	"bytes"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/stretchr/testify/require"
)

func TestBuildParsesGitStatus(t *testing.T) {
	snapshot := Build(Options{
		Version:        "test-version",
		Workspace:      "/repo/codog",
		Model:          "claude-test",
		PermissionMode: "workspace-write",
		AuthConfigured: true,
		PlanActive:     true,
		PlanText:       "inspect first",
		PlanUpdatedAt:  "2026-01-01T00:00:00Z",
		MemoryFiles: []MemoryFileStatus{{
			Path:  "/repo/codog/AGENTS.md",
			Name:  "AGENTS.md",
			Scope: "/repo/codog",
			Chars: 18,
		}},
		ToolNames: []string{"bash", "read_file"},
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
	require.Equal(t, "codog", snapshot.Workspace.Name)
	require.Equal(t, 1, snapshot.Workspace.MemoryFileCount)
	require.Equal(t, "AGENTS.md", snapshot.Workspace.MemoryFiles[0].Name)
	require.Equal(t, "main", snapshot.Git.Branch)
	require.False(t, snapshot.Git.Clean)
	require.Equal(t, 1, snapshot.Git.Staged)
	require.Equal(t, 1, snapshot.Git.Unstaged)
	require.Equal(t, 1, snapshot.Git.Untracked)
	require.Equal(t, 1, snapshot.Git.Conflicts)
	require.Equal(t, 2, snapshot.Tools.Count)
	require.True(t, snapshot.Plan.Active)
	require.Equal(t, "inspect first", snapshot.Plan.Text)
	require.True(t, snapshot.LaneBoard.StatusJSONSupported)
	require.Equal(t, background.LaneFreshnessTransportDead, snapshot.LaneBoard.FreshnessStates[2])
	require.True(t, snapshot.LaneBoard.Available)
	require.Equal(t, 1, snapshot.LaneBoard.ActiveCount)
	require.Equal(t, "task-1", snapshot.LaneBoard.Active[0].TaskID)
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
	require.Equal(t, 2, snapshot.Git.Freshness.Behind)

	var out bytes.Buffer
	RenderText(&out, snapshot)
	require.Contains(t, out.String(), "Git freshness    status=stale base=main ahead=0 behind=2")
}

func TestBuildMarksInvalidValidationDegraded(t *testing.T) {
	index := 0
	snapshot := Build(Options{
		Version:   "test-version",
		GitStatus: "## main",
		MCPValidation: MCPValidationStatus{
			TotalConfigured: 1,
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
	require.Contains(t, out.String(), "MCP validation   valid=0 invalid=1")
	require.Contains(t, out.String(), "Hook validation  valid=1 invalid=1")
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

func TestBuildParsesInitialBranch(t *testing.T) {
	snapshot := Build(Options{GitStatus: "## No commits yet on main"})

	require.Equal(t, "main", snapshot.Git.Branch)
	require.True(t, snapshot.Git.Clean)
}

func TestRenderText(t *testing.T) {
	snapshot := Build(Options{
		Version:         "test-version",
		Workspace:       "/repo/codog",
		Model:           "claude-test",
		PermissionMode:  "read-only",
		AuthConfigured:  true,
		SessionID:       "session-1",
		SessionMessages: 3,
		ToolNames:       []string{"bash"},
		LaneBoard:       &background.LaneBoard{},
		GitStatus:       "## main",
		SandboxDefault:  "sandbox-exec",
	})

	var out bytes.Buffer
	RenderText(&out, snapshot)

	require.Contains(t, out.String(), "Status")
	require.Contains(t, out.String(), "Version          test-version")
	require.Contains(t, out.String(), "Memory files     0")
	require.Contains(t, out.String(), "Plan             inactive")
	require.Contains(t, out.String(), "Session          session-1")
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
