package process

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"

	"scum_run/model"
)

const (
	runtimeStateRunning  = "running"
	runtimeStateStarting = "starting"
	runtimeStateStopping = "stopping"
	runtimeStateStopped  = "stopped"
	runtimeStateCrashed  = "crashed"
	runtimeStateBlocked  = "blocked"

	startFailureThreshold = 3
	lockStaleAfter        = 30 * time.Second
)

var unsafeStateFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// RuntimeState 是本地持久化的游戏服务运行状态。
type RuntimeState struct {
	// ServerInstanceID 是该本地进程对应的平台服务器实例 ID。
	ServerInstanceID string `json:"server_instance_id,omitempty"`
	// ServiceName 是服务实例名称，用于区分同一执行器上的多个游戏服务。
	ServiceName string `json:"service_name"`
	// GamePort 是游戏服务监听端口，用于启动前去重和服务状态识别。
	GamePort int `json:"game_port"`
	// DeclaredPorts 是启动配置声明的端口集合。
	DeclaredPorts []model.LaunchDeclaredPort `json:"declared_ports,omitempty"`
	// LaunchGeneration 是当前本地状态对应的平台启动配置代次。
	LaunchGeneration uint64 `json:"launch_generation,omitempty"`
	// State 是当前运行状态，例如 starting、running、crashed 或 blocked。
	State string `json:"state"`
	// PID 是最近一次记录的服务器进程 ID。
	PID int `json:"pid"`
	// ProcessName 是启动后观测到的系统进程名称。
	ProcessName string `json:"process_name,omitempty"`
	// ProcessCreateTimeMS 是系统报告的进程创建时间毫秒，用于降低 PID 复用误判。
	ProcessCreateTimeMS int64 `json:"process_create_time_ms,omitempty"`
	// WorkDir 是启动命令的工作目录。
	WorkDir string `json:"work_dir,omitempty"`
	// StartCommand 是脱敏前的本地启动命令，仅保存在执行器本机状态文件中用于接管判断。
	StartCommand string `json:"start_command,omitempty"`
	// StartedAt 是本次服务启动时间。
	StartedAt time.Time `json:"started_at,omitempty"`
	// UpdatedAt 是状态文件最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// ConsecutiveStartFailures 是连续启动失败次数，达到阈值后阻止继续自动启动。
	ConsecutiveStartFailures int `json:"consecutive_start_failures"`
	// LastError 是最近一次启动或运行失败的错误摘要。
	LastError string `json:"last_error,omitempty"`
	// LastExitCode 是最近一次进程退出码，无法获取时为空。
	LastExitCode *int `json:"last_exit_code,omitempty"`
	// LogPath 是本地 stdout/stderr 重定向日志文件路径。
	LogPath string `json:"log_path,omitempty"`
	// LastLogTail 是最近一次失败时截取的本地进程输出尾部。
	LastLogTail []string `json:"last_log_tail,omitempty"`
}

// serviceIdentity returns the stable local identity for one configured game service.
// config contains service name, port and command hints, and the function returns the identity name plus port used by state and lock files.
func serviceIdentity(config *model.ServerConfig) (string, int) {
	if config == nil {
		return "game-server", 0
	}
	if config.LaunchProfile != nil {
		serviceName := strings.TrimSpace(config.LaunchProfile.ServiceName)
		if serviceName == "" {
			serviceName = "game-server"
		}
		return serviceName, launchProfileGamePort(config.LaunchProfile)
	}
	serviceName := strings.TrimSpace(config.ServiceName)
	if serviceName == "" && strings.TrimSpace(config.StartCommand) != "" {
		serviceName = firstCommandToken(config.StartCommand)
	}
	if serviceName == "" && strings.TrimSpace(config.ExecPath) != "" {
		serviceName = filepath.Base(strings.TrimSpace(config.ExecPath))
	}
	if serviceName == "" {
		serviceName = "game-server"
	}
	return serviceName, config.GamePort
}

// firstCommandToken extracts a readable process name from a shell command.
// command is the configured startup command, and the function returns the first token without surrounding quotes or a generic fallback.
func firstCommandToken(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return "game-server"
	}
	return filepath.Base(strings.Trim(fields[0], `"'`))
}

// stateBaseDir returns the directory used for local process runtime files.
// It does not take parameters, and it returns a writable path near the executable unless SCUM_RUN_PROCESS_STATE_DIR overrides it.
func stateBaseDir() string {
	if override := strings.TrimSpace(os.Getenv("SCUM_RUN_PROCESS_STATE_DIR")); override != "" {
		return override
	}
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(".", "runtime", "processes")
	}
	return filepath.Join(filepath.Dir(exe), "runtime", "processes")
}

