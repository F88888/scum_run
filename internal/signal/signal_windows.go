//go:build windows
// +build windows

package signal

import (
	"syscall"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCtrlHandler = kernel32.NewProc("SetConsoleCtrlHandler")
)

// DisableCtrlC 禁用 Ctrl+C 信号处理
// @description: 在程序启动时调用，永久禁用 Ctrl+C 处理，防止 scum_run 因任何 Ctrl+C 信号而退出
// @return: error 错误信息
func DisableCtrlC() error {
	// SetConsoleCtrlHandler(NULL, TRUE) 会禁用默认的 Ctrl+C 处理
	// 第一个参数为 0 (NULL) 表示使用默认处理器
	// 第二个参数为 1 (TRUE) 表示忽略 Ctrl+C 信号
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 1)
	if ret == 0 {
		return err
	}
	return nil
}

// EnableCtrlC 启用 Ctrl+C 信号处理（一般不需要调用）
// @description: 恢复 Ctrl+C 处理，一般不需要调用
// @return: error 错误信息
func EnableCtrlC() error {
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 0)
	if ret == 0 {
		return err
	}
	return nil
}
