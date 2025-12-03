package _const

// 配置相关常量
const (
	// 默认配置值
	DefaultInstallPath  = "./scumserver"
	DefaultSteamCmdPath = "./steamcmd/steamcmd.exe"
	DefaultSteamCmdURL  = "https://scum.npc0.com/steamcmd.zip"
	DefaultDirectxURL   = "https://scum.npc0.com/directx_Jun2010_redist.exe"
	DefaultSteamCmdDir  = "./steamcmd"

	// SCUM 服务器相关常量
	SCUMServerAppID = "3792580"

	// 日志处理相关常量 - 与服务端保持一致
	LogMaxRatePerSecond = 100 // 每秒最大日志发送数量
	LogRateWindow       = 500 // 日志频率控制窗口 (毫秒, 与服务端一致)
	LogBatchInterval    = 500 // 日志批量发送间隔 (毫秒, 与服务端一致)
	LogBatchSize        = 100 // 日志批量大小 (与服务端一致)
)

// 数据库查询相关常量
const (
	// 在线玩家查询常量
	OnlinePlayerTimeWindow = 60 // 在线玩家时间窗口（秒）
	UserTypeServer         = 2  // 服务器类型用户
	CurrencyTypeMoney      = 1  // 渣币类型
	CurrencyTypeGold       = 2  // 金币类型

	// 载具查询常量
	VehicleClassSuffix = "_ES"                     // 载具类名后缀
	VehicleTimeFormat  = "2006-01-02T15:04:05.000" // 载具时间格式

	// 领地查询常量
	FlagAssetPattern = "%Flag%" // 领地资产模式
	SquadLeaderRank  = 4        // 队长等级
)
