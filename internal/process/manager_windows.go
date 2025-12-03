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
// @description: 使用辅助进程发送CTRL_C_EVENT信号，完全隔离scum_run主程序，避免收到信号
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlC(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d) using helper process", pid)

	// 使用辅助进程发送 Ctrl+C，完全隔离 scum_run
	// 这样可以避免 scum_run 附加到目标进程的控制台，从而避免收到信号
	return m.sendCtrlCViaHelperProcess(pid)
}

// sendCtrlCViaHelperProcess 使用辅助进程发送Ctrl+C
// @description: 通过PowerShell脚本使用辅助进程发送Ctrl+C，完全隔离scum_run
// @param: pid int 目标进程ID
// @return: error 错误信息
func (m *Manager) sendCtrlCViaHelperProcess(pid int) error {
	// 使用 PowerShell 脚本作为辅助进程发送 Ctrl+C
	// 这个脚本会在独立的进程中执行，不会影响 scum_run
	psScript := fmt.Sprintf(`
		$ErrorActionPreference = "Stop"
		$pid = %d
		
		# 加载 Windows API
		$signature = @"
			[DllImport("kernel32.dll", SetLastError=true)]
			public static extern bool AttachConsole(uint dwProcessId);
			
			[DllImport("kernel32.dll", SetLastError=true)]
			public static extern bool FreeConsole();
			
			[DllImport("kernel32.dll", SetLastError=true)]
			public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);
			
			[DllImport("kernel32.dll", SetLastError=true)]
			public static extern bool SetConsoleCtrlHandler([IntPtr] HandlerRoutine, bool Add);
"@
		
		$type = Add-Type -MemberDefinition $signature -Name Win32Utils -Namespace Console -PassThru
		
		try {
			# 附加到目标进程的控制台
			if (-not $type::AttachConsole($pid)) {
				$errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
				throw "Failed to attach to console: $errorCode"
			}
			
			# 等待一小段时间确保附加完成
			Start-Sleep -Milliseconds 100
			
			# 发送 Ctrl+C 事件到指定的进程组（PID）
			if (-not $type::GenerateConsoleCtrlEvent(0, $pid)) {
				$errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
				$type::FreeConsole()
				throw "Failed to send Ctrl+C: $errorCode"
			}
			
			# 释放控制台
			$type::FreeConsole()
			
			Write-Host "Successfully sent Ctrl+C to process $pid"
			exit 0
		} catch {
			Write-Error $_.Exception.Message
			if ($type::FreeConsole) {
				$type::FreeConsole()
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

// sendCtrlCDirect 直接发送Ctrl+C（已弃用，保留作为参考）
// @description: 直接使用Windows API发送Ctrl+C，但会导致scum_run也收到信号
// @param: pid int 目标进程ID
// @return: error 错误信息
// 注意：此方法已弃用，因为AttachConsole会导致scum_run也附加到目标控制台，从而收到信号
func (m *Manager) sendCtrlCDirect(pid int) error {
	m.logger.Info("Sending Ctrl+C to process (PID: %d) - DEPRECATED METHOD", pid)

	// 临时禁用scum_run主程序的Ctrl+C处理器，避免自己收到信号
	// 设置为true表示忽略Ctrl+C事件
	ret, _, err := procSetConsoleCtrlHandler.Call(0, 1)
	if ret == 0 {
		m.logger.Warn("Failed to disable Ctrl+C handler: %v", err)
	}
	defer func() {
		// 恢复Ctrl+C处理器
		procSetConsoleCtrlHandler.Call(0, 0)
	}()

	// 尝试附加到目标进程的控制台
	// 注意：这会导致scum_run也附加到目标控制台，可能收到信号
	ret, _, err = procAttachConsole.Call(uintptr(pid))
	if ret == 0 {
		m.logger.Warn("Failed to attach to console of PID %d: %v", pid, err)
		return fmt.Errorf("cannot attach to process console: %v", err)
	}

	// 等待一小段时间确保附加完成
	time.Sleep(100 * time.Millisecond)

	// 发送Ctrl+C事件
	ret, _, err = procGenerateConsoleCtrlEvent.Call(CTRL_C_EVENT, uintptr(pid))

	// 立即释放控制台，防止影响主程序
	procFreeConsole.Call()

	if ret == 0 {
		m.logger.Error("Failed to send Ctrl+C event: %v", err)
		return fmt.Errorf("failed to generate Ctrl+C event: %v", err)
	}

	m.logger.Info("Successfully sent Ctrl+C to process (PID: %d)", pid)
	return nil
}
