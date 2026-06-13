package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	waitDone       chan error
	startedAt      time.Time // startedAt 是当前 SCUM 进程启动时间，空值表示未运行或未知。
	mutex          sync.Mutex
	outputCallback OutputCallback
}

// Status 是对外返回的安全进程状态。
type Status struct {
	// Running 表示 SCUM 服务进程当前是否仍在运行。
	Running bool `json:"running"`
	// State 表示本地记录的服务状态，例如 running、starting、crashed 或 blocked。
	State string `json:"state"`
	// PID 是当前进程 ID；未运行时为 0。
	PID int `json:"pid"`
	// ServiceName 是本地服务实例名称。
	ServiceName string `json:"service_name,omitempty"`
	// GamePort 是本地服务监听端口。
	GamePort int `json:"game_port,omitempty"`
	// StartedAt 是当前进程启动时间；未运行或未知时为空。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// UptimeSeconds 是当前进程已运行秒数；未运行时为 0。
	UptimeSeconds int64 `json:"uptime_seconds"`
	// ConsecutiveStartFailures 是连续启动失败次数。
	ConsecutiveStartFailures int `json:"consecutive_start_failures,omitempty"`
	// LastError 是最近一次启动或运行失败的错误摘要。
	LastError string `json:"last_error,omitempty"`
	// LastLogTail 是最近一次失败时截取的本地进程输出尾部。
	LastLogTail []string `json:"last_log_tail,omitempty"`
}

