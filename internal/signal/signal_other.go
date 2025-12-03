//go:build !windows
// +build !windows

package signal

// ShutdownCallback 关闭回调函数类型
type ShutdownCallback func()

// SetShutdownCallback 设置关闭回调函数（非 Windows 平台为空操作）
// @description: 在非 Windows 平台上通过 signal.Notify 处理
// @param: callback ShutdownCallback 回调函数
func SetShutdownCallback(callback ShutdownCallback) {
	// 非 Windows 平台通过 signal.Notify 处理
}

// DisableCtrlC 禁用 Ctrl+C 信号处理（非 Windows 平台为空操作）
// @description: 在非 Windows 平台上不需要禁用 Ctrl+C
// @return: error 错误信息
func DisableCtrlC() error {
	// 非 Windows 平台不需要特殊处理
	return nil
}

// EnableCtrlC 启用 Ctrl+C 信号处理（非 Windows 平台为空操作）
// @description: 在非 Windows 平台上不需要启用 Ctrl+C
// @return: error 错误信息
func EnableCtrlC() error {
	// 非 Windows 平台不需要特殊处理
	return nil
}
