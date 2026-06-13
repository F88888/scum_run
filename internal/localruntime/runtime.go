package localruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"scum_run/internal/database"
	"scum_run/internal/logger"
	"scum_run/internal/process"
	runtimecheck "scum_run/internal/runtime"
	"scum_run/internal/steam"
	"scum_run/model"
)

const (
	// ProcessReasonRuntimeBlocked 表示本地进程状态已经被连续失败保护阻断。
	ProcessReasonRuntimeBlocked = "process.start_blocked"
	// ProcessReasonBinaryMissing 表示本地 SCUM server 可执行文件不存在。
	ProcessReasonBinaryMissing = "process.binary_missing"
	// ProcessReasonPathUnresolved 表示本地 SCUM server 可执行文件路径无法推导。
	ProcessReasonPathUnresolved = "process.path_unresolved"
)

// LocalRuntimeOptions 描述共享本地执行 runtime 的初始化输入。
type LocalRuntimeOptions struct {
	// ScopeRoot 是实例在宿主机上的真实根目录，优先用于解析启动文件和数据库。
	ScopeRoot string
	// SteamDir 是可选的 Steam 根目录，用于推导数据库和服务端可执行文件路径。
	SteamDir string
	// DatabasePath 是可选的本地 SCUM.db 路径；为空时会按 SteamDir 自动推导。
	DatabasePath string
	// ServerPath 是可选的本地 SCUMServer 可执行文件路径；为空时会按 SteamDir 或数据库路径自动推导。
	ServerPath string
}

// ProcessReadiness 描述本地进程生命周期能力的 readiness 摘要。
type ProcessReadiness struct {
	// Supported 表示当前 runtime 是否能声明托管进程生命周期契约。
	Supported bool
	// Ready 表示当前 runtime 是否已经满足启动或查询进程的前置条件。
	Ready bool
	// ReasonCode 是 blocked 或 unsupported 场景下的稳定原因码。
	ReasonCode string
	// Summary 是面向调用方的脱敏 readiness 摘要。
	Summary string
	// Status 是当前安全进程状态快照。
	Status process.Status
}

// LocalRuntime 统一封装本地数据库、进程管理和运行时依赖检查能力。
type LocalRuntime struct {
	// logger 是本地 runtime 使用的日志记录器。
	logger *logger.Logger
	// steamDir 是推导出的 Steam 根目录。
	steamDir string
	// databasePath 是推导出的本地 SCUM.db 路径。
	databasePath string
	// serverPath 是推导出的本地 SCUM server 可执行文件路径。
	serverPath string
	// db 是共享本地数据库客户端。
	db *database.Client
	// process 是共享本地进程管理器。
	process *process.Manager
	// checker 是运行时依赖检查器。
	checker *runtimecheck.Checker
}

