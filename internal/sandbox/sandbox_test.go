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

func TestDetectForReportsContainerAndStrategyDetails(t *testing.T) {
	status := DetectFor(DetectionInputs{
		OS:                 "linux",
		Env:                []string{"container=docker", "EMPTY="},
		DockerEnvExists:    true,
		ContainerEnvExists: false,
		Proc1Cgroup:        "12:memory:/kubepods.slice/docker/abc",
		CommandAvailable: func(name string) bool {
			return name == "unshare"
		},
		UserNamespaceWorks: func() bool { return true },
	})

	require.Equal(t, "linux", status.OS)
	require.True(t, status.Available)
	require.Equal(t, "unshare", status.Default)
	require.Equal(t, []string{"unshare"}, status.Strategies)
	require.True(t, status.NamespaceSupported)
	require.True(t, status.NetworkSupported)
	require.True(t, status.Container.InContainer)
	require.Contains(t, status.Container.Markers, "/.dockerenv")
	require.Contains(t, status.Container.Markers, "/proc/1/cgroup:docker")
	require.Contains(t, status.Container.Markers, "/proc/1/cgroup:kubepods")
	require.Contains(t, status.Container.Markers, "env:container=docker")
	require.Contains(t, status.StrategyStatuses, StrategyStatus{Name: "bwrap", Available: false, Reason: "command not found"})
	require.Contains(t, status.StrategyStatuses, StrategyStatus{Name: "unshare", Available: true})
}

func TestDetectForReportsFallbackReasons(t *testing.T) {
	status := DetectFor(DetectionInputs{
		OS:               "linux",
		CommandAvailable: func(string) bool { return false },
	})

	require.False(t, status.Available)
	require.Empty(t, status.Default)
	require.Contains(t, status.FallbackReason, "bwrap: command not found")
	require.Contains(t, status.FallbackReason, "unshare: command not found")
	require.False(t, status.NamespaceSupported)
}

func TestResolveStrategyReportForDetectAndUnavailable(t *testing.T) {
	available := Status{
		Available:        true,
		Default:          "bwrap",
		StrategyStatuses: []StrategyStatus{{Name: "bwrap", Available: true}},
	}
	resolution := ResolveStrategyReportFor("detect", available)
	require.Equal(t, "enabled", resolution.Status)
	require.Equal(t, "bwrap", resolution.Effective)
	require.True(t, resolution.Enabled)

	unavailable := Status{
		FallbackReason:   "bwrap: command not found",
		StrategyStatuses: []StrategyStatus{{Name: "bwrap", Reason: "command not found"}},
	}
	resolution = ResolveStrategyReportFor("detect", unavailable)
	require.Equal(t, "fallback", resolution.Status)
	require.Equal(t, "bwrap: command not found", resolution.FallbackReason)
	require.False(t, resolution.Enabled)

	resolution = ResolveStrategyReportFor("bwrap", unavailable)
	require.Equal(t, "unavailable", resolution.Status)
	require.Contains(t, resolution.Error, "not available")
	require.Contains(t, resolution.FallbackReason, "command not found")
}
