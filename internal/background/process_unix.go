//go:build !windows

package background

import (
	"os"
	"os/exec"
	"syscall"
)

func configureBackgroundCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killBackgroundProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			process, findErr := os.FindProcess(pid)
			if findErr != nil {
				return nil
			}
			if killErr := process.Kill(); killErr != nil && killErr != os.ErrProcessDone {
				return killErr
			}
			return nil
		}
		return err
	}
	return nil
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
