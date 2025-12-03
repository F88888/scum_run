//go:build windows
// +build windows

package process

import (
	"fmt"
	"syscall"
	"time"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
)

const (
	CTRL_C_EVENT          = 0
	CTRL_BREAK_EVENT      = 1
	ATTACH_PARENT_PROCESS = 0xFFFFFFFF
)

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

	// 方法1：优先使用 CTRL_BREAK_EVENT
	// CTRL_BREAK_EVENT 可以直接发送给特定进程组，不需要附加控制台
	// 使用 CREATE_NEW_PROCESS_GROUP 启动的进程，其进程组ID等于PID
	m.logger.Info("Attempting to send CTRL_BREAK_EVENT to process group (PID: %d)", pid)
	ret, _, err := procGenerateConsoleCtrlEvent.Call(CTRL_BREAK_EVENT, uintptr(pid))
	if ret != 0 {
		m.logger.Info("Successfully sent CTRL_BREAK_EVENT to process (PID: %d)", pid)
		return nil
	}
	m.logger.Warn("Failed to send CTRL_BREAK_EVENT: %v, trying CTRL_C_EVENT with AttachConsole", err)

	// 方法2：如果 CTRL_BREAK_EVENT 失败，使用 AttachConsole + CTRL_C_EVENT
	// 由于 scum_run 已经在启动时禁用了 Ctrl+C 处理，即使附加到同一控制台也不会退出
	ret, _, err = procAttachConsole.Call(uintptr(pid))
	if ret == 0 {
		m.logger.Warn("Failed to attach to console of PID %d: %v", pid, err)
		return fmt.Errorf("cannot attach to process console: %v", err)
	}

	// 等待一小段时间确保附加完成
	time.Sleep(100 * time.Millisecond)

	// 发送 CTRL_C_EVENT
	// 第二个参数使用 0 发送给当前控制台的所有进程
	// 但由于 scum_run 已经禁用了 Ctrl+C 处理，它不会退出
	ret, _, err = procGenerateConsoleCtrlEvent.Call(CTRL_C_EVENT, 0)

	// 立即释放控制台
	procFreeConsole.Call()

	if ret == 0 {
		m.logger.Error("Failed to send CTRL_C event: %v", err)
		return fmt.Errorf("failed to generate CTRL_C event: %v", err)
	}

	m.logger.Info("Successfully sent CTRL_C to process (PID: %d)", pid)
	return nil
}
