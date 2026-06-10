//go:build !windows
// +build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup detaches a child process into its own Unix process group.
// cmd is the command being started, and the function returns no values because the setting is applied directly to cmd.
func (m *Manager) configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// sendCtrlC reports that Ctrl+C console events are unsupported on non-Windows systems.
// pid identifies the target process for logging context, and the function returns an unsupported error.
func (m *Manager) sendCtrlC(pid int) error {
	return fmt.Errorf("Ctrl+C console event is only supported on Windows (pid: %d)", pid)
}

// isProcessRunning checks whether a process still accepts signal zero.
// process is the OS process handle, and the function returns true when the process appears alive.
func isProcessRunning(process *os.Process) bool {
	if process == nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// signalProcess sends a signal to a detached process group when possible.
// process is the target process and signal is the Unix signal to send; the function returns any delivery error.
func signalProcess(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return fmt.Errorf("process is nil")
	}
	if err := syscall.Kill(-process.Pid, signal); err == nil {
		return nil
	}
	return process.Signal(signal)
}

// killProcess forcefully kills a detached process group when possible.
// process is the target process, and the function returns any delivery error.
func killProcess(process *os.Process) error {
	if process == nil {
		return fmt.Errorf("process is nil")
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return process.Kill()
}
