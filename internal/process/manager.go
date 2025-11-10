package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	_const "scum_run/internal/const"
	"scum_run/internal/logger"
	"scum_run/internal/network"
	"scum_run/model"
)

// OutputCallback is a function type for handling real-time output
type OutputCallback func(source string, line string)

// Manager manages the SCUM server process
type Manager struct {
	config         *model.ServerConfig
	logger         *logger.Logger
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	mutex          sync.Mutex
	outputCallback OutputCallback
}

// New creates a new process manager
func New(execPath string, logger *logger.Logger) *Manager {
	return &Manager{
		config: &model.ServerConfig{
			ExecPath:       execPath,
			GamePort:       _const.DefaultGamePort,
			MaxPlayers:     _const.DefaultMaxPlayers,
			EnableBattlEye: _const.DefaultEnableBattlEye,
			ServerIP:       _const.DefaultServerIP,
			AdditionalArgs: _const.DefaultAdditionalArgs,
		},
		logger: logger,
	}
}

// NewWithConfig creates a new process manager with configuration
func NewWithConfig(config *model.ServerConfig, logger *logger.Logger) *Manager {
	return &Manager{
		config: config,
		logger: logger,
	}
}

// SetOutputCallback sets the callback function for real-time output
func (m *Manager) SetOutputCallback(callback OutputCallback) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.outputCallback = callback
}

// UpdateConfig updates the server configuration
func (m *Manager) UpdateConfig(config *model.ServerConfig) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.config = config
}

// GetConfig returns the current server configuration
func (m *Manager) GetConfig() *model.ServerConfig {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	configCopy := *m.config
	return &configCopy
}

// buildStartArgs builds the command line arguments for starting SCUM server
func (m *Manager) buildStartArgs() []string {
	args := []string{}

	// 基本参数
	if m.config.GamePort > 0 {
		args = append(args, fmt.Sprintf("-port=%d", m.config.GamePort))
	}

	if m.config.MaxPlayers > 0 {
		args = append(args, fmt.Sprintf("-MaxPlayers=%d", m.config.MaxPlayers))
	}

	// BattlEye设置
	if !m.config.EnableBattlEye {
		args = append(args, "-nobattleye")
	}

	// 添加额外参数
	if m.config.AdditionalArgs != "" {
		additionalArgs := strings.Fields(m.config.AdditionalArgs)
		args = append(args, additionalArgs...)
	}

	return args
}

// Start starts the SCUM server process
func (m *Manager) Start() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		return fmt.Errorf("server is already running")
	}

	// Check if executable exists
	if _, err := os.Stat(m.config.ExecPath); os.IsNotExist(err) {
		return fmt.Errorf("SCUM server executable not found: %s", m.config.ExecPath)
	}

	// Check if the configured port is already in use
	if m.config.GamePort > 0 {
		portChecker := network.NewPortChecker(_const.DefaultWaitTime + _const.ShortWaitTime)
		host := m.config.ServerIP
		if host == "" {
			host = _const.DefaultServerIP
		}

		m.logger.Info("Checking if port %d is available on %s...", m.config.GamePort, host)

		portStatus, err := portChecker.CheckPort(host, m.config.GamePort)
		if err != nil {
			m.logger.Warn("Failed to check port status: %v", err)
		} else if portStatus.InUse {
			m.logger.Warn("Port %d is already in use on %s, skipping SCUM server startup", m.config.GamePort, host)
			return fmt.Errorf("port %d is already in use on %s", m.config.GamePort, host)
		} else {
			m.logger.Info("Port %d is available on %s", m.config.GamePort, host)
		}
	}

	// Build command arguments
	args := m.buildStartArgs()

	m.logger.Info("Starting SCUM server: %s %s", m.config.ExecPath, strings.Join(args, " "))

	m.cmd = exec.Command(m.config.ExecPath, args...)

	// Set working directory to the directory containing the executable
	execDir := strings.TrimSuffix(m.config.ExecPath, "SCUMServer.exe")
	if execDir != m.config.ExecPath {
		m.cmd.Dir = execDir
		m.logger.Info("Setting working directory to: %s", execDir)
	}

	// On Windows, create the process in a new console so it can receive Ctrl+C
	if runtime.GOOS == "windows" {
		m.cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		}
		m.logger.Info("Process will be created in new process group for Ctrl+C handling")
	}

	// Set up stdin, stdout and stderr pipes
	stdin, err := m.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	m.stdin = stdin

	stdout, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := m.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SCUM server: %w", err)
	}

	// On Windows, create a new process group to manage child processes
	if runtime.GOOS == "windows" {
		if err := m.createProcessGroup(); err != nil {
			m.logger.Warn("Failed to create process group: %v", err)
		}
	}

	m.logger.Info("SCUM server started with PID: %d", m.cmd.Process.Pid)
	m.logger.Info("Server configuration - Port: %d, MaxPlayers: %d, BattlEye: %v",
		m.config.GamePort, m.config.MaxPlayers, m.config.EnableBattlEye)

	// Start goroutines to read stdout and stderr
	go m.readOutput(stdout, "STDOUT")
	go m.readOutput(stderr, "STDERR")

	// Start goroutine to wait for process completion
	go m.waitForCompletion()

	return nil
}

