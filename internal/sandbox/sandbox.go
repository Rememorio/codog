package sandbox

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Status struct {
	OS         string   `json:"os"`
	Strategies []string `json:"strategies"`
	Default    string   `json:"default"`
	Available  bool     `json:"available"`
}

func Detect() Status {
	status := Status{OS: runtime.GOOS}
	switch runtime.GOOS {
	case "darwin":
		status.Strategies = append(status.Strategies, "sandbox-exec")
		status.Default = "sandbox-exec"
		status.Available = commandExists("sandbox-exec")
	case "linux":
		for _, candidate := range []string{"bwrap", "unshare"} {
			if commandExists(candidate) {
				status.Strategies = append(status.Strategies, candidate)
			}
		}
		if len(status.Strategies) != 0 {
			status.Default = status.Strategies[0]
			status.Available = true
		}
	case "windows":
		status.Strategies = append(status.Strategies, "restricted-token")
		status.Default = "restricted-token"
		status.Available = false
	}
	return status
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
	strategy = strings.TrimSpace(strategy)
	switch strategy {
	case "", "off", "none":
		return "", nil
	case "detect":
		status := Detect()
		if !status.Available {
			return "", nil
		}
		return status.Default, nil
	}
	if !commandExists(strategy) {
		return "", fmt.Errorf("sandbox strategy %q is not available", strategy)
	}
	return strategy, nil
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
	default:
		return "", nil, fmt.Errorf("unsupported sandbox strategy %q", strategy)
	}
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

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
