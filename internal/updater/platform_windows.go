//go:build windows

package updater

import "syscall"

// getSysProcAttr 获取Windows平台特定的进程属性
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | syscall.CREATE_NEW_CONSOLE,
	}
}
