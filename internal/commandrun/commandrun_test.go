package commandrun

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCapturesSuccessfulCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX echo")
	}
	result, err := Run(context.Background(), Options{
		Workspace: t.TempDir(),
		Command:   []string{"sh", "-c", "printf hello"},
		Kind:      "run",
	})
	require.NoError(t, err)
	require.Equal(t, "run", result.Kind)
	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, "hello", result.Stdout)

	var out bytes.Buffer
	RenderText(&out, result)
	require.Contains(t, out.String(), "Command")
	require.Contains(t, out.String(), "stdout:")
	require.Contains(t, out.String(), "hello")
}

func TestRunCapturesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	result, err := Run(context.Background(), Options{
		Workspace: t.TempDir(),
		Command:   []string{"sh", "-c", "echo nope >&2; exit 7"},
		Kind:      "run",
	})
	require.NoError(t, err)
	require.Equal(t, 7, result.ExitCode)
	require.Contains(t, result.Stderr, "nope")
}

func TestRunTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	result, err := Run(context.Background(), Options{
		Workspace: t.TempDir(),
		Command:   []string{"sh", "-c", "sleep 1"},
		Timeout:   time.Millisecond,
	})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Equal(t, -1, result.ExitCode)
}