// Stop stops the SCUM server process
func (m *Manager) Stop() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return fmt.Errorf("server is not running")
	}

	pid := m.cmd.Process.Pid
	m.logger.Info("Stopping SCUM server (PID: %d)", pid)

	// Try graceful shutdown first
	// 注意：避免使用可能影响scum_run主程序的信号
	// 优先使用进程特定的停止方法
	if runtime.GOOS == "windows" {
		// Windows下使用Ctrl+C信号优雅关闭SCUM服务器
		// 这是SCUM服务器正确的关闭方式，能够保存游戏数据
		m.logger.Info("Sending Ctrl+C to SCUM server process (PID: %d) for graceful shutdown", pid)

		// 尝试发送Ctrl+C信号
		if err := m.sendCtrlC(pid); err != nil {
			m.logger.Warn("Failed to send Ctrl+C via console API: %v, will try alternative method", err)

			// 如果Ctrl+C发送失败，尝试关闭stdin让进程自然退出
			if m.stdin != nil {
				m.logger.Info("Closing stdin pipe as fallback method")
				m.stdin.Close()
				m.stdin = nil
			}

			// 等待一段时间看进程是否自然退出
			time.Sleep(2 * time.Second)

			// 如果进程还在运行，使用taskkill作为最后手段
			// 但这可能导致数据丢失
			if m.cmd.Process != nil {
				m.logger.Warn("Process still running, using taskkill as last resort (may cause data loss)")
				cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid))
				output, err := cmd.CombinedOutput()
				if err != nil {
					m.logger.Warn("taskkill command failed: %v, output: %s", err, string(output))
				} else {
					m.logger.Info("taskkill sent to process %d: %s", pid, string(output))
				}
			}
		} else {
			// Ctrl+C发送成功，关闭stdin
			if m.stdin != nil {
				m.stdin.Close()
				m.stdin = nil
			}
		}
	} else {
		// Unix系统下使用SIGTERM，但只发送给子进程
		// 注意：这里需要确保信号只发送给SCUM进程，不影响scum_run主程序
		if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			m.logger.Warn("Failed to send SIGTERM: %v", err)
		}
	}

	// Wait for graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- m.cmd.Wait()
	}()

	select {
	case <-done:
		m.logger.Info("SCUM server stopped gracefully")
	case <-time.After(10 * time.Second):
		m.logger.Warn("Graceful shutdown timeout, forcing kill")
		if err := m.cmd.Process.Kill(); err != nil {
			m.logger.Error("Failed to kill process: %v", err)
		}
		<-done // Wait for the process to actually exit
	}

	// Close stdin pipe
	if m.stdin != nil {
		m.stdin.Close()
		m.stdin = nil
	}

	m.cmd = nil
	return nil
}

