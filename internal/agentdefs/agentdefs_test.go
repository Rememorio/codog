package agentdefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAgentDefinitions(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewer.json"), []byte(`{"description":"reviews code","tools":["grep"]}`), 0o644))
	claudeDir := filepath.Join(workspace, ".claude", "agents")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "planner.md"), []byte(`---
name: planner
description: Plans implementation work
model: openai/gpt-4.1-mini
tools:
  - read_file
  - grep
---
Use focused plans and call out validation.
`), 0o644))
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "extra"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"demo","name":"demo","agents":["./extra/critic.md"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "agents", "helper.json"), []byte(`{"description":"plugin helper","tools":["read_file"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "extra", "critic.md"), []byte(`---
description: plugin markdown critic
tools: grep
---
# Critic
Review with care.
`), 0o644))

	defs, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, defs, 4)
	byName := map[string]Definition{}
	for _, def := range defs {
		byName[def.Name] = def
	}
	require.Equal(t, "plugin:demo", byName["demo:critic"].Source)
	require.Equal(t, "demo", byName["demo:critic"].Plugin)
	require.Equal(t, "markdown", byName["demo:critic"].Format)
	require.Equal(t, []string{"grep"}, byName["demo:critic"].Tools)
	require.Contains(t, byName["demo:critic"].Prompt, "Review with care.")
	require.Equal(t, "plugin:demo", byName["demo:helper"].Source)
	require.Equal(t, "demo", byName["demo:helper"].Plugin)
	require.Equal(t, "json", byName["demo:helper"].Format)
	require.Equal(t, []string{"read_file"}, byName["demo:helper"].Tools)
	require.Equal(t, "claude", byName["planner"].Source)
	require.Equal(t, "markdown", byName["planner"].Format)
	require.Equal(t, "openai/gpt-4.1-mini", byName["planner"].Model)
	require.Equal(t, []string{"read_file", "grep"}, byName["planner"].Tools)
	require.Equal(t, "reviewer", byName["reviewer"].Name)
	require.Equal(t, "workspace", byName["reviewer"].Source)
	require.Equal(t, []string{"grep"}, byName["reviewer"].Tools)
	require.Equal(t, []string{".json", ".md", ".markdown"}, AcceptedFormats())
}
