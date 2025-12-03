//go:build windows
// +build windows

package process

import (
	"fmt"
	"os/exec"
	"syscall"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
)

const (
	CTRL_C_EVENT          = 0
	CTRL_BREAK_EVENT      = 1
	ATTACH_PARENT_PROCESS = 0xFFFFFFFF
)

// sendCtrlC 向指定进程发送Ctrl+C信号
// @description: 使用独立的辅助进程发送CTRL_C_EVENT信号，完全隔离scum_run，防止自己退出
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlC(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d) via helper process", pid)

	// 使用独立的 PowerShell 进程发送 Ctrl+C
	// 这样 scum_run 完全不会附加到目标控制台，不会收到任何信号
	err := m.sendCtrlCViaHelperProcess(pid)
	if err != nil {
		m.logger.Warn("Helper process method failed: %v, trying CTRL_BREAK_EVENT", err)
		// 备用方案：使用 CTRL_BREAK_EVENT
		return m.sendCtrlBreak(pid)
	}

	return nil
}

// sendCtrlCViaHelperProcess 使用独立的辅助进程发送Ctrl+C
// @description: 通过PowerShell脚本在独立进程中发送Ctrl+C，scum_run完全不会受到影响
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlCViaHelperProcess(pid int) error {
	// 使用 PowerShell 脚本作为独立辅助进程发送 Ctrl+C
	// 这个脚本会在完全独立的进程中执行，scum_run 不会附加到任何控制台
	// 注意：PowerShell 中 $pid 是内置变量，使用 $targetPid 代替
	psScript := fmt.Sprintf(`
$ErrorActionPreference = "Stop"
$targetPid = %d

# 定义 Windows API
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public class ConsoleCtrl {
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool AttachConsole(uint dwProcessId);
    
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool FreeConsole();
    
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool SetConsoleCtrlHandler(IntPtr handler, bool add);
    
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);
}
"@

try {
    # 设置忽略 Ctrl+C（在 PowerShell 进程中）
    [ConsoleCtrl]::SetConsoleCtrlHandler([IntPtr]::Zero, $true) | Out-Null
    
    # 附加到目标进程的控制台
    $attached = [ConsoleCtrl]::AttachConsole($targetPid)
    if (-not $attached) {
        $errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
        throw "Failed to attach console, error: $errorCode"
    }
    
    # 发送 Ctrl+C 事件（0 = CTRL_C_EVENT, 使用 targetPid 作为进程组ID）
    $sent = [ConsoleCtrl]::GenerateConsoleCtrlEvent(0, $targetPid)
    
    # 立即释放控制台
    [ConsoleCtrl]::FreeConsole() | Out-Null
    
    if (-not $sent) {
        $errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
        throw "Failed to send Ctrl+C, error: $errorCode"
    }
    
    Write-Host "SUCCESS: Sent Ctrl+C to process $targetPid"
    exit 0
} catch {
    Write-Error $_.Exception.Message
    try { [ConsoleCtrl]::FreeConsole() | Out-Null } catch {}
    exit 1
}
`, pid)

	// 执行 PowerShell 脚本
	// 使用 -NoProfile 加速启动，-NonInteractive 禁止交互
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)

	// 重要：隐藏 PowerShell 窗口
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.Warn("PowerShell helper failed: %v, output: %s", err, string(output))
		return fmt.Errorf("helper process failed: %v, output: %s", err, string(output))
	}

	m.logger.Info("Successfully sent Ctrl+C to process (PID: %d) via helper process: %s", pid, string(output))
	return nil
}

// sendCtrlBreak 发送 CTRL_BREAK_EVENT 信号
// @description: 备用方案，使用CTRL_BREAK_EVENT信号，可以发送给特定进程组
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlBreak(pid int) error {
	m.logger.Info("Sending CTRL_BREAK to process (PID: %d)", pid)

	// CTRL_BREAK_EVENT 可以直接发送给特定的进程组，不需要附加控制台
	// 使用 CREATE_NEW_PROCESS_GROUP 启动的进程，其进程组ID等于PID
	ret, _, err := procGenerateConsoleCtrlEvent.Call(CTRL_BREAK_EVENT, uintptr(pid))
	if ret == 0 {
		m.logger.Error("Failed to send CTRL_BREAK event: %v", err)
		return fmt.Errorf("failed to generate CTRL_BREAK event: %v", err)
	}

	m.logger.Info("Successfully sent CTRL_BREAK to process (PID: %d)", pid)
	return nil
}
