package _const

// 配置相关常量
const (
	// 默认配置值
	DefaultInstallPath     = "./scumserver"
	DefaultSteamCmdPath    = "./steamcmd/steamcmd.exe"
	DefaultSteamCmdURL     = "https://ssl.npc0.com/steamcmd.zip"
	DefaultSteamCmdDir     = "./steamcmd"
	DefaultSteamCmdRelPath = "steamcmd/steamcmd.exe" // 相对路径，不带 ./

	// SCUM 服务器相关常量
	SCUMServerAppID      = "3792580"
	SCUMServerExecutable = "SCUMServer.exe"

	// 自动安装配置
	DefaultAutoInstall    = true
	DefaultForceReinstall = false
	DefaultInstallTimeout = 600 // 10分钟
)
