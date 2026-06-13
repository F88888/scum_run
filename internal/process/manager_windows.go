//go:build windows
// +build windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
	"golang.org/x/sys/windows"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const (
	CTRL_C_EVENT     = 0
	CTRL_BREAK_EVENT = 1
)

// configureProcessGroup starts a child process outside the scum_run task tree while preserving targeted Ctrl+Break handling.
// cmd is the command being started, and the function returns a cleanup callback that releases any temporary parent-process handle.
func (m *Manager) configureProcessGroup(cmd *exec.Cmd) func() {
	attr := &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	parentHandle, parentName, err := preferredDetachedParentProcess()
	if err != nil {
		m.logger.Warn("Starting server with scum_run as Windows parent because detached parent lookup failed: %v", err)
	} else if parentHandle != 0 {
		attr.ParentProcess = parentHandle
		m.logger.Info("Starting server with Windows parent process %s to avoid coupling it to the scum_run task tree", parentName)
	}
	cmd.SysProcAttr = attr
	m.logger.Info("Process will be created in new process group for Ctrl+C handling")
	return func() {
		if parentHandle != 0 {
			if err := windows.CloseHandle(windows.Handle(parentHandle)); err != nil {
				m.logger.Warn("Failed to close detached parent process handle: %v", err)
			}
		}
	}
}

// isProcessRunning checks whether a Windows process handle is present.
// process is the OS process handle, and the function returns true when the handle is non-nil.
func isProcessRunning(process *os.Process) bool {
	return process != nil
}

// signalProcess sends a best-effort signal to a Windows process.
// process is the target process and signal is the requested signal; the function returns any delivery error.
func signalProcess(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return fmt.Errorf("process is nil")
	}
	return process.Signal(signal)
}

// killProcess forcefully kills a Windows process.
// process is the target process, and the function returns any delivery error.
func killProcess(process *os.Process) error {
	if process == nil {
		return fmt.Errorf("process is nil")
	}
	return process.Kill()
}

// sendCtrlC 向指定进程发送Ctrl+C信号
// @description: 使用Windows API发送CTRL_C_EVENT或CTRL_BREAK_EVENT信号
//
//	scum_run 在启动时已经通过 SetConsoleCtrlHandler(NULL, TRUE) 禁用了 Ctrl+C 处理
//	所以即使发送 Ctrl+C，scum_run 也不会退出
//
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlC(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d)", pid)

	// 只向 SCUM 服务所在的新进程组发送 CTRL_BREAK_EVENT。
	// 不再使用 AttachConsole + CTRL_C_EVENT 兜底，避免把控制事件广播到
	// scum_run/scum_client 所在控制台，导致客户端进程一起退出。
	m.logger.Info("Attempting to send CTRL_BREAK_EVENT to process group (PID: %d)", pid)
	ret, _, err := procGenerateConsoleCtrlEvent.Call(CTRL_BREAK_EVENT, uintptr(pid))
	if ret != 0 {
		m.logger.Info("Successfully sent CTRL_BREAK_EVENT to process (PID: %d)", pid)
		return nil
	}
	m.logger.Warn("Failed to send CTRL_BREAK_EVENT to process group %d: %v", pid, err)
	return fmt.Errorf("failed to generate CTRL_BREAK_EVENT: %v", err)
}

// preferredDetachedParentProcess opens a stable interactive Windows parent process for SCUM.
// It does not take parameters, and it returns a process handle plus display name, or an error when no suitable parent can be opened.
func preferredDetachedParentProcess() (syscall.Handle, string, error) {
	processes, err := gopsprocess.Processes()
	if err != nil {
		return 0, "", fmt.Errorf("list processes: %w", err)
	}
	for _, candidate := range processes {
		name, err := candidate.Name()
		if err != nil || !strings.EqualFold(strings.TrimSpace(name), "explorer.exe") {
			continue
		}
		handle, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE, false, uint32(candidate.Pid))
		if err != nil {
			continue
		}
		return syscall.Handle(handle), fmt.Sprintf("%s:%d", name, candidate.Pid), nil
	}
	return 0, "", fmt.Errorf("explorer.exe with process-create and duplicate-handle access was not found")
}
