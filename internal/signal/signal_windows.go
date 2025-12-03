//go:build windows
// +build windows

package signal

import (
	"os"
	"syscall"
	"time"
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

// ShutdownCallback 关闭回调函数类型
// @description: 当控制台关闭时调用，用于优雅关闭 SCUM 服务器
type ShutdownCallback func()

// shutdownCallback 保存关闭回调函数
var shutdownCallback ShutdownCallback

// SetShutdownCallback 设置关闭回调函数
// @description: 设置当控制台关闭时调用的回调函数
// @param: callback ShutdownCallback 回调函数
func SetShutdownCallback(callback ShutdownCallback) {
	shutdownCallback = callback
}

// consoleCtrlHandler 自定义控制台处理器
// @description: 返回 1 (TRUE) 表示已处理事件，系统不应该调用默认处理器
// @param: ctrlType uintptr 控制台事件类型
// @return: uintptr 1 表示已处理，0 表示未处理
func consoleCtrlHandler(ctrlType uintptr) uintptr {
	switch ctrlType {
	case CTRL_C_EVENT, CTRL_BREAK_EVENT:
		// 忽略 CTRL+C 和 CTRL+BREAK 事件
		// 返回 1 (TRUE) 表示已处理，scum_run 不会退出
		return 1

	case CTRL_CLOSE_EVENT, CTRL_LOGOFF_EVENT, CTRL_SHUTDOWN_EVENT:
		// 控制台关闭、注销、关机事件
		// 先调用关闭回调，让 SCUM 服务器优雅关闭
		if shutdownCallback != nil {
			shutdownCallback()
			// 给 SCUM 服务器一些时间来保存数据
			time.Sleep(5 * time.Second)
		}
		// 返回 0 让程序退出，或者直接调用 os.Exit
		os.Exit(0)
		return 0
	}
	return 0
}

// handlerCallback 保存回调函数指针，防止被垃圾回收
var handlerCallback uintptr

// DisableCtrlC 禁用 Ctrl+C 和 Ctrl+Break 信号处理
// @description: 在程序启动时调用，注册自定义处理器来忽略 CTRL+C 和 CTRL+BREAK
//
//	同时处理控制台关闭事件，确保 SCUM 服务器可以优雅关闭
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
