package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunLifecycleExecutesCommandsInPluginRoot(t *testing.T) {
	root := t.TempDir()
	manifest := Manifest{
		ID:      "demo",
		Root:    root,
		Enabled: true,
		Lifecycle: LifecycleConfig{
			Init: []string{"echo plugin-ok > marker.txt"},
		},
	}

	result := RunLifecycle(context.Background(), manifest, "init", 5*time.Second)

	require.Equal(t, "ok", result.Status)
	require.Equal(t, "demo", result.PluginID)
	require.Equal(t, "init", result.Phase)
	require.Len(t, result.Commands, 1)
	require.Equal(t, 0, result.Commands[0].ExitCode)
	data, err := os.ReadFile(filepath.Join(root, "marker.txt"))
	require.NoError(t, err)
	require.Contains(t, string(data), "plugin-ok")
}

func TestRunLifecycleSkipsDisabledPlugin(t *testing.T) {
	result := RunLifecycle(context.Background(), Manifest{
		ID:        "demo",
		Root:      t.TempDir(),
		Enabled:   false,
		Lifecycle: LifecycleConfig{Init: []string{"echo should-not-run"}},
	}, "init", 5*time.Second)

	require.Equal(t, "skipped", result.Status)
	require.Equal(t, "plugin is disabled", result.Message)
	require.Empty(t, result.Commands)
}

func TestRunLifecycleReportsCommandFailure(t *testing.T) {
	result := RunLifecycle(context.Background(), Manifest{
		ID:        "demo",
		Root:      t.TempDir(),
		Enabled:   true,
		Lifecycle: LifecycleConfig{Init: []string{"exit 9"}},
	}, "init", 5*time.Second)

	require.Equal(t, "failed", result.Status)
	require.Len(t, result.Commands, 1)
	require.Equal(t, "failed", result.Commands[0].Status)
	require.Equal(t, 9, result.Commands[0].ExitCode)
	require.NotEmpty(t, result.Commands[0].Error)
}
