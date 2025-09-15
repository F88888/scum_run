//go:build !windows

package updater

import "syscall"

// getSysProcAttr 获取Unix平台（包括macOS和Linux）特定的进程属性
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}
