//go:build windows
// +build windows

package process

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
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
// @description: 尝试使用辅助进程发送CTRL_C_EVENT信号，如果失败则使用改进的直接方法
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlC(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d)", pid)

	// 首先尝试使用辅助进程发送 Ctrl+C，完全隔离 scum_run
	err := m.sendCtrlCViaHelperProcess(pid)
	if err == nil {
		return nil
	}

	// 如果辅助进程方法失败（例如无法附加控制台），使用改进的直接方法
	m.logger.Info("Helper process method failed, trying improved direct method: %v", err)
	return m.sendCtrlCImproved(pid)
}

// sendCtrlCViaHelperProcess 使用辅助进程发送Ctrl+C
// @description: 通过PowerShell脚本使用辅助进程发送Ctrl+C，完全隔离scum_run
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlCViaHelperProcess(pid int) error {
	// 使用 PowerShell 脚本作为辅助进程发送 Ctrl+C
	// 这个脚本会在独立的进程中执行，不会影响 scum_run
	// 注意：不能使用 $pid 作为变量名，因为它是 PowerShell 的内置只读变量
	psScript := fmt.Sprintf(`
		$ErrorActionPreference = "Stop"
		$targetPid = %d
		
		# 加载 Windows API（只需要发送 Ctrl+C 所需的 API）
		$signature = @"
			using System;
			using System.Runtime.InteropServices;
			
			public class Win32Utils {
				[DllImport("kernel32.dll", SetLastError=true)]
				public static extern bool AttachConsole(uint dwProcessId);
				
				[DllImport("kernel32.dll", SetLastError=true)]
				public static extern bool FreeConsole();
				
				[DllImport("kernel32.dll", SetLastError=true)]
				public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);
			}
"@
		
		# 使用 -TypeDefinition 时不需要 -Name 参数，因为类名已经在类型定义中
		$type = Add-Type -TypeDefinition $signature -PassThru
		
		try {
			# 附加到目标进程的控制台
			if (-not $type::AttachConsole($targetPid)) {
				$errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
				throw "Failed to attach to console: $errorCode"
			}
			
			# 等待一小段时间确保附加完成
			Start-Sleep -Milliseconds 100
			
			# 发送 Ctrl+C 事件到指定的进程组（使用 targetPid 作为进程组ID）
			if (-not $type::GenerateConsoleCtrlEvent(0, $targetPid)) {
				$errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
				$type::FreeConsole()
				throw "Failed to send Ctrl+C: $errorCode"
			}
			
			# 释放控制台
			$type::FreeConsole()
			
			Write-Host "Successfully sent Ctrl+C to process $targetPid"
			exit 0
		} catch {
			Write-Error $_.Exception.Message
			try {
				$type::FreeConsole()
			} catch {
				# 忽略释放控制台时的错误
			}
			exit 1
		}
	`, pid)

	// 执行 PowerShell 脚本
	cmd := exec.Command("powershell", "-Command", psScript)
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.Warn("Failed to send Ctrl+C via helper process: %v, output: %s", err, string(output))
		return fmt.Errorf("helper process failed: %v, output: %s", err, string(output))
	}

	m.logger.Info("Successfully sent Ctrl+C to process (PID: %d) via helper process", pid)
	return nil
}

// sendCtrlCImproved 改进的直接发送Ctrl+C方法
// @description: 使用改进的方法直接发送Ctrl+C，通过设置处理器和快速释放控制台来保护scum_run
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlCImproved(pid int) error {
	m.logger.Info("Using improved direct method to send Ctrl+C to process (PID: %d)", pid)

	// 1. 首先设置 scum_run 忽略 Ctrl+C 信号
	// 设置为 true 表示忽略 Ctrl+C 事件
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 1)
	if ret == 0 {
		m.logger.Warn("Failed to disable Ctrl+C handler: %v", err)
		// 即使设置失败，也继续尝试，因为可能不会收到信号
	} else {
		m.logger.Info("Successfully disabled Ctrl+C handler for scum_run")
	}

	// 确保在函数返回前恢复处理器
	defer func() {
		procSetConsoleCtrlHandler.Call(0, 0)
		m.logger.Info("Restored Ctrl+C handler for scum_run")
	}()

	// 2. 尝试附加到目标进程的控制台
	// 如果目标进程没有控制台（使用管道重定向），这会失败
	ret, _, err = procAttachConsole.Call(uintptr(pid))
	if ret == 0 {
		// 无法附加控制台，可能是目标进程没有控制台窗口
		// 这种情况下，我们无法发送 Ctrl+C，返回错误让调用者使用其他方法
		m.logger.Warn("Cannot attach to console of PID %d (process may not have console): %v", pid, err)
		return fmt.Errorf("cannot attach to process console (process may not have console window): %v", err)
	}

	// 3. 立即发送 Ctrl+C 事件，使用进程组 ID（PID）而不是 0
	// 使用 PID 确保只发送给目标进程组
	m.logger.Info("Sending Ctrl+C event to process group %d", pid)
	ret, _, err = procGenerateConsoleCtrlEvent.Call(CTRL_C_EVENT, uintptr(pid))

	// 4. 立即释放控制台，防止 scum_run 继续附加到目标控制台
	procFreeConsole.Call()
	m.logger.Info("Released console attachment")

	if ret == 0 {
		m.logger.Error("Failed to send Ctrl+C event: %v", err)
		return fmt.Errorf("failed to generate Ctrl+C event: %v", err)
	}

	m.logger.Info("Successfully sent Ctrl+C to process (PID: %d) using improved direct method", pid)
	return nil
}