// stateFileName builds a bounded safe filename from service identity.
// serviceName and port identify one local game service, and the function returns a collision-resistant filename stem.
func stateFileName(serviceName string, port int) string {
	return stateFileNameWithGeneration(serviceName, port, 0)
}

// stateFileNameWithGeneration builds a bounded safe filename from service identity and launch generation.
// serviceName, port and generation identify one local game service generation, and the function returns a collision-resistant filename stem.
func stateFileNameWithGeneration(serviceName string, port int, generation uint64) string {
	base := unsafeStateFileChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(serviceName)), "_")
	base = strings.Trim(base, "._-")
	if base == "" {
		base = "game-server"
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d:%d", serviceName, port, generation)))
	if generation > 0 {
		return fmt.Sprintf("%s_%d_g%d_%s", base, port, generation, hex.EncodeToString(sum[:])[:8])
	}
	return fmt.Sprintf("%s_%d_%s", base, port, hex.EncodeToString(sum[:])[:8])
}

// statePath returns the runtime state path for the manager's current service identity.
// It does not take parameters, and it returns the absolute or relative JSON file path used for persisted process state.
func (m *Manager) statePath() string {
	serviceName, port := serviceIdentity(m.config)
	return filepath.Join(stateBaseDir(), stateFileNameWithGeneration(serviceName, port, launchGeneration(m.config))+".json")
}

// lockPath returns the startup lock path for the manager's current service identity.
// It does not take parameters, and it returns the lock file path paired with the state file.
func (m *Manager) lockPath() string {
	serviceName, port := serviceIdentity(m.config)
	return filepath.Join(stateBaseDir(), stateFileNameWithGeneration(serviceName, port, launchGeneration(m.config))+".lock")
}

// logPath returns the process log path for the manager's current service identity.
// It does not take parameters, and it returns the log file path where detached stdout and stderr are appended.
func (m *Manager) logPath() string {
	serviceName, port := serviceIdentity(m.config)
	return filepath.Join(stateBaseDir(), stateFileNameWithGeneration(serviceName, port, launchGeneration(m.config))+".log")
}

// acquireStartupLock creates an exclusive startup lock for one service.
// It does not take parameters, and it returns a release function or an error when another fresh startup is already in progress.
func (m *Manager) acquireStartupLock() (func(), error) {
	path := m.lockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create process state directory: %w", err)
	}
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, fmt.Errorf("inspect process startup lock: %w", statErr)
		}
		if time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		return nil, fmt.Errorf("server startup already in progress")
	}
}

// readRuntimeState loads the manager's persisted process state.
// It does not take parameters, and it returns an empty state when no file exists or an error when the state is unreadable.
func (m *Manager) readRuntimeState() (RuntimeState, error) {
	data, err := os.ReadFile(m.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeState{}, nil
		}
		return RuntimeState{}, fmt.Errorf("read process state: %w", err)
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("decode process state: %w", err)
	}
	return state, nil
}

// writeRuntimeState persists one process state atomically enough for local reconciliation.
// state contains the latest process facts, and the method returns an error when the state cannot be written.
func (m *Manager) writeRuntimeState(state RuntimeState) error {
	path := m.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create process state directory: %w", err)
	}
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode process state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write process state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace process state: %w", err)
	}
	return nil
}

// inspectRuntimeState validates whether a persisted state still points to a live process.
// state contains PID and process metadata, and the method returns a normalized state plus whether the process is alive.
func (m *Manager) inspectRuntimeState(state RuntimeState) (RuntimeState, bool) {
	if state.PID <= 0 {
		return state, false
	}
	proc, err := process.NewProcess(int32(state.PID))
	if err != nil {
		return state, false
	}
	createTime, err := proc.CreateTime()
	if err == nil && state.ProcessCreateTimeMS > 0 && createTime != state.ProcessCreateTimeMS {
		return state, false
	}
	if name, err := proc.Name(); err == nil && strings.TrimSpace(name) != "" {
		state.ProcessName = name
	}
	if createTime > 0 {
		state.ProcessCreateTimeMS = createTime
	}
	if !m.configPortMatchesState(state) {
		return state, false
	}
	return state, true
}

// configPortMatchesState checks whether the persisted process belongs to the manager's configured port.
// state is the loaded runtime state, and the method returns true when the service identity is compatible.
func (m *Manager) configPortMatchesState(state RuntimeState) bool {
	_, port := serviceIdentity(m.config)
	return port == 0 || state.GamePort == 0 || state.GamePort == port
}

