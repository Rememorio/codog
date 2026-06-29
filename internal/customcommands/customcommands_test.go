package customcommands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFindAndRenderCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "commands", "review.md"), []byte("User review $ARGUMENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "fix.md"), []byte("Claude fix {{args}}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "audit.md"), []byte("Team audit $ARGUMENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "fix.md"), []byte("Codog fix {{ ARGUMENTS }}"), 0o644))

	commands, err := Load(configHome, workspace)
	require.NoError(t, err)
	require.Len(t, commands, 4)
	require.Contains(t, commandNames(commands), "team:audit")

	command, err := Find(configHome, workspace, "/fix")
	require.NoError(t, err)
	require.Equal(t, "workspace", command.Source)
	rendered := Render(command, "bug 123")
	require.Equal(t, "Codog fix bug 123", rendered.Rendered)

	command, err = Find(configHome, workspace, "review")
	require.NoError(t, err)
	require.Equal(t, "user", command.Source)
	require.Equal(t, "User review file.go", Render(command, "file.go").Rendered)

	command, err = Find(configHome, workspace, "/team/audit")
	require.NoError(t, err)
	require.Equal(t, "team:audit", command.Name)
	require.Equal(t, "claude", command.Source)
	require.Equal(t, "Team audit security", Render(command, "security").Rendered)
}

func commandNames(commands []Command) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	return names
}
