package sandbox

import (
	"os/exec"
	"runtime"
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

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
