package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestRenderWelcomeUsesWideWordmarkAndRuntimeMetadata(t *testing.T) {
	view := renderWelcome(welcomeInfo{
		Version:    "0.1.1",
		Model:      "openai/glm52",
		Permission: "workspace-write",
		Workspace:  "/workspace/codog",
		GitBranch:  "main",
	}, 100, stylesForTheme("no-color"))

	require.Contains(t, view, "____")
	require.Contains(t, view, "Codog 0.1.1")
	require.Contains(t, view, "model       openai/glm52")
	require.Contains(t, view, "permission  workspace-write")
	require.Contains(t, view, "workspace   /workspace/codog · main")
	require.Len(t, strings.Split(view, "\n"), len(welcomeLogo))
	assertWelcomeWidth(t, view, 100)
}

func TestRenderWelcomeStacksAtMediumWidth(t *testing.T) {
	view := renderWelcome(welcomeInfo{
		Version:    "0.1.1",
		Model:      "glm52",
		Permission: "prompt",
		Workspace:  "/workspace/codog",
	}, 50, stylesForTheme("no-color"))

	require.Contains(t, view, welcomeLogo[1])
	require.Contains(t, view, "Codog 0.1.1")
	require.Greater(t, len(strings.Split(view, "\n")), len(welcomeLogo))
	assertWelcomeWidth(t, view, 50)
}

func TestRenderWelcomeCompactsForNarrowTerminals(t *testing.T) {
	view := renderWelcome(welcomeInfo{
		Version:    "0.1.1",
		Model:      "model-with-a-very-long-name",
		Permission: "workspace-write",
		Workspace:  "/workspace/codog",
		GitBranch:  "feature/long-branch-name",
	}, 30, stylesForTheme("no-color"))

	require.Contains(t, view, "Codog 0.1.1")
	require.NotContains(t, view, "____")
	require.Contains(t, view, "model ·")
	require.Contains(t, view, "permission ·")
	assertWelcomeWidth(t, view, 30)
}

func TestDisplayWelcomePathUsesHomeRelativePath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	require.Equal(t, "~/work/codog", displayWelcomePath(filepath.Join(home, "work", "codog")))
}

func assertWelcomeWidth(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), width, line)
	}
}