// ForceStop forcefully stops the SCUM server process and all child processes
func (m *Manager) ForceStop() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return nil // Already stopped
	}

	pid := m.cmd.Process.Pid
	m.logger.Info("Force stopping SCUM server and child processes (PID: %d)", pid)

	// Close stdin pipe first
	if m.stdin != nil {
		m.stdin.Close()
		m.stdin = nil
	}

	// On Windows, use enhanced process tree killing
	if runtime.GOOS == "windows" {
		m.logger.Info("Using Windows-specific process tree cleanup for PID: %d", pid)
		m.killProcessTree(pid)
	} else {
		// On Unix-like systems, try graceful shutdown first
		if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			m.logger.Warn("Failed to send SIGTERM: %v", err)
		}

		// Wait a bit for graceful shutdown
		time.Sleep(_const.DefaultWaitTime)

		// Force kill if still running
		if err := m.cmd.Process.Kill(); err != nil {
			m.logger.Warn("Failed to kill main process: %v", err)
		}
	}

	// Wait for process to exit
	done := make(chan error, 1)
	go func() {
		done <- m.cmd.Wait()
	}()

	select {
	case <-done:
		m.logger.Info("SCUM server force stopped")
	case <-time.After(10 * time.Second):
		m.logger.Warn("Force stop timeout, process may still be running")
		// Final attempt - kill any remaining SCUM processes
		if runtime.GOOS == "windows" {
			m.killScumProcesses()
		}
	}

	m.cmd = nil
	return nil
}

// createProcessGroup creates a new process group on Windows
func (m *Manager) createProcessGroup() error {
	if runtime.GOOS != "windows" {
		return nil
	}

	// On Windows, we'll use safer process management methods
	// This avoids using /T flag which could affect the scum_run main process
	m.logger.Info("Process group management will use safe individual process killing")
	return nil
}

// killChildProcesses kills child processes on Windows
func (m *Manager) killChildProcesses(parentPID int) {
	if runtime.GOOS != "windows" {
		return
	}

	// 注意：避免使用 /T 参数，因为它可能影响scum_run主程序
	// 使用更安全的方法逐个杀死子进程
	m.killScumChildProcesses(parentPID)
}

// killProcessTree provides enhanced process tree cleanup for Windows
func (m *Manager) killProcessTree(pid int) {
	if runtime.GOOS != "windows" {
		return
	}

	m.logger.Info("Attempting to kill process tree for PID: %d", pid)

	// 注意：避免使用 /T 参数，因为它可能影响scum_run主程序
	// 先尝试只杀死指定的进程
	cmd := exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.Warn("taskkill failed for PID %d: %v, output: %s", pid, err, string(output))

		// Fallback: try to kill individual SCUM processes
		m.killScumProcesses()
	} else {
		m.logger.Info("Successfully killed process PID %d: %s", pid, string(output))

		// 等待一段时间，然后检查是否还有子进程需要清理
		time.Sleep(1 * time.Second)

		// 尝试清理可能的子进程，但不使用 /T 参数
		m.killScumChildProcesses(pid)
	}
}

// killScumChildProcesses kills child processes of a specific parent PID
func (m *Manager) killScumChildProcesses(parentPID int) {
	if runtime.GOOS != "windows" {
		return
	}

	m.logger.Info("Attempting to kill child processes of PID: %d", parentPID)

	// 使用wmic命令查找子进程，然后逐个杀死
	// 这比使用 /T 参数更安全，不会影响scum_run主程序
	cmd := exec.Command("wmic", "process", "where", fmt.Sprintf("ParentProcessId=%d", parentPID), "get", "ProcessId", "/format:value")
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.Debug("Failed to get child processes: %v", err)
		return
	}

	// 解析输出，提取子进程PID
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "ProcessId=") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				childPID := strings.TrimSpace(parts[1])
				if childPID != "" && childPID != "0" {
					m.logger.Info("Killing child process PID: %s", childPID)
					killCmd := exec.Command("taskkill", "/F", "/PID", childPID)
					killOutput, killErr := killCmd.CombinedOutput()
					if killErr != nil {
						m.logger.Debug("Failed to kill child process %s: %v, output: %s", childPID, killErr, string(killOutput))
					} else {
						m.logger.Info("Successfully killed child process %s: %s", childPID, string(killOutput))
					}
				}
			}
		}
	}
}

