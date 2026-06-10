package request

// ScumServerConfigData SCUM服务器配置数据
type ScumServerConfigData struct {
	// ServiceName 是服务实例名称，用于和端口一起识别本地游戏服务。
	ServiceName string `json:"service_name"`
	// InstallPath 是旧兼容模式下的安装路径或通用命令模式下的工作目录。
	InstallPath string `json:"install_path"`
	// WorkDir 是服务器启动命令执行时使用的工作目录。
	WorkDir string `json:"work_dir"`
	// StartCommand 是服务器插件配置的完整启动命令。
	StartCommand string `json:"start_command"`
	// GamePort 是游戏服务监听端口。
	GamePort int `json:"game_port"`
	// MaxPlayers 是旧 SCUM 兼容配置中的最大玩家数。
	MaxPlayers int `json:"max_players"`
	// EnableBattlEye 是旧 SCUM 兼容配置中的 BattlEye 开关。
	EnableBattlEye bool `json:"enable_battleye"`
	// ServerIP 是端口探测使用的服务监听地址。
	ServerIP string `json:"server_ip"`
	// AdditionalArgs 是旧兼容模式下的附加参数或旧命令行服完整命令。
	AdditionalArgs string `json:"additional_args"`
	// SteamCmdPath 是 SteamCMD 可执行文件路径。
	SteamCmdPath string `json:"steamcmd_path"`
	// AutoUpdate 表示是否允许自动更新服务器。
	AutoUpdate bool `json:"auto_update"`
}