// New 构建一个共享本地执行 runtime。
// options 提供 Steam 根目录、数据库路径和服务端可执行文件路径提示，log 负责记录初始化过程。
// 它返回一个包含数据库客户端、进程管理器和运行时检查器的 runtime；当本地数据库路径无法解析时返回错误。
func New(options LocalRuntimeOptions, log *logger.Logger) (*LocalRuntime, error) {
	if log == nil {
		log = logger.New()
	}
	detector := steam.NewDetector(log)
	scopeRoot := strings.TrimSpace(options.ScopeRoot)
	steamDir := strings.TrimSpace(options.SteamDir)
	databasePath := strings.TrimSpace(options.DatabasePath)
	serverPath := strings.TrimSpace(options.ServerPath)

	if err := validateLocalPathHint("SCUM_RUN_SCOPE_ROOT", scopeRoot); err != nil {
		return nil, err
	}
	if err := validateLocalPathHint("SCUM_RUN_DATABASE_PATH", databasePath); err != nil {
		return nil, err
	}
	if err := validateLocalPathHint("SCUM_RUN_SERVER_PATH", serverPath); err != nil {
		return nil, err
	}
	if databasePath == "" && scopeRoot != "" {
		databasePath = filepath.Join(scopeRoot, "SCUM", "Saved", "SaveFiles", "SCUM.db")
	}
	if serverPath == "" && scopeRoot != "" {
		serverPath = filepath.Join(scopeRoot, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	}
	if steamDir == "" && (databasePath == "" || serverPath == "") {
		steamDir = strings.TrimSpace(detector.DetectSteamDirectory())
	}
	if steamDir == "" && databasePath != "" {
		steamDir = inferSteamDirFromDatabasePath(databasePath)
	}
	if databasePath == "" && steamDir != "" {
		databasePath = detector.GetSCUMDatabasePath(steamDir)
	}
	if serverPath == "" && steamDir != "" {
		serverPath = detector.GetSCUMServerPath(steamDir)
	}
	if serverPath == "" && databasePath != "" {
		serverPath = inferServerPathFromDatabasePath(databasePath)
	}
	if databasePath == "" {
		return nil, fmt.Errorf("SCUM database path is required")
	}
	return &LocalRuntime{
		logger:       log,
		steamDir:     steamDir,
		databasePath: databasePath,
		serverPath:   serverPath,
		db:           database.New(databasePath, log),
		process: process.NewWithConfig(&model.ServerConfig{
			ServiceName:    "scum",
			ExecPath:       serverPath,
			WorkDir:        firstNonEmpty(scopeRoot, filepath.Dir(serverPath)),
			GamePort:       7777,
			MaxPlayers:     64,
			EnableBattlEye: true,
			ServerIP:       "0.0.0.0",
		}, log),
		checker: runtimecheck.NewChecker(log),
	}, nil
}

// SteamDir 返回当前 runtime 推导出的 Steam 根目录。
// 它不接收参数，返回当前共享 runtime 使用的 Steam 根目录；如果无法推导则返回空字符串。
func (r *LocalRuntime) SteamDir() string {
	if r == nil {
		return ""
	}
	return r.steamDir
}

// DatabasePath 返回当前 runtime 使用的数据库路径。
// 它不接收参数，返回共享数据库客户端对应的本地 SCUM.db 路径。
func (r *LocalRuntime) DatabasePath() string {
	if r == nil {
		return ""
	}
	return r.databasePath
}

// ServerPath 返回当前 runtime 使用的服务端可执行文件路径。
// 它不接收参数，返回共享进程管理器对应的本地 SCUM server 可执行文件路径；未解析时返回空字符串。
func (r *LocalRuntime) ServerPath() string {
	if r == nil {
		return ""
	}
	return r.serverPath
}

// Database 返回共享数据库客户端。
// 它不接收参数，返回 runtime 持有的 SCUM.db 客户端；调用方可直接复用该对象执行查询。
func (r *LocalRuntime) Database() *database.Client {
	if r == nil {
		return nil
	}
	return r.db
}

// Process 返回共享进程管理器。
// 它不接收参数，返回 runtime 持有的进程管理器；调用方可直接复用该对象执行启动、停止和状态读取。
func (r *LocalRuntime) Process() *process.Manager {
	if r == nil {
		return nil
	}
	return r.process
}

// EnsureRuntimeDependencies 执行本地运行时依赖检查。
// 它不接收额外参数，会使用 runtime 自带检查器执行依赖校验。
// 它返回 nil 表示依赖已满足或当前平台无需检查；若依赖安装或检查失败则返回错误。
func (r *LocalRuntime) EnsureRuntimeDependencies() error {
	if r == nil || r.checker == nil {
		return nil
	}
	return r.checker.CheckAndInstallRuntimes()
}

// ProcessReadiness 返回当前本地进程生命周期能力的 readiness 摘要。
// 它不接收参数，基于推导出的服务端可执行文件路径和本地进程状态返回是否支持、是否 ready 以及稳定原因码。
// 当本地运行环境缺少 SCUM server 或连续失败已触发阻断时，会返回 blocked 或 unsupported 摘要。
func (r *LocalRuntime) ProcessReadiness() ProcessReadiness {
	readiness := ProcessReadiness{}
	if r == nil || r.process == nil {
		readiness.ReasonCode = ProcessReasonPathUnresolved
		readiness.Summary = "本地进程 runtime 未初始化。"
		return readiness
	}
	status := r.process.GetStatus()
	readiness.Status = status
	if strings.TrimSpace(r.serverPath) == "" {
		readiness.ReasonCode = ProcessReasonPathUnresolved
		readiness.Summary = "当前执行端还没有可用的本地服务器启动路径。"
		return readiness
	}
	readiness.Supported = true
	if _, err := os.Stat(r.serverPath); err != nil {
		readiness.ReasonCode = ProcessReasonBinaryMissing
		readiness.Summary = "本地 SCUM server 可执行文件尚未就绪。"
		return readiness
	}
	if status.State == "blocked" {
		readiness.ReasonCode = ProcessReasonRuntimeBlocked
		readiness.Summary = firstNonEmpty(strings.TrimSpace(status.LastError), "本地启动连续失败，当前已进入阻断状态。")
		return readiness
	}
	readiness.Ready = true
	if status.Running {
		readiness.Summary = "本地 SCUM server 正在运行。"
		return readiness
	}
	if status.State == "starting" {
		readiness.Summary = "本地 SCUM server 正在启动。"
		return readiness
	}
	readiness.Summary = "本地 SCUM server 已具备启动条件。"
	return readiness
}

// inferSteamDirFromDatabasePath 根据数据库路径反推 Steam 根目录。
// databasePath 是本地 SCUM.db 路径，函数返回可用于推导其他资源路径的 Steam 根目录；无法识别时返回空字符串。
func inferSteamDirFromDatabasePath(databasePath string) string {
	dbPath := filepath.Clean(strings.TrimSpace(databasePath))
	if dbPath == "" {
		return ""
	}
	saveFilesDir := filepath.Dir(dbPath)
	savedDir := filepath.Dir(saveFilesDir)
	scumDir := filepath.Dir(savedDir)
	if strings.EqualFold(filepath.Base(saveFilesDir), "SaveFiles") && strings.EqualFold(filepath.Base(savedDir), "Saved") && strings.EqualFold(filepath.Base(scumDir), "SCUM") {
		return filepath.Dir(scumDir)
	}
	return ""
}

// inferServerPathFromDatabasePath 根据数据库路径反推 SCUM server 可执行文件路径。
// databasePath 是本地 SCUM.db 路径，函数返回与该数据库目录配套的默认服务器可执行文件路径；无法识别时返回空字符串。
func inferServerPathFromDatabasePath(databasePath string) string {
	steamDir := inferSteamDirFromDatabasePath(databasePath)
	if steamDir == "" {
		return ""
	}
	return filepath.Join(steamDir, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
}

// firstNonEmpty 返回第一个非空字符串。
// values 是候选字符串列表，函数返回第一个去空白后仍非空的值；若全部为空则返回空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// validateLocalPathHint rejects malformed Windows-style volume prefixes before SQLite or process startup sees them.
// name identifies the environment or compiled path source and value is the raw path hint; it returns nil for blank or usable paths and an error for values such as manual:\server or C:server.
func validateLocalPathHint(name string, value string) error {
	path := strings.TrimSpace(value)
	if path == "" {
		return nil
	}
	if hasMalformedWindowsVolume(path) {
		return fmt.Errorf("%s must be a real Windows absolute path such as D:\\scum-run-manual, not a logical ref such as manual:\\server or C:server", name)
	}
	return nil
}

// hasMalformedWindowsVolume detects path prefixes that are invalid or drive-relative on Windows.
// value is a user-supplied path hint, and the function returns true for scheme-like prefixes or drive-relative prefixes that would later become invalid local filenames.
func hasMalformedWindowsVolume(value string) bool {
	path := strings.TrimSpace(value)
	colon := strings.IndexByte(path, ':')
	if colon < 0 {
		return false
	}
	firstSeparator := strings.IndexAny(path, `/\`)
	if firstSeparator >= 0 && colon > firstSeparator {
		return false
	}
	if colon == 1 && isASCIILetter(path[0]) {
		return len(path) < 3 || !isWindowsSeparator(path[2])
	}
	return true
}

// isASCIILetter reports whether b is an English alphabetic byte.
// b is one byte from a path prefix, and the function returns true for A-Z or a-z.
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// isWindowsSeparator reports whether b is a Windows path separator.
// b is one byte from a path string, and the function returns true for slash or backslash.
func isWindowsSeparator(b byte) bool {
	return b == '\\' || b == '/'
}
