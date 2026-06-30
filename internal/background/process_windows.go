//go:build windows

package background

import (
	"os"
	"os/exec"
)

func configureBackgroundCommand(cmd *exec.Cmd) {}

func killBackgroundProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process != nil
}