// New creates a new process manager
func New(execPath string, logger *logger.Logger) *Manager {
	return &Manager{
		config: &model.ServerConfig{
			ServiceName:    "scum",
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

	// 命令行服务器（GamePort == 0）只使用 AdditionalArgs，不添加 SCUM 特定参数
	if m.config.GamePort == 0 {
		// 命令行服务器：AdditionalArgs 是完整的启动命令
		if m.config.AdditionalArgs != "" {
			additionalArgs := strings.Fields(m.config.AdditionalArgs)
			args = append(args, additionalArgs...)
		}
		return args
	}

	// 普通 SCUM 服务器：添加 SCUM 特定参数
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

// Start launches or attaches to the configured game server process.
// It does not take parameters because configuration is stored on the manager, including service name, port and startup command.
// It returns nil when an existing matching service is alive or a new process starts, or an error when validation, locking, process setup, startup, or failure-threshold checks fail.
func (m *Manager) Start() error {
	releaseLock, err := m.acquireStartupLock()
	if err != nil {
		return err
	}
	defer releaseLock()

	m.mutex.Lock()
	if m.cmd != nil && m.cmd.Process != nil {
		if isProcessRunning(m.cmd.Process) {
			m.mutex.Unlock()
			return nil
		}
	}
	m.mutex.Unlock()

	state, err := m.readRuntimeState()
	if err != nil {
		return err
	}
	if inspected, alive := m.inspectRuntimeState(state); alive {
		inspected.State = runtimeStateRunning
		inspected.LastError = ""
		if err := m.writeRuntimeState(inspected); err != nil {
			m.logger.Warn("Failed to refresh attached process state: %v", err)
		}
		m.mutex.Lock()
		m.startedAt = inspected.StartedAt
		m.mutex.Unlock()
		m.logger.Info("Attached to existing %s server process on port %d with PID: %d", inspected.ServiceName, inspected.GamePort, inspected.PID)
		return nil
	}
	if state.PID > 0 && (state.State == runtimeStateRunning || state.State == runtimeStateStarting) {
		_ = m.recordStartFailure("previous server process is no longer running", nil)
		state, _ = m.readRuntimeState()
	}
	if state.State == runtimeStateBlocked && state.ConsecutiveStartFailures >= startFailureThreshold {
		return fmt.Errorf("server startup is blocked after %d consecutive failures: %s", state.ConsecutiveStartFailures, state.LastError)
	}

	if err := m.ensurePortAvailable(); err != nil {
		_ = m.recordStartFailure(err.Error(), nil)
		return err
	}

	cmd, err := m.buildCommand()
	if err != nil {
		_ = m.recordStartFailure(err.Error(), nil)
		return err
	}

	m.configureProcessGroup(cmd)

	logFile, err := m.openProcessLog()
	if err != nil {
		_ = m.recordStartFailure(err.Error(), nil)
		return err
	}

	// Detached services must not depend on parent-owned stdout/stderr pipes.
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = logFile.Close()
		_ = m.recordStartFailure(fmt.Sprintf("failed to create stdin pipe: %v", err), nil)
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	state = m.runtimeStateFromConfig()
	state.State = runtimeStateStarting
	if err := m.writeRuntimeState(state); err != nil {
		_ = logFile.Close()
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = m.recordStartFailure(fmt.Sprintf("failed to start server: %v", err), nil)
		return fmt.Errorf("failed to start server: %w", err)
	}
	startedAt := time.Now().UTC()
	waitDone := make(chan error, 1)

	runningState := m.runtimeStateFromConfig()
	runningState.State = runtimeStateRunning
	runningState.PID = cmd.Process.Pid
	runningState.ProcessName = processName(cmd.Process.Pid)
	runningState.ProcessCreateTimeMS = processCreateTimeMS(cmd.Process.Pid)
	runningState.StartedAt = startedAt
	if err := m.writeRuntimeState(runningState); err != nil {
		m.logger.Warn("Failed to write running process state: %v", err)
	}

	m.mutex.Lock()
	m.cmd = cmd
	m.stdin = stdin
	m.waitDone = waitDone
	m.startedAt = startedAt
	m.mutex.Unlock()

	m.logger.Info("Game server %s started on port %d with PID: %d", runningState.ServiceName, runningState.GamePort, cmd.Process.Pid)

	go m.readDetachedLogClose(logFile)
	go m.waitForCompletion(cmd, waitDone)

	select {
	case err := <-waitDone:
		summary := "server exited immediately after startup"
		if err != nil {
			summary = fmt.Sprintf("%s: %v", summary, err)
		}
		return fmt.Errorf("%s", summary)
	case <-time.After(_const.ShortWaitTime):
		return nil
	}
}

// buildCommand creates the OS command used to start the configured server.
// It reads service configuration from the manager, and it returns the command or an error when required legacy executable settings are invalid.
func (m *Manager) buildCommand() (*exec.Cmd, error) {
	if m.config != nil && m.config.LaunchProfile != nil {
		return m.buildLaunchProfileCommand()
	}
	if strings.TrimSpace(m.config.StartCommand) != "" {
		shell, args := shellCommand(m.config.StartCommand)
		cmd := exec.Command(shell, args...)
		if workDir := strings.TrimSpace(m.config.WorkDir); workDir != "" {
			cmd.Dir = workDir
		} else if strings.TrimSpace(m.config.ExecPath) != "" {
			cmd.Dir = m.config.ExecPath
		}
		m.logger.Info("Starting configured service command for %s on port %d", m.config.ServiceName, m.config.GamePort)
		return cmd, nil
	}

	if m.config.GamePort == 0 {
		shell, args := shellCommand(m.config.AdditionalArgs)
		cmd := exec.Command(shell, args...)
		if m.config.ExecPath != "" {
			cmd.Dir = m.config.ExecPath
			m.logger.Info("Setting working directory to: %s", m.config.ExecPath)
		}
		m.logger.Info("Starting legacy command line server: %s (in directory: %s)", m.config.AdditionalArgs, m.config.ExecPath)
		return cmd, nil
	}

	if _, err := os.Stat(m.config.ExecPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("server executable not found: %s", m.config.ExecPath)
	}

	args := m.buildStartArgs()
	m.logger.Info("Starting legacy SCUM server: %s %s", m.config.ExecPath, strings.Join(args, " "))

	cmd := exec.Command(m.config.ExecPath, args...)
	if workDir := strings.TrimSpace(m.config.WorkDir); workDir != "" {
		cmd.Dir = workDir
		m.logger.Info("Setting working directory to configured scope root: %s", workDir)
	} else {
		execDir := strings.TrimSuffix(m.config.ExecPath, "SCUMServer.exe")
		if execDir != m.config.ExecPath {
			cmd.Dir = execDir
			m.logger.Info("Setting working directory to: %s", execDir)
		}
	}
	return cmd, nil
}

// buildLaunchProfileCommand creates an OS command from a generic launch profile.
// It reads the current manager config, resolves workDir and relative executables under the instance scope, and returns the command or an error when mode, path containment, or executable availability is invalid.
func (m *Manager) buildLaunchProfileCommand() (*exec.Cmd, error) {
	profile := m.config.LaunchProfile
	if profile == nil {
		return nil, fmt.Errorf("launch profile is required")
	}
	workDir, err := m.resolveScopedPath(profile.WorkDir, true)
	if err != nil {
		return nil, fmt.Errorf("resolve launch workDir: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(profile.LaunchMode))
	if mode == "" {
		mode = "argv"
	}
	var cmd *exec.Cmd
	switch mode {
	case "argv":
		executable, err := m.resolveExecutablePath(workDir, profile.Executable)
		if err != nil {
			return nil, err
		}
		cmd = exec.Command(executable, profile.Args...)
	case "shell":
		if strings.TrimSpace(profile.ShellCommand) == "" {
			return nil, fmt.Errorf("shell command is required")
		}
		shell, args := shellCommand(profile.ShellCommand)
		cmd = exec.Command(shell, args...)
	default:
		return nil, fmt.Errorf("unsupported launch mode: %s", mode)
	}
	cmd.Dir = workDir
	if len(profile.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range profile.Env {
			cmd.Env = append(cmd.Env, strings.TrimSpace(key)+"="+value)
		}
	}
	m.logger.Info("Starting launch profile %s generation %d in %s", profile.ServiceName, profile.LaunchGeneration, workDir)
	return cmd, nil
}

// resolveExecutablePath resolves a launch-profile executable under the scoped work directory.
// workDir is the resolved scoped working directory and executable is the profile relative executable path; it returns an executable path or an error for absolute, escaping, or missing files.
func (m *Manager) resolveExecutablePath(workDir string, executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return "", fmt.Errorf("executable is required")
	}
	if filepath.IsAbs(executable) {
		return "", fmt.Errorf("absolute executable paths are not allowed")
	}
	candidate := filepath.Clean(filepath.Join(workDir, filepath.FromSlash(executable)))
	if !pathWithin(workDir, candidate) && !pathWithin(m.launchScopeRoot(), candidate) {
		return "", fmt.Errorf("executable escapes instance scope")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("executable is missing: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("executable is a directory")
	}
	resolved, err := resolveExistingPath(candidate)
	if err != nil {
		return "", err
	}
	scope, err := resolveExistingPath(m.launchScopeRoot())
	if err != nil {
		return "", err
	}
	if !pathWithin(scope, resolved) {
		return "", fmt.Errorf("executable symlink escapes instance scope")
	}
	return resolved, nil
}

// resolveScopedPath resolves an instance-relative path under the launch profile scope.
// relative is the profile path and mustExist controls existence checks; it returns the resolved host path or an error for absolute, traversal, missing, or symlink-escaping paths.
func (m *Manager) resolveScopedPath(relative string, mustExist bool) (string, error) {
	scope := m.launchScopeRoot()
	if strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("instance scope root is required")
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if cleanRelative == "" || cleanRelative == "." {
		cleanRelative = "."
	}
	if strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) || cleanRelative == ".." {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(scope, cleanRelative))
	if !pathWithin(scope, candidate) {
		return "", fmt.Errorf("path escapes instance scope")
	}
	if mustExist {
		info, err := os.Stat(candidate)
		if err != nil {
			return "", fmt.Errorf("scoped path is missing: %w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("scoped workDir is not a directory")
		}
		resolved, err := resolveExistingPath(candidate)
		if err != nil {
			return "", err
		}
		resolvedScope, err := resolveExistingPath(scope)
		if err != nil {
			return "", err
		}
		if !pathWithin(resolvedScope, resolved) {
			return "", fmt.Errorf("scoped path symlink escapes instance scope")
		}
		return resolved, nil
	}
	return candidate, nil
}

// launchScopeRoot returns the host-local root assigned to the current instance.
// It reads WorkDir first and falls back to ExecPath for older scoped launch-profile configs, returning an empty string when no scope is configured.
func (m *Manager) launchScopeRoot() string {
	if m == nil || m.config == nil {
		return ""
	}
	if workDir := strings.TrimSpace(m.config.WorkDir); workDir != "" {
		return filepath.Clean(workDir)
	}
	return filepath.Clean(strings.TrimSpace(m.config.ExecPath))
}

// resolveExistingPath resolves symlinks for an existing path.
// path identifies a local filesystem entry, and the function returns its evaluated path or an error when resolution fails.
func resolveExistingPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve symlink path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// pathWithin reports whether child is equal to or inside parent.
// parent and child are host-local paths, and the function returns true when child is contained by parent after filepath cleaning.
func pathWithin(parent string, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ensurePortAvailable verifies that the configured service port is not already owned by an untracked process.
// It does not take parameters, and it returns nil when no port check is required or the port is available.
func (m *Manager) ensurePortAvailable() error {
	serviceName, port := serviceIdentity(m.config)
	if port <= 0 {
		return nil
	}
	portChecker := network.NewPortChecker(_const.DefaultWaitTime + _const.ShortWaitTime)
	host := m.config.ServerIP
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}

	m.logger.Info("Checking if port %d is available on %s...", port, host)
	portStatus, err := portChecker.CheckPort(host, port)
	if err != nil {
		m.logger.Warn("Failed to check port status: %v", err)
		return nil
	}
	if portStatus.InUse {
		return fmt.Errorf("port %d is already in use on %s for service %s", port, host, serviceName)
	}
	return nil
}

// openProcessLog opens the detached process log file for stdout and stderr.
// It does not take parameters, and it returns an append-only file handle or an error when the log cannot be created.
func (m *Manager) openProcessLog() (*os.File, error) {
	path := m.logPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create process log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open process log: %w", err)
	}
	return file, nil
}

// readDetachedLogClose closes a detached process log handle after the process has inherited it.
// file is the opened log file, and the method returns no values because close errors are only logged.
func (m *Manager) readDetachedLogClose(file *os.File) {
	if file == nil {
		return
	}
	if err := file.Close(); err != nil {
		m.logger.Warn("Failed to close parent process log handle: %v", err)
	}
}

// Stop stops the configured game server process.
// It does not take parameters and uses either the in-memory command or the persisted runtime state.
// It returns nil when the process is stopped or an error when no matching running process exists or the stop signal fails.
func (m *Manager) Stop() error {
	m.mutex.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mutex.Unlock()
		state, err := m.readRuntimeState()
		if err != nil {
			return err
		}
		state, alive := m.inspectRuntimeState(state)
		if !alive {
			return fmt.Errorf("server is not running")
		}
		return m.stopPersistedProcess(state, false)
	}

	cmd := m.cmd
	waitDone := m.waitDone
	pid := cmd.Process.Pid
	m.logger.Info("Stopping SCUM server (PID: %d)", pid)
	m.markStopping(pid)

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
			m.mutex.Unlock()

			// 等待一段时间看进程是否自然退出
			time.Sleep(2 * time.Second)

			// 如果进程还在运行，使用taskkill作为最后手段
			// 但这可能导致数据丢失
			if isProcessRunning(cmd.Process) {
				m.logger.Warn("Process still running, using taskkill as last resort (may cause data loss)")
				killCmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid))
				output, err := killCmd.CombinedOutput()
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
			m.mutex.Unlock()
		}
	} else {
		// Unix系统下使用SIGTERM，但只发送给子进程
		// 注意：这里需要确保信号只发送给SCUM进程，不影响scum_run主程序
		if err := signalProcess(cmd.Process, syscall.SIGTERM); err != nil {
			m.logger.Warn("Failed to send SIGTERM: %v", err)
		}
		m.mutex.Unlock()
	}

	if waitDone == nil {
		return nil
	}

	select {
	case <-waitDone:
		m.logger.Info("SCUM server stopped gracefully")
	case <-time.After(10 * time.Second):
		m.logger.Warn("Graceful shutdown timeout, forcing kill")
		if err := killProcess(cmd.Process); err != nil {
			m.logger.Error("Failed to kill process: %v", err)
		}
		<-waitDone // Wait for the process to actually exit
	}

	return nil
}