// runtimeStateFromConfig creates the base state for the current manager configuration.
// It does not take parameters, and it returns a state populated with service identity and command metadata.
func (m *Manager) runtimeStateFromConfig() RuntimeState {
	serviceName, port := serviceIdentity(m.config)
	state := RuntimeState{
		ServiceName:      serviceName,
		GamePort:         port,
		LaunchGeneration: launchGeneration(m.config),
		WorkDir:          strings.TrimSpace(m.config.WorkDir),
		StartCommand:     strings.TrimSpace(m.config.StartCommand),
		LogPath:          m.logPath(),
	}
	if m.config != nil && m.config.LaunchProfile != nil {
		state.ServerInstanceID = strings.TrimSpace(m.config.LaunchProfile.ServerInstanceID)
		state.DeclaredPorts = append([]model.LaunchDeclaredPort(nil), m.config.LaunchProfile.Ports...)
		state.WorkDir = strings.TrimSpace(m.config.LaunchProfile.WorkDir)
		state.StartCommand = ""
	}
	return state
}

// launchProfileGamePort returns the primary declared game port from a launch profile.
// profile contains declared ports, and the function returns the game port, first declared port, or zero when absent.
func launchProfileGamePort(profile *model.LaunchProfile) int {
	if profile == nil {
		return 0
	}
	for _, port := range profile.Ports {
		if strings.EqualFold(strings.TrimSpace(port.Name), "game") {
			return port.Port
		}
	}
	if len(profile.Ports) > 0 {
		return profile.Ports[0].Port
	}
	return 0
}

// launchGeneration returns the configured launch profile generation.
// config contains optional launch profile data, and the function returns zero for legacy configurations.
func launchGeneration(config *model.ServerConfig) uint64 {
	if config == nil || config.LaunchProfile == nil {
		return 0
	}
	return config.LaunchProfile.LaunchGeneration
}

// recordStartFailure persists one startup failure and applies the local failure threshold.
// summary is the sanitized failure reason and exitCode is optional process exit metadata; the method returns an error when the state cannot be written.
func (m *Manager) recordStartFailure(summary string, exitCode *int) error {
	state, err := m.readRuntimeState()
	if err != nil {
		state = m.runtimeStateFromConfig()
	}
	if strings.TrimSpace(state.ServiceName) == "" {
		state = m.runtimeStateFromConfig()
	}
	state.State = runtimeStateCrashed
	state.PID = 0
	state.ConsecutiveStartFailures++
	state.LastError = strings.TrimSpace(summary)
	state.LastExitCode = exitCode
	if strings.TrimSpace(state.LogPath) == "" {
		state.LogPath = m.logPath()
	}
	state.LastLogTail = tailLogLines(state.LogPath, 20)
	if state.ConsecutiveStartFailures >= startFailureThreshold {
		state.State = runtimeStateBlocked
	}
	return m.writeRuntimeState(state)
}

// processCreateTimeMS reads the creation time for a running process.
// pid identifies the process, and the function returns the creation timestamp in milliseconds or zero when unavailable.
func processCreateTimeMS(pid int) int64 {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0
	}
	createTime, err := proc.CreateTime()
	if err != nil {
		return 0
	}
	return createTime
}

// processName reads the operating-system process name for a running process.
// pid identifies the process, and the function returns the process name or an empty string when unavailable.
func processName(pid int) string {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return ""
	}
	name, err := proc.Name()
	if err != nil {
		return ""
	}
	return name
}

// exitCodeFromError extracts a process exit code when Go exposes one.
// err is the wait result, and the function returns a pointer to the exit code or nil when the value is unavailable.
func exitCodeFromError(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	type exitCoder interface {
		ExitCode() int
	}
	if exitErr, ok := err.(exitCoder); ok {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

// shellCommand builds an OS-specific shell command for generic startup commands.
// command is the exact plugin-provided startup command, and the function returns the shell executable and arguments.
func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}

// tailLogLines reads the last bounded lines from a process log file.
// path identifies the local log file and maxLines limits the returned lines, and the function returns an empty slice when the log is unavailable.
func tailLogLines(path string, maxLines int) []string {
	if strings.TrimSpace(path) == "" || maxLines <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil
	}
	const maxTailBytes int64 = 64 * 1024
	offset := info.Size() - maxTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			trimmed = append(trimmed, line)
		}
	}
	if len(trimmed) > maxLines {
		trimmed = trimmed[len(trimmed)-maxLines:]
	}
	return trimmed
}
