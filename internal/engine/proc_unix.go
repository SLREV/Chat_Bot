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

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid = the process group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
