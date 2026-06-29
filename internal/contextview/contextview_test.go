package contextview

import (
	"bytes"
	"testing"

	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/memory"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestBuildAndRenderText(t *testing.T) {
	status := localstatus.Build(localstatus.Options{
		Workspace:       "/repo",
		Model:           "claude-test",
		PermissionMode:  "workspace-write",
		MaxTokens:       4096,
		MaxTurns:        8,
		AuthConfigured:  true,
		ToolNames:       []string{"bash", "read_file"},
		SessionID:       "session-1",
		SessionMessages: 2,
		GitStatus:       "## main\n M notes.md\n",
	})
	report := Build(Options{
		Status: status,
		Memory: memory.Report{
			InstructionFiles: 1,
			Files: []memory.Summary{{
				Path:    "/repo/AGENTS.md",
				Name:    "AGENTS.md",
				Lines:   2,
				Chars:   18,
				Preview: "Prefer tests.",
			}},
		},
		Focus: focus.Report{
			Total:   1,
			Entries: []focus.Entry{{Path: "notes.md", Kind: "file", Exists: true, Lines: 1, Chars: 8}},
		},
		TokenUsage:   usage.Summary{InputTokens: 3, OutputTokens: 2, TotalTokens: 5, EstimatedUSD: 0.00002},
		SystemPrompt: "line one\nline two",
	})

	require.Equal(t, "context", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 18, report.Memory.TotalChars)
	require.Equal(t, 1, report.Focus.FocusedPaths)
	require.Equal(t, 2, report.Prompt.Lines)
	require.Contains(t, report.Signals, "git working tree has local changes")

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Context")
	require.Contains(t, out.String(), "Memory files     1")
	require.Contains(t, out.String(), "Focused paths    1")
	require.Contains(t, out.String(), "notes.md")
}

func TestBuildSignalsMissingContextInputs(t *testing.T) {
	status := localstatus.Build(localstatus.Options{Workspace: "/repo"})
	report := Build(Options{Status: status})

	require.Contains(t, report.Signals, "auth is not configured")
	require.Contains(t, report.Signals, "no project memory files loaded")
	require.Contains(t, report.Signals, "no focused paths selected")
	require.Contains(t, report.Signals, "no active session selected")
}