// killScumProcesses kills all SCUM-related processes as a fallback
func (m *Manager) killScumProcesses() {
	if runtime.GOOS != "windows" {
		return
	}

	m.logger.Info("Attempting to kill all SCUM-related processes...")

	// List of SCUM process names to kill
	scumProcesses := []string{
		"SCUMServer.exe",
		"SCUM.exe",
		"BattlEye.exe",
		"BEService.exe",
	}

	for _, processName := range scumProcesses {
		cmd := exec.Command("taskkill", "/F", "/IM", processName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			m.logger.Debug("Failed to kill %s: %v, output: %s", processName, err, string(output))
		} else {
			m.logger.Info("Successfully killed %s: %s", processName, string(output))
		}
	}
}

// CleanupOnExit ensures all processes are cleaned up when the program exits
func (m *Manager) CleanupOnExit() {
	if m.cmd != nil && m.cmd.Process != nil {
		pid := m.cmd.Process.Pid
		m.logger.Info("Cleaning up SCUM server process on exit (PID: %d)", pid)

		// Force stop with enhanced cleanup
		if err := m.ForceStop(); err != nil {
			m.logger.Error("Failed to force stop SCUM server: %v", err)
		}

		// Additional cleanup for Windows - ensure process tree is killed
		if runtime.GOOS == "windows" {
			m.logger.Info("Performing additional Windows process cleanup for PID: %d", pid)
			m.killProcessTree(pid)
		}
	}
}

// Restart restarts the SCUM server process
func (m *Manager) Restart() error {
	if m.IsRunning() {
		if err := m.Stop(); err != nil {
			return fmt.Errorf("failed to stop server: %w", err)
		}
		// Wait a bit for the process to fully terminate
		time.Sleep(_const.DefaultWaitTime)
	}

	return m.Start()
}

// IsRunning returns whether the SCUM server is running
func (m *Manager) IsRunning() bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}

	// Check if process is still alive
	if runtime.GOOS == "windows" {
		// On Windows, check if we can get process info
		if err := m.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return false
		}
	} else {
		// On Unix-like systems, send signal 0 to check if process exists
		if err := m.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return false
		}
	}

	return true
}

// GetPID returns the process ID of the running server
func (m *Manager) GetPID() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process.Pid
	}
	return 0
}

// readOutput reads output from stdout or stderr and logs it
func (m *Manager) readOutput(pipe io.ReadCloser, source string) {
	defer pipe.Close()
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()

		// 调用回调函数发送实时输出
		if m.outputCallback != nil {
			m.outputCallback(source, line)
		}
	}
	if err := scanner.Err(); err != nil {
		m.logger.Error("Error reading %s: %v", source, err)
	}
}

// SendCommand sends a command to the running SCUM server
func (m *Manager) SendCommand(command string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd == nil {
		m.logger.Error("DEBUG: cmd is nil")
		return fmt.Errorf("server process is not initialized")
	}

	if m.cmd.Process == nil {
		m.logger.Error("DEBUG: cmd.Process is nil")
		return fmt.Errorf("server process is not running")
	}

	if m.stdin == nil {
		m.logger.Error("DEBUG: stdin pipe is nil")
		return fmt.Errorf("stdin pipe is not available")
	}

	// Write command to stdin with newline
	_, err := fmt.Fprintf(m.stdin, "%s\n", command)
	if err != nil {
		m.logger.Error("DEBUG: Failed to write command to stdin: %v", err)
		return fmt.Errorf("failed to send command: %w", err)
	}

	m.logger.Info("DEBUG: Command written to stdin successfully: %s", command)
	return nil
}

// waitForCompletion waits for the process to complete
func (m *Manager) waitForCompletion() {
	if m.cmd != nil {
		err := m.cmd.Wait()
		m.mutex.Lock()
		pid := 0
		if m.cmd.Process != nil {
			pid = m.cmd.Process.Pid
		}
		// Close stdin pipe when process completes
		if m.stdin != nil {
			m.stdin.Close()
			m.stdin = nil
		}
		m.cmd = nil
		m.mutex.Unlock()

		if err != nil {
			m.logger.Error("SCUM server (PID: %d) exited with error: %v", pid, err)
		} else {
			m.logger.Info("SCUM server (PID: %d) exited normally", pid)
		}
	}
}
