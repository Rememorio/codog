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
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "extra"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "commands", "review.md"), []byte(`---
description: Review a file.
allowed-tools: Read, Bash(go test:*)
argument-hint: FILE
arguments: [file, focus]
---
User review $file / $focus / $0 / $ARGUMENTS[1] / $ARGUMENTS / $filename`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "fix.md"), []byte("Claude fix {{args}}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "audit.md"), []byte("Team audit $ARGUMENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "fix.md"), []byte("Codog fix {{ ARGUMENTS }}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "plugin.json"), []byte(`{"id":"demo","name":"demo","commands":["./extra/ops.md"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "commands", "deploy.md"), []byte(`---
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/*)
---
Deploy ${CLAUDE_PLUGIN_ROOT} ${CLAUDE_PLUGIN_DATA} $ARGUMENTS`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "extra", "ops.md"), []byte("Ops $ARGUMENTS"), 0o644))

	commands, err := Load(configHome, workspace)
	require.NoError(t, err)
	require.Len(t, commands, 6)
	require.Contains(t, commandNames(commands), "team:audit")
	require.Contains(t, commandNames(commands), "demo:deploy")
	require.Contains(t, commandNames(commands), "demo:ops")

	command, err := Find(configHome, workspace, "/fix")
	require.NoError(t, err)
	require.Equal(t, "workspace", command.Source)
	rendered := Render(command, "bug 123")
	require.Equal(t, "Codog fix bug 123", rendered.Rendered)

	command, err = Find(configHome, workspace, "review")
	require.NoError(t, err)
	require.Equal(t, "user", command.Source)
	require.Equal(t, "Review a file.", command.Description)
	require.Equal(t, "Review a file.", command.Preview)
	require.Equal(t, []string{"Read", "Bash(go test:*)"}, command.AllowedTools)
	require.Equal(t, "FILE", command.ArgumentHint)
	require.Equal(t, []string{"file", "focus"}, command.Arguments)
	require.NotContains(t, command.Body, "allowed-tools")
	rendered = Render(command, `file.go "race condition"`)
	require.Equal(t, `User review file.go / race condition / file.go / race condition / file.go "race condition" / $filename`, rendered.Rendered)
	require.Equal(t, "Review a file.", rendered.Description)
	require.Equal(t, []string{"Read", "Bash(go test:*)"}, rendered.AllowedTools)

	command, err = Find(configHome, workspace, "/team/audit")
	require.NoError(t, err)
	require.Equal(t, "team:audit", command.Name)
	require.Equal(t, "claude", command.Source)
	require.Equal(t, "Team audit security", Render(command, "security").Rendered)

	command, err = Find(configHome, workspace, "demo:deploy")
	require.NoError(t, err)
	require.Equal(t, "plugin:demo", command.Source)
	pluginRoot := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugins", "demo"))
	pluginData := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo"))
	require.Equal(t, pluginRoot, command.PluginRoot)
	require.Equal(t, pluginData, command.PluginData)
	require.Equal(t, []string{"Bash(" + pluginRoot + "/bin/*)"}, command.AllowedTools)
	rendered = Render(command, "prod")
	require.Equal(t, pluginRoot, rendered.PluginRoot)
	require.Equal(t, pluginData, rendered.PluginData)
	require.Equal(t, "Deploy "+pluginRoot+" "+pluginData+" prod", rendered.Rendered)

	command, err = Find(configHome, workspace, "demo:ops")
	require.NoError(t, err)
	require.Equal(t, "plugin:demo", command.Source)
	require.Equal(t, "Ops prod", Render(command, "prod").Rendered)
}

func TestRenderWithSessionSubstitutesSessionID(t *testing.T) {
	command := Command{Body: "session=${CLAUDE_SESSION_ID} args=$ARGUMENTS"}

	require.Equal(t, "session=${CLAUDE_SESSION_ID} args=target", Render(command, "target").Rendered)
	require.Equal(t, "session=session-123 args=target", RenderWithSession(command, "target", "session-123").Rendered)
}

func commandNames(commands []Command) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	return names
}