// ForceStop forcefully stops the configured game server process and known child processes.
// It does not take parameters and uses either the in-memory command or the persisted runtime state.
// It returns nil when the process is already stopped or force-stop best effort completes, or an error when persisted state cannot be read.
func (m *Manager) ForceStop() error {
	m.mutex.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mutex.Unlock()
		state, err := m.readRuntimeState()
		if err != nil {
			return err
		}
		state, alive := m.inspectRuntimeState(state)
		if !alive {
			return nil
		}
		return m.stopPersistedProcess(state, true)
	}

	cmd := m.cmd
	waitDone := m.waitDone
	pid := cmd.Process.Pid
	m.logger.Info("Force stopping SCUM server and child processes (PID: %d)", pid)

	// Close stdin pipe first
	if m.stdin != nil {
		m.stdin.Close()
		m.stdin = nil
	}
	m.mutex.Unlock()

	// On Windows, use enhanced process tree killing
	if runtime.GOOS == "windows" {
		m.logger.Info("Using Windows-specific process tree cleanup for PID: %d", pid)
		m.killProcessTree(pid)
	} else {
		// On Unix-like systems, try graceful shutdown first
		if err := signalProcess(cmd.Process, syscall.SIGTERM); err != nil {
			m.logger.Warn("Failed to send SIGTERM: %v", err)
		}

		// Wait a bit for graceful shutdown
		time.Sleep(_const.DefaultWaitTime)

		// Force kill if still running
		if err := killProcess(cmd.Process); err != nil {
			m.logger.Warn("Failed to kill main process: %v", err)
		}
	}

	if waitDone == nil {
		return nil
	}

	select {
	case <-waitDone:
		m.logger.Info("SCUM server force stopped")
	case <-time.After(10 * time.Second):
		m.logger.Warn("Force stop timeout, process may still be running")
		// Final attempt - kill any remaining SCUM processes
		if runtime.GOOS == "windows" {
			m.killScumProcesses()
		}
	}

	return nil
}

