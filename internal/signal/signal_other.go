//go:build !windows
// +build !windows

package signal

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
