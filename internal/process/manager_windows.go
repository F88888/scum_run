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
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
)

const (
	CTRL_C_EVENT          = 0
	CTRL_BREAK_EVENT      = 1
	ATTACH_PARENT_PROCESS = 0xFFFFFFFF
)

// sendCtrlC 向指定进程发送Ctrl+C信号
// @description: 使用Windows API发送CTRL_C_EVENT信号，这是SCUM服务器优雅关闭的正确方式
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlC(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d)", pid)

	// 注意：scum_run 主程序已经在启动时通过 SetConsoleCtrlHandler(NULL, TRUE) 禁用了 Ctrl+C
	// 所以这里不需要再临时禁用，直接发送信号即可
	// 即使 scum_run 收到信号，也会被忽略

	// 尝试附加到目标进程的控制台
	ret, _, err := procAttachConsole.Call(uintptr(pid))
	if ret == 0 {
		// 如果无法附加（进程可能没有控制台），尝试其他方法
		m.logger.Warn("Failed to attach to console of PID %d: %v", pid, err)
		return fmt.Errorf("cannot attach to process console: %v", err)
	}

	// 等待一小段时间确保附加完成
	time.Sleep(100 * time.Millisecond)

	// 发送Ctrl+C事件
	// 重要：第二个参数使用进程ID（PID）而不是0
	// 使用0会发送给所有共享控制台的进程组，可能导致scum_run也收到信号
	// 使用PID确保只发送给目标进程组
	// 即使 scum_run 收到信号，主程序已经禁用了 Ctrl+C 处理，所以不会退出
	ret, _, err = procGenerateConsoleCtrlEvent.Call(CTRL_C_EVENT, uintptr(pid))

	// 立即释放控制台，防止影响主程序
	procFreeConsole.Call()

	if ret == 0 {
		m.logger.Error("Failed to send Ctrl+C event: %v", err)
		return fmt.Errorf("failed to generate Ctrl+C event: %v", err)
	}

	m.logger.Info("Successfully sent Ctrl+C to process (PID: %d)", pid)
	return nil
}

// sendCtrlCViaCreateProcess 使用helper进程发送Ctrl+C (备用方案)
// @description: 如果直接发送失败，使用辅助进程的方式发送Ctrl+C
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlCViaCreateProcess(pid int) error {
	// 这是一个备用方案，使用外部工具
	// 可以考虑使用 windows-kill 等工具或者编写一个简单的C helper
	m.logger.Warn("Alternative Ctrl+C sending method not implemented yet")
	return fmt.Errorf("alternative method not available")
}