// markStopping records that a tracked process is being intentionally stopped.
// pid identifies the process being stopped, and the method returns no values because persistence failures are logged.
func (m *Manager) markStopping(pid int) {
	state, err := m.readRuntimeState()
	if err != nil {
		m.logger.Warn("Failed to read process state before stop: %v", err)
		return
	}
	if state.PID != pid {
		return
	}
	state.State = runtimeStateStopping
	state.LastError = ""
	if err := m.writeRuntimeState(state); err != nil {
		m.logger.Warn("Failed to mark process stopping: %v", err)
	}
}

// stopPersistedProcess stops a process recovered from the runtime state file.
// state contains the PID and identity to stop, force selects forceful termination, and the method returns an error when the signal cannot be sent.
func (m *Manager) stopPersistedProcess(state RuntimeState, force bool) error {
	pid := state.PID
	m.logger.Info("Stopping persisted server process %s on port %d (PID: %d)", state.ServiceName, state.GamePort, pid)
	state.State = runtimeStateStopping
	state.LastError = ""
	if err := m.writeRuntimeState(state); err != nil {
		m.logger.Warn("Failed to mark persisted process stopping: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if runtime.GOOS == "windows" {
		args := []string{"/PID", fmt.Sprintf("%d", pid)}
		if force {
			args = append([]string{"/F"}, args...)
		}
		killCmd := exec.Command("taskkill", args...)
		output, err := killCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("taskkill process %d failed: %w, output: %s", pid, err, string(output))
		}
	} else if force {
		if err := killProcess(process); err != nil {
			return fmt.Errorf("kill process %d: %w", pid, err)
		}
	} else if err := signalProcess(process, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal process %d: %w", pid, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive := m.inspectRuntimeState(state); !alive {
			state.State = runtimeStateStopped
			state.PID = 0
			state.LastError = ""
			_ = m.writeRuntimeState(state)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !force {
		return m.stopPersistedProcess(state, true)
	}
	return fmt.Errorf("process %d did not stop before timeout", pid)
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

// CleanupOnExit detaches from the managed game server when scum_run exits.
// It does not take parameters, and it returns no values because executor shutdown must not stop a user server.
func (m *Manager) CleanupOnExit() {
	if m.cmd != nil && m.cmd.Process != nil {
		pid := m.cmd.Process.Pid
		m.logger.Info("Detaching from game server process on scum_run exit (PID: %d)", pid)
	}
}

// Restart restarts the configured game server process.
// It does not take parameters and reuses the manager configuration for the new process.
// It returns nil when stop and start succeed, or an error when either phase fails.
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

// IsRunning reports whether the configured game server process is alive.
// It does not take parameters, and it returns true when either the in-memory command or persisted runtime state points to a live matching process.
func (m *Manager) IsRunning() bool {
	m.mutex.Lock()
	if m.cmd != nil && m.cmd.Process != nil && isProcessRunning(m.cmd.Process) {
		m.mutex.Unlock()
		return true
	}
	m.mutex.Unlock()
	state, err := m.readRuntimeState()
	if err != nil {
		return false
	}
	_, alive := m.inspectRuntimeState(state)
	return alive
}

// GetPID returns the process ID of the configured game server.
// It does not take parameters, and it returns 0 when no live in-memory or persisted process is currently tracked.
func (m *Manager) GetPID() int {
	m.mutex.Lock()
	if m.cmd != nil && m.cmd.Process != nil && isProcessRunning(m.cmd.Process) {
		defer m.mutex.Unlock()
		return m.cmd.Process.Pid
	}
	m.mutex.Unlock()
	state, err := m.readRuntimeState()
	if err != nil {
		return 0
	}
	state, alive := m.inspectRuntimeState(state)
	if !alive {
		return 0
	}
	return state.PID
}

// GetStatus returns a safe status snapshot for the configured game server process.
// It does not take parameters, and it returns running state, PID, service identity, uptime and bounded failure metadata without exposing command lines or host paths.
func (m *Manager) GetStatus() Status {
	m.mutex.Lock()
	if m.cmd != nil && m.cmd.Process != nil && isProcessRunning(m.cmd.Process) {
		status := m.statusFromMemoryLocked()
		m.mutex.Unlock()
		return status
	}
	m.mutex.Unlock()

	state, err := m.readRuntimeState()
	if err != nil {
		return Status{}
	}
	state, alive := m.inspectRuntimeState(state)
	if !alive && state.PID > 0 && (state.State == runtimeStateRunning || state.State == runtimeStateStarting) {
		_ = m.recordStartFailure("server process is no longer running", nil)
		state, _ = m.readRuntimeState()
	}
	status := statusFromRuntimeState(state, alive)
	if alive && state.State != runtimeStateRunning {
		state.State = runtimeStateRunning
		_ = m.writeRuntimeState(state)
	}
	return status
}

// statusFromMemoryLocked builds a status snapshot from the in-memory command.
// The manager mutex must already be held, and the method returns a status payload for the currently tracked process.
func (m *Manager) statusFromMemoryLocked() Status {
	serviceName, port := serviceIdentity(m.config)
	status := Status{
		Running:     true,
		State:       runtimeStateRunning,
		PID:         m.cmd.Process.Pid,
		ServiceName: serviceName,
		GamePort:    port,
	}
	if !m.startedAt.IsZero() {
		startedAt := m.startedAt
		status.StartedAt = &startedAt
		status.UptimeSeconds = int64(time.Since(startedAt).Seconds())
	}
	return status
}

// statusFromRuntimeState builds a status snapshot from a persisted runtime state.
// state is the persisted process state and alive indicates OS liveness, and the function returns a safe status payload.
func statusFromRuntimeState(state RuntimeState, alive bool) Status {
	status := Status{
		Running:                  alive,
		State:                    state.State,
		ServiceName:              state.ServiceName,
		GamePort:                 state.GamePort,
		ConsecutiveStartFailures: state.ConsecutiveStartFailures,
		LastError:                state.LastError,
		LastLogTail:              state.LastLogTail,
	}
	if alive {
		status.PID = state.PID
		if !state.StartedAt.IsZero() {
			startedAt := state.StartedAt
			status.StartedAt = &startedAt
			status.UptimeSeconds = int64(time.Since(startedAt).Seconds())
		}
	}
	return status
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

// waitForCompletion waits for the tracked process to complete and persists the terminal state.
// cmd is the process command and waitDone receives the wait result; the function returns no values and clears in-memory process state when the command exits.
func (m *Manager) waitForCompletion(cmd *exec.Cmd, waitDone chan error) {
	pid := 0
	if cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	err := cmd.Wait()

	m.mutex.Lock()
	if m.cmd == cmd {
		// Close stdin pipe when process completes
		if m.stdin != nil {
			m.stdin.Close()
			m.stdin = nil
		}
		m.cmd = nil
		m.waitDone = nil
		m.startedAt = time.Time{}
	}
	m.mutex.Unlock()

	m.recordProcessExit(pid, err)

	waitDone <- err
	close(waitDone)

	if err != nil {
		m.logger.Error("SCUM server (PID: %d) exited with error: %v", pid, err)
	} else {
		m.logger.Info("SCUM server (PID: %d) exited normally", pid)
	}
}

// recordProcessExit persists stopped or crashed state for a completed child process.
// pid identifies the completed process and waitErr is the command wait result; the method returns no values because persistence failures are logged.
func (m *Manager) recordProcessExit(pid int, waitErr error) {
	state, err := m.readRuntimeState()
	if err != nil {
		m.logger.Warn("Failed to read process state after exit: %v", err)
		return
	}
	if state.PID != pid {
		return
	}
	wasStopping := state.State == runtimeStateStopping
	state.PID = 0
	state.LastExitCode = exitCodeFromError(waitErr)
	if wasStopping {
		state.State = runtimeStateStopped
		state.LastError = ""
	} else if waitErr != nil {
		state.State = runtimeStateCrashed
		state.LastError = fmt.Sprintf("server process exited: %v", waitErr)
		state.LastLogTail = tailLogLines(state.LogPath, 20)
		state.ConsecutiveStartFailures++
	} else {
		state.State = runtimeStateStopped
		state.LastError = ""
	}
	if state.ConsecutiveStartFailures >= startFailureThreshold && !wasStopping {
		state.State = runtimeStateBlocked
	}
	if err := m.writeRuntimeState(state); err != nil {
		m.logger.Warn("Failed to write process exit state: %v", err)
	}
}
