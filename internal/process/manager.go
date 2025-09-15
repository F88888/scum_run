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
	"scum_run/model"
)

// Manager manages the SCUM server process
type Manager struct {
	config *model.ServerConfig
	logger *logger.Logger
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mutex  sync.Mutex
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

	m.logger.Info("Stopping SCUM server (PID: %d)", m.cmd.Process.Pid)

	// Try graceful shutdown first
	if runtime.GOOS == "windows" {
		if err := m.cmd.Process.Signal(os.Interrupt); err != nil {
			m.logger.Warn("Failed to send interrupt signal: %v", err)
		}
	} else {
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

// Restart restarts the SCUM server process
func (m *Manager) Restart() error {
	if m.IsRunning() {
		if err := m.Stop(); err != nil {
			return fmt.Errorf("failed to stop server: %w", err)
		}
		// Wait a bit for the process to fully terminate
		time.Sleep(2 * time.Second)
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
		m.logger.Info("[%s] %s", source, line)
	}
	if err := scanner.Err(); err != nil {
		m.logger.Error("Error reading %s: %v", source, err)
	}
}

// SendCommand sends a command to the running SCUM server
func (m *Manager) SendCommand(command string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return fmt.Errorf("server is not running")
	}

	if m.stdin == nil {
		return fmt.Errorf("stdin pipe is not available")
	}

	m.logger.Info("Sending command to server: %s", command)

	// Write command to stdin with newline
	_, err := fmt.Fprintf(m.stdin, "%s\n", command)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}

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
