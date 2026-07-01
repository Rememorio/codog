package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StrategyStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type ContainerStatus struct {
	InContainer bool     `json:"in_container"`
	Markers     []string `json:"markers,omitempty"`
}

type Status struct {
	OS                 string           `json:"os"`
	Strategies         []string         `json:"strategies"`
	Default            string           `json:"default"`
	Available          bool             `json:"available"`
	StrategyStatuses   []StrategyStatus `json:"strategy_statuses,omitempty"`
	Container          ContainerStatus  `json:"container"`
	NamespaceSupported bool             `json:"namespace_supported"`
	NetworkSupported   bool             `json:"network_supported"`
	FallbackReason     string           `json:"fallback_reason,omitempty"`
}

type StrategyResolution struct {
	Configured     string `json:"configured"`
	Effective      string `json:"effective,omitempty"`
	Enabled        bool   `json:"enabled"`
	Available      bool   `json:"available"`
	Status         string `json:"status"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	Error          string `json:"error,omitempty"`
}

type DetectionInputs struct {
	OS                 string
	Env                []string
	DockerEnvExists    bool
	ContainerEnvExists bool
	Proc1Cgroup        string
	CommandAvailable   func(string) bool
	UserNamespaceWorks func() bool
}

func Detect() Status {
	proc1Cgroup, _ := os.ReadFile("/proc/1/cgroup")
	return DetectFor(DetectionInputs{
		OS:                 runtime.GOOS,
		Env:                os.Environ(),
		DockerEnvExists:    fileExists("/.dockerenv"),
		ContainerEnvExists: fileExists("/run/.containerenv"),
		Proc1Cgroup:        string(proc1Cgroup),
		CommandAvailable:   commandExists,
		UserNamespaceWorks: unshareUserNamespaceWorks,
	})
}

func DetectFor(inputs DetectionInputs) Status {
	goos := strings.TrimSpace(inputs.OS)
	if goos == "" {
		goos = runtime.GOOS
	}
	if inputs.CommandAvailable == nil {
		inputs.CommandAvailable = commandExists
	}
	status := Status{OS: goos, Container: detectContainer(inputs)}
	candidates := candidateStrategies(goos)
	reasons := []string{}
	for _, candidate := range candidates {
		probe := probeStrategy(candidate, inputs)
		status.StrategyStatuses = append(status.StrategyStatuses, probe)
		if probe.Available {
			status.Strategies = append(status.Strategies, candidate)
			if status.Default == "" {
				status.Default = candidate
			}
			status.Available = true
			continue
		}
		if probe.Reason != "" {
			reasons = append(reasons, candidate+": "+probe.Reason)
		}
	}
	status.NamespaceSupported = strategyAvailable(status.StrategyStatuses, "unshare")
	status.NetworkSupported = status.NamespaceSupported
	if !status.Available {
		if len(reasons) == 0 {
			reasons = append(reasons, "no supported sandbox strategy detected for "+goos)
		}
		status.FallbackReason = strings.Join(reasons, "; ")
	}
	return status
}

func candidateStrategies(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"sandbox-exec"}
	case "linux":
		return []string{"bwrap", "unshare"}
	case "windows":
		return []string{"restricted-token"}
	default:
		return nil
	}
}

func probeStrategy(name string, inputs DetectionInputs) StrategyStatus {
	switch name {
	case "restricted-token":
		missing := []string{}
		if !commandAvailableAny(inputs.CommandAvailable, "runas.exe", "runas") {
			missing = append(missing, "runas.exe")
		}
		if !commandAvailableAny(inputs.CommandAvailable, windowsShellCandidates()...) {
			missing = append(missing, "powershell.exe")
		}
		if len(missing) != 0 {
			return StrategyStatus{Name: name, Available: false, Reason: "command not found: " + strings.Join(missing, ", ")}
		}
		return StrategyStatus{Name: name, Available: true}
	case "unshare":
		if !inputs.CommandAvailable("unshare") {
			return StrategyStatus{Name: name, Available: false, Reason: "command not found"}
		}
		if inputs.UserNamespaceWorks != nil && !inputs.UserNamespaceWorks() {
			return StrategyStatus{Name: name, Available: false, Reason: "user namespaces are unavailable"}
		}
		return StrategyStatus{Name: name, Available: true}
	default:
		if inputs.CommandAvailable(name) {
			return StrategyStatus{Name: name, Available: true}
		}
		return StrategyStatus{Name: name, Available: false, Reason: "command not found"}
	}
}

func strategyAvailable(strategies []StrategyStatus, name string) bool {
	for _, strategy := range strategies {
		if strategy.Name == name {
			return strategy.Available
		}
	}
	return false
}

func commandAvailableAny(commandAvailable func(string) bool, candidates ...string) bool {
	for _, candidate := range candidates {
		if commandAvailable(candidate) {
			return true
		}
	}
	return false
}

func ResolveStrategyReport(strategy string) StrategyResolution {
	return ResolveStrategyReportFor(strategy, Detect())
}

func ResolveStrategyReportFor(strategy string, status Status) StrategyResolution {
	configured := strings.TrimSpace(strategy)
	switch configured {
	case "", "off", "none":
		return StrategyResolution{Configured: configured, Status: "disabled"}
	case "detect":
		if !status.Available {
			return StrategyResolution{
				Configured:     configured,
				Status:         "fallback",
				FallbackReason: status.FallbackReason,
			}
		}
		return StrategyResolution{
			Configured: configured,
			Effective:  status.Default,
			Enabled:    true,
			Available:  true,
			Status:     "enabled",
		}
	default:
		for _, item := range status.StrategyStatuses {
			if item.Name != configured {
				continue
			}
			if item.Available {
				return StrategyResolution{
					Configured: configured,
					Effective:  configured,
					Enabled:    true,
					Available:  true,
					Status:     "enabled",
				}
			}
			message := fmt.Sprintf("sandbox strategy %q is not available", configured)
			if item.Reason != "" {
				message += ": " + item.Reason
			}
			return StrategyResolution{
				Configured:     configured,
				Status:         "unavailable",
				FallbackReason: item.Reason,
				Error:          message,
			}
		}
		return StrategyResolution{
			Configured: configured,
			Status:     "unavailable",
			Error:      fmt.Sprintf("sandbox strategy %q is not available: unsupported strategy", configured),
		}
	}
}

func ShellCommand(strategy, workspace, command string) (string, []string, string, error) {
	effective, err := ResolveStrategy(strategy)
	if err != nil {
		return "", nil, "", err
	}
	if effective == "" {
		return "sh", []string{"-lc", command}, "", nil
	}
	name, args, err := BuildShellCommand(effective, workspace, command)
	return name, args, effective, err
}

func ResolveStrategy(strategy string) (string, error) {
	resolution := ResolveStrategyReport(strategy)
	if resolution.Error != "" {
		return "", fmt.Errorf("%s", resolution.Error)
	}
	return resolution.Effective, nil
}

func BuildShellCommand(strategy, workspace, command string) (string, []string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, err
	}
	switch strategy {
	case "sandbox-exec":
		return "sandbox-exec", []string{"-p", macOSSandboxProfile(absWorkspace), "sh", "-lc", command}, nil
	case "bwrap":
		return "bwrap", []string{
			"--ro-bind", "/", "/",
			"--bind", absWorkspace, absWorkspace,
			"--dev", "/dev",
			"--proc", "/proc",
			"--tmpfs", "/tmp",
			"--chdir", absWorkspace,
			"sh", "-lc", command,
		}, nil
	case "unshare":
		return "unshare", []string{"-Urn", "sh", "-lc", command}, nil
	case "restricted-token":
		return BuildWindowsRestrictedTokenCommand(absWorkspace, command, "")
	default:
		return "", nil, fmt.Errorf("unsupported sandbox strategy %q", strategy)
	}
}

func BuildWindowsRestrictedTokenCommand(workspace, command, shell string) (string, []string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, err
	}
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = defaultWindowsShell()
	}
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$env:CODOG_SANDBOX_STRATEGY = 'restricted-token'",
		"$env:CODOG_SANDBOX_WORKSPACE = " + powerShellLiteral(absWorkspace),
		"Set-Location -LiteralPath " + powerShellLiteral(absWorkspace),
		"& $env:ComSpec /d /s /c " + powerShellLiteral(command),
		"exit $LASTEXITCODE",
	}, "; ")
	commandLine := windowsCommandLine(shell, []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		script,
	})
	return "runas.exe", []string{"/trustlevel:0x20000", commandLine}, nil
}

func defaultWindowsShell() string {
	for _, candidate := range windowsShellCandidates() {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return "powershell.exe"
}

func windowsShellCandidates() []string {
	return []string{"powershell.exe", "powershell", "pwsh.exe", "pwsh"}
}

func powerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsCommandLine(program string, args []string) string {
	parts := []string{windowsCommandLineArg(program)}
	for _, arg := range args {
		parts = append(parts, windowsCommandLineArg(arg))
	}
	return strings.Join(parts, " ")
}

func windowsCommandLineArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}
	var builder strings.Builder
	builder.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			builder.WriteString(strings.Repeat(`\`, backslashes*2+1))
			builder.WriteRune(r)
			backslashes = 0
		default:
			if backslashes > 0 {
				builder.WriteString(strings.Repeat(`\`, backslashes))
				backslashes = 0
			}
			builder.WriteRune(r)
		}
	}
	if backslashes > 0 {
		builder.WriteString(strings.Repeat(`\`, backslashes*2))
	}
	builder.WriteByte('"')
	return builder.String()
}

func macOSSandboxProfile(workspace string) string {
	quotedWorkspace := strconv.Quote(workspace)
	return strings.Join([]string{
		"(version 1)",
		"(deny default)",
		"(allow process*)",
		"(allow signal (target same-sandbox))",
		"(allow sysctl-read)",
		"(allow mach-lookup)",
		"(allow network*)",
		"(allow file-read*)",
		"(allow file-write* (subpath " + quotedWorkspace + "))",
		"(allow file-write* (subpath \"/tmp\"))",
		"(allow file-write* (subpath \"/private/tmp\"))",
	}, "\n")
}

func detectContainer(inputs DetectionInputs) ContainerStatus {
	markers := []string{}
	if inputs.DockerEnvExists {
		markers = append(markers, "/.dockerenv")
	}
	if inputs.ContainerEnvExists {
		markers = append(markers, "/run/.containerenv")
	}
	for _, item := range inputs.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "container", "docker", "podman", "kubernetes_service_host":
			markers = append(markers, "env:"+key+"="+value)
		}
	}
	for _, needle := range []string{"docker", "containerd", "kubepods", "podman", "libpod"} {
		if strings.Contains(inputs.Proc1Cgroup, needle) {
			markers = append(markers, "/proc/1/cgroup:"+needle)
		}
	}
	markers = dedupeSorted(markers)
	return ContainerStatus{InContainer: len(markers) > 0, Markers: markers}
}

func dedupeSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

var (
	unshareProbeOnce sync.Once
	unshareProbeOK   bool
)

func unshareUserNamespaceWorks() bool {
	unshareProbeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "true")
		err := cmd.Run()
		unshareProbeOK = err == nil
	})
	return unshareProbeOK
}
