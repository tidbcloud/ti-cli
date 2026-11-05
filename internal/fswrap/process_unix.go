//go:build !windows

package fswrap

import (
	"os"
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd) {
	// Terminal signals reach ti; ti forwards each one exactly once. A separate
	// group prevents a single Ctrl-C from also interrupting the child directly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interruptProcess(process *os.Process, sig os.Signal) error {
	return process.Signal(sig)
}
