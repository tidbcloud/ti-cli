package fswrap

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func interruptProcess(process *os.Process, _ os.Signal) error {
	// CTRL_BREAK can target a process group and Go delivers it as os.Interrupt.
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid))
}
