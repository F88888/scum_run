package updater

import (
	"runtime"
	"syscall"
)

// getSysProcAttr 获取平台特定的进程属性
func getSysProcAttr() *syscall.SysProcAttr {
	if runtime.GOOS == "windows" {
		return &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		}
	}
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
