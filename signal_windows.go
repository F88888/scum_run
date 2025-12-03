//go:build windows
// +build windows

package main

import (
	"syscall"
)

var (
	kernel32Windows           = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCtrlHandler = kernel32Windows.NewProc("SetConsoleCtrlHandler")
)

// ctrlHandler 是 Windows 控制台控制事件处理器
// @description: 返回 1 (TRUE) 表示已处理该信号，系统不应调用默认处理器
// @param: ctrlType uint32 控制事件类型
// @return: uintptr 1 表示已处理，0 表示未处理
func ctrlHandler(ctrlType uint32) uintptr {
	// CTRL_C_EVENT = 0
	// CTRL_BREAK_EVENT = 1
	// CTRL_CLOSE_EVENT = 2
	// CTRL_LOGOFF_EVENT = 5
	// CTRL_SHUTDOWN_EVENT = 6

	// 对于 CTRL_C_EVENT 和 CTRL_BREAK_EVENT，返回 TRUE 表示忽略
	// 这样 scum_run 就不会因为这些信号而退出
	if ctrlType == 0 || ctrlType == 1 {
		// 忽略 Ctrl+C 和 Ctrl+Break
		return 1
	}

	// 对于其他事件（如关闭、注销、关机），让系统处理
	return 0
}

// initPlatformSignalHandling 初始化平台特定的信号处理
// @description: 在 Windows 上设置控制台控制处理器，忽略 Ctrl+C 和 Ctrl+Break 信号
func initPlatformSignalHandling() {
	// 使用 syscall.NewCallback 创建 Windows 回调函数
	// 这个处理器会拦截 Ctrl+C 和 Ctrl+Break 信号，防止 scum_run 退出
	handlerPtr := syscall.NewCallback(ctrlHandler)

	// SetConsoleCtrlHandler(HandlerRoutine, Add)
	// HandlerRoutine: 处理器函数指针
	// Add: TRUE (1) 表示添加处理器，FALSE (0) 表示移除
	ret, _, _ := procSetConsoleCtrlHandler.Call(handlerPtr, 1)
	if ret == 0 {
		// 如果设置失败，程序继续运行，但可能会受到 Ctrl+C 影响
		// 这种情况很少发生
	}
}

