package model

// LaunchDeclaredPort 启动配置声明端口。
type LaunchDeclaredPort struct {
	// Name 是端口业务名称，例如 game、query 或 rcon。
	Name string `json:"name"`
	// Port 是端口号。
	Port int `json:"port"`
	// Protocol 是端口协议，例如 tcp 或 udp。
	Protocol string `json:"protocol,omitempty"`
}

// LaunchRestartPolicy 启动配置重启策略。
type LaunchRestartPolicy struct {
	// Mode 是自动重启策略，例如 never、on_failure 或 always。
	Mode string `json:"mode"`
	// MaxConsecutiveFailures 是阻断自动重启前允许的连续失败次数。
	MaxConsecutiveFailures uint32 `json:"max_consecutive_failures,omitempty"`
}

// LaunchProfile 是 scum_server 下发的通用启动配置。
type LaunchProfile struct {
	// ServerInstanceID 是目标服务器实例 ID。
	ServerInstanceID string `json:"server_instance_id"`
	// ServiceName 是执行端本地服务名。
	ServiceName string `json:"service_name"`
	// Ports 是声明端口列表。
	Ports []LaunchDeclaredPort `json:"ports"`
	// LaunchGeneration 是期望应用的启动配置代次。
	LaunchGeneration uint64 `json:"launch_generation"`
	// WorkDir 是实例作用域内的相对工作目录。
	WorkDir string `json:"work_dir"`
	// LaunchMode 是启动模式，例如 argv 或 shell。
	LaunchMode string `json:"launch_mode"`
	// Executable 是 argv 模式下的相对可执行文件路径。
	Executable string `json:"executable,omitempty"`
	// Args 是 argv 模式下的启动参数。
	Args []string `json:"args,omitempty"`
	// ShellCommand 是 shell 模式下的命令文本。
	ShellCommand string `json:"shell_command,omitempty"`
	// Env 是有界环境变量集合。
	Env map[string]string `json:"env,omitempty"`
	// RestartPolicy 是执行端自动重启策略。
	RestartPolicy LaunchRestartPolicy `json:"restart_policy"`
}

// ServerConfig 服务器配置结构体。
type ServerConfig struct {
	// ServiceName 是服务实例名称，用于和端口一起区分同一执行器上的多个游戏服务。
	ServiceName string `json:"service_name"`
	// ExecPath 是旧兼容模式下的可执行文件路径，或通用命令模式下的工作目录兜底值。
	ExecPath string `json:"exec_path"`
	// WorkDir 是启动命令的工作目录；为空时按旧 ExecPath 规则推导。
	WorkDir string `json:"work_dir"`
	// StartCommand 是服务器插件下发的完整启动命令，存在时 scum_run 不再拼接游戏专用参数。
	StartCommand string `json:"start_command"`
	// GamePort 是游戏服务监听端口，用于启动前去重和服务状态识别。
	GamePort int `json:"game_port"`
	// MaxPlayers 是旧 SCUM 兼容模式下拼接的最大玩家数。
	MaxPlayers int `json:"max_players"`
	// EnableBattlEye 是旧 SCUM 兼容模式下是否启用 BattlEye。
	EnableBattlEye bool `json:"enable_battleye"`
	// ServerIP 是端口探测使用的服务监听地址。
	ServerIP string `json:"server_ip"`
	// AdditionalArgs 是旧兼容模式下的附加参数；通用服务应优先使用 StartCommand。
	AdditionalArgs string `json:"additional_args"`
	// LaunchProfile 是 scum_server 下发的通用启动配置；存在时优先于旧 SCUM 拼接逻辑。
	LaunchProfile *LaunchProfile `json:"launch_profile,omitempty"`
}
