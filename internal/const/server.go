package _const

// 服务器相关常量
const (
	// SCUM 服务器默认配置
	DefaultGamePort       = 7779
	DefaultMaxPlayers     = 128
	DefaultEnableBattlEye = true
	DefaultServerIP       = "0.0.0.0"
	DefaultAdditionalArgs = ""

	// 服务器状态
	ServerStatusOffline  = "offline"
	ServerStatusOnline   = "online"
	ServerStatusStarting = "starting"
	ServerStatusStopping = "stopping"

	// 安装状态
	InstallStatusPending     = "pending"
	InstallStatusDownloading = "downloading"
	InstallStatusInstalling  = "installing"
	InstallStatusInstalled   = "installed"
	InstallStatusFailed      = "failed"
)
