package _const

// 配置相关常量
const (
	// 默认配置值
	DefaultInstallPath  = "./scumserver"
	DefaultSteamCmdPath = "./steamcmd/steamcmd.exe"
	DefaultSteamCmdURL  = "https://ssl.npc0.com/steamcmd.zip"
	DefaultSteamCmdDir  = "./steamcmd"

	// SCUM 服务器相关常量
	SCUMServerAppID      = "3792580"
	SCUMServerExecutable = "SCUMServer.exe"

	// 自动安装配置
	DefaultAutoInstall           = true
	DefaultForceReinstall        = false
	DefaultInstallTimeout        = 600  // 10分钟
	DefaultAutoStartAfterInstall = true // 默认安装完成后自动启动
	DefaultAutoStartAfterConfig  = true // 默认配置同步后自动启动

	// WebSocket 相关常量
	WebSocketReadBufferSize  = 4096        // WebSocket 读取缓冲区大小
	WebSocketWriteBufferSize = 4096        // WebSocket 写入缓冲区大小
	WebSocketMaxMessageSize  = 1024 * 1024 // 最大消息大小 (1MB)
	HeartbeatTimeout         = 90          // 心跳超时时间 (秒) - 增加超时时间避免误判

	// 日志处理相关常量
	LogMaxRatePerSecond = 100 // 每秒最大日志发送数量
	LogRateWindow       = 1   // 日志频率控制窗口 (秒)
	LogBatchInterval    = 5   // 日志批量发送间隔 (秒)
	LogBatchSize        = 50  // 日志批量大小
)
