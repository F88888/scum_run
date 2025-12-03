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

// 控制台事件类型
const (
	CTRL_C_EVENT        = 0
	CTRL_BREAK_EVENT    = 1
	CTRL_CLOSE_EVENT    = 2
	CTRL_LOGOFF_EVENT   = 5
	CTRL_SHUTDOWN_EVENT = 6
)

// consoleCtrlHandler 自定义控制台处理器
// @description: 返回 1 (TRUE) 表示已处理事件，系统不应该调用默认处理器
// @param: ctrlType uintptr 控制台事件类型
// @return: uintptr 1 表示已处理，0 表示未处理
func consoleCtrlHandler(ctrlType uintptr) uintptr {
	// 忽略 CTRL+C 和 CTRL+BREAK 事件
	// 返回 1 (TRUE) 表示我们已经处理了这个事件
	// 这样 scum_run 就不会因为这些信号而退出
	switch ctrlType {
	case CTRL_C_EVENT, CTRL_BREAK_EVENT:
		// 忽略这些事件，返回 TRUE
		return 1
	case CTRL_CLOSE_EVENT, CTRL_LOGOFF_EVENT, CTRL_SHUTDOWN_EVENT:
		// 对于关闭、注销、关机事件，返回 FALSE 让系统处理
		// 这样用户手动关闭控制台窗口时程序可以正常退出
		return 0
	}
	return 0
}

// handlerCallback 保存回调函数指针，防止被垃圾回收
var handlerCallback uintptr

// DisableCtrlC 禁用 Ctrl+C 和 Ctrl+Break 信号处理
// @description: 在程序启动时调用，注册自定义处理器来忽略 CTRL+C 和 CTRL+BREAK
//
//	防止 scum_run 因任何控制台信号而退出
//
// @return: error 错误信息
func DisableCtrlC() error {
	// 创建回调函数
	// syscall.NewCallback 将 Go 函数转换为 Windows 回调函数
	handlerCallback = syscall.NewCallback(consoleCtrlHandler)

	// 注册自定义控制台处理器
	// SetConsoleCtrlHandler(HandlerRoutine, TRUE) 添加处理器
	ret, _, err := procSetConsoleCtrlHandler.Call(handlerCallback, 1)
	if ret == 0 {
		return err
	}
	return nil
}

// EnableCtrlC 启用 Ctrl+C 信号处理（一般不需要调用）
// @description: 移除自定义处理器，恢复默认行为
// @return: error 错误信息
func EnableCtrlC() error {
	if handlerCallback == 0 {
		return nil
	}
	// 移除自定义处理器
	ret, _, err := procSetConsoleCtrlHandler.Call(handlerCallback, 0)
	if ret == 0 {
		return err
	}
	return nil
}
