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

// initPlatformSignalHandling 初始化平台特定的信号处理
// @description: 在 Windows 上完全禁用 Ctrl+C 和 Ctrl+Break 信号，防止 scum_run 退出
// 使用 SetConsoleCtrlHandler(NULL, TRUE) 来禁用默认的 Ctrl+C 处理
func initPlatformSignalHandling() {
	// SetConsoleCtrlHandler(NULL, TRUE) 会禁用默认的 Ctrl+C 处理
	// 参数1: NULL (0) 表示不使用自定义处理器，而是禁用默认处理
	// 参数2: TRUE (1) 表示添加（禁用），FALSE (0) 表示移除（启用）
	// 返回值: 非零表示成功，零表示失败
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 1)
	if ret == 0 {
		// 如果设置失败，记录错误但继续运行
		// 这种情况很少发生，通常是权限问题或没有控制台
		// 注意：如果 scum_run 作为服务运行或没有控制台，这个调用可能会失败
		// 但程序仍然可以继续运行
		_ = err
	} else {
		// 成功设置，scum_run 将完全忽略 Ctrl+C 和 Ctrl+Break 信号
		// 即使收到这些信号，程序也不会退出
	}
}
