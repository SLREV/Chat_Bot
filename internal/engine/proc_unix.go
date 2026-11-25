//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group so a cancelled turn
// takes down the whole CLI tree, not just the wrapper.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid = the process group.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
