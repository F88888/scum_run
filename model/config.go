package model

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
}
