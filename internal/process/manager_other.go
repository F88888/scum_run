//go:build !windows
// +build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func (m *Manager) configureProcessGroup(cmd *exec.Cmd) {
}

func (m *Manager) sendCtrlC(pid int) error {
	return fmt.Errorf("Ctrl+C console event is only supported on Windows (pid: %d)", pid)
}

func isProcessRunning(process *os.Process) bool {
	if process == nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
