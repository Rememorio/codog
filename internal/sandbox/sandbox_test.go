package sandbox

import (
	"path/filepath"
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

func TestBuildShellCommandRestrictedToken(t *testing.T) {
	workspace := t.TempDir()
	name, args, err := BuildWindowsRestrictedTokenCommand(workspace, "echo 'hi'", "powershell.exe")
	require.NoError(t, err)
	require.Equal(t, "runas.exe", name)
	require.Len(t, args, 2)
	require.Equal(t, "/trustlevel:0x20000", args[0])
	require.Contains(t, args[1], "powershell.exe")
	require.Contains(t, args[1], "CODOG_SANDBOX_STRATEGY")
	require.Contains(t, args[1], "restricted-token")
	require.Contains(t, args[1], "Set-Location -LiteralPath")
	require.Contains(t, args[1], workspace)
	require.Contains(t, args[1], "echo ''hi''")
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

func TestDetectForWindowsRestrictedToken(t *testing.T) {
	status := DetectFor(DetectionInputs{
		OS: "windows",
		CommandAvailable: func(name string) bool {
			return name == "runas.exe" || name == "powershell.exe"
		},
	})

	require.Equal(t, "windows", status.OS)
	require.True(t, status.Available)
	require.Equal(t, "restricted-token", status.Default)
	require.Equal(t, []string{"restricted-token"}, status.Strategies)
	require.Contains(t, status.StrategyStatuses, StrategyStatus{Name: "restricted-token", Available: true})
	require.False(t, status.NamespaceSupported)
	require.False(t, status.NetworkSupported)
}

func TestDetectForWindowsRestrictedTokenReportsMissingDependencies(t *testing.T) {
	status := DetectFor(DetectionInputs{
		OS:               "windows",
		CommandAvailable: func(string) bool { return false },
	})

	require.False(t, status.Available)
	require.Empty(t, status.Default)
	require.Contains(t, status.FallbackReason, "restricted-token: command not found")
	require.Contains(t, status.FallbackReason, "runas.exe")
	require.Contains(t, status.FallbackReason, "powershell.exe")
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

func TestResolveSandboxExecutionStatusForRequest(t *testing.T) {
	workspace := t.TempDir()
	network := true
	mode := FilesystemIsolationAllowList
	status, effective, err := ResolveSandboxExecutionStatusFor("detect", workspace, SandboxRequestOptions{
		NetworkIsolation: &network,
		FilesystemMode:   mode,
		AllowedMounts:    []string{"logs", "tmp/cache"},
	}, Status{
		Available: true,
		Default:   "bwrap",
		StrategyStatuses: []StrategyStatus{
			{Name: "bwrap", Available: true},
		},
		Container: ContainerStatus{InContainer: true, Markers: []string{"/.dockerenv"}},
	})

	require.NoError(t, err)
	require.Equal(t, "bwrap", effective)
	require.True(t, status.Enabled)
	require.True(t, status.Active)
	require.True(t, status.Supported)
	require.True(t, status.NamespaceActive)
	require.True(t, status.NetworkActive)
	require.True(t, status.FilesystemActive)
	require.Equal(t, "allow-list", status.FilesystemMode)
	require.Equal(t, FilesystemIsolationAllowList, status.Requested.FilesystemMode)
	require.Contains(t, status.AllowedMounts, filepath.Join(workspace, "logs"))
	require.Contains(t, status.AllowedMounts, filepath.Join(workspace, "tmp/cache"))
	require.True(t, status.InContainer)
	require.Contains(t, status.ContainerMarkers, "/.dockerenv")

	name, args, err := BuildShellCommandWithStatus(effective, workspace, "printf hi", status)
	require.NoError(t, err)
	require.Equal(t, "bwrap", name)
	require.Contains(t, args, "--unshare-net")
	require.Contains(t, args, filepath.Join(workspace, "logs"))
}

func TestResolveSandboxExecutionStatusForDisabledRequest(t *testing.T) {
	enabled := false
	status, effective, err := ResolveSandboxExecutionStatusFor("codog-missing-sandbox", t.TempDir(), SandboxRequestOptions{
		Enabled: &enabled,
	}, Status{})

	require.NoError(t, err)
	require.Empty(t, effective)
	require.False(t, status.Enabled)
	require.False(t, status.Active)
	require.True(t, status.Supported)
	require.Equal(t, "disabled", status.ResolutionStatus)
	require.Equal(t, "codog-missing-sandbox", status.ConfiguredStrategy)
	require.True(t, status.Requested.NamespaceRestrictions)
}

func TestResolveSandboxExecutionStatusReportsUnsupportedCapabilities(t *testing.T) {
	network := true
	status, effective, err := ResolveSandboxExecutionStatusFor("restricted-token", t.TempDir(), SandboxRequestOptions{
		NetworkIsolation: &network,
	}, Status{
		Available: true,
		Default:   "restricted-token",
		StrategyStatuses: []StrategyStatus{
			{Name: "restricted-token", Available: true},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "restricted-token", effective)
	require.False(t, status.Active)
	require.False(t, status.Supported)
	require.False(t, status.NetworkActive)
	require.Contains(t, status.FallbackReason, "network isolation unavailable")
	require.Contains(t, status.FallbackReason, "namespace isolation unavailable")
}
