package sandbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellCommandOff(t *testing.T) {
	name, args, effective, err := ShellCommand("off", t.TempDir(), "echo hi")
	require.NoError(t, err)
	require.Equal(t, "sh", name)
	require.Equal(t, []string{"-lc", "echo hi"}, args)
	require.Empty(t, effective)
}

func TestBuildShellCommandSandboxExec(t *testing.T) {
	workspace := t.TempDir()
	name, args, err := BuildShellCommand("sandbox-exec", workspace, "pwd")
	require.NoError(t, err)
	require.Equal(t, "sandbox-exec", name)
	require.Contains(t, args[1], "(deny default)")
	require.Contains(t, args[1], workspace)
	require.Equal(t, []string{"sh", "-lc", "pwd"}, args[2:])
}

func TestBuildShellCommandBwrap(t *testing.T) {
	workspace := t.TempDir()
	name, args, err := BuildShellCommand("bwrap", workspace, "pwd")
	require.NoError(t, err)
	require.Equal(t, "bwrap", name)
	require.Contains(t, args, "--ro-bind")
	require.Contains(t, args, "--bind")
	require.Contains(t, args, workspace)
	require.Equal(t, []string{"sh", "-lc", "pwd"}, args[len(args)-3:])
}

func TestBuildShellCommandUnshare(t *testing.T) {
	name, args, err := BuildShellCommand("unshare", t.TempDir(), "pwd")
	require.NoError(t, err)
	require.Equal(t, "unshare", name)
	require.Equal(t, []string{"-Urn", "sh", "-lc", "pwd"}, args)
}
