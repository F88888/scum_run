//go:build !windows
// +build !windows

package main

// initPlatformSignalHandling 初始化平台特定的信号处理
// @description: 非 Windows 平台的空实现
func initPlatformSignalHandling() {
	// 在非 Windows 平台上不需要特殊处理
}

