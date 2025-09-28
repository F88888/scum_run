//go:build windows

package updater

import "syscall"

// getSysProcAttr 获取Windows平台特定的进程属性
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    false, // 显示更新器窗口，让用户能看到更新进度
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
