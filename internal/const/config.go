package _const

// 配置相关常量
const (
	// 默认配置值
	DefaultInstallPath  = "./scumserver"
	DefaultSteamCmdPath = "./steamcmd/steamcmd.exe"
	DefaultSteamCmdURL  = "https://ssl.npc0.com/steamcmd.zip"
	DefaultSteamCmdDir  = "./steamcmd"

	// SCUM 服务器相关常量
	SCUMServerAppID = "3792580"

	// 日志处理相关常量 - 与服务端保持一致
	LogMaxRatePerSecond = 100 // 每秒最大日志发送数量
	LogRateWindow       = 500 // 日志频率控制窗口 (毫秒, 与服务端一致)
	LogBatchInterval    = 500 // 日志批量发送间隔 (毫秒, 与服务端一致)
	LogBatchSize        = 100 // 日志批量大小 (与服务端一致)

)
