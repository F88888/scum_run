package _const

// 配置相关常量
const (
	// 默认配置值
	DefaultInstallPath  = "./scumserver"
	DefaultSteamCmdPath = "./steamcmd/steamcmd.exe"
	DefaultSteamCmdURL  = "https://scum.npc0.com/steamcmd.zip"
	DefaultDirectxURL   = "https://scum.npc0.com/directx_Jun2010_redist.exe"
	DefaultVisualCURL   = "https://scum.npc0.com/VC_redist.x64.exe"
	DefaultSteamCmdDir  = "./steamcmd"

	// 运行时依赖相关常量
	RuntimeTempDir    = "scum_runtime"       // 运行时临时目录名
	DirectXExtractDir = "directx_extract"    // DirectX 解压目录名
	VCRedistFilename  = "vc_redist.x64.exe"  // Visual C++ Redistributable 文件名
	DirectXRedistFile = "directx_redist.exe" // DirectX 安装文件名
	DirectXSetupExe   = "DXSETUP.exe"        // DirectX 安装程序名
	DirectXCheckDll   = "d3dx9_43.dll"       // DirectX 检查文件
	WindowsSystem32   = "System32"           // Windows 系统目录
	VCRedistWaitTime  = 2                    // VC++ 安装等待时间（秒）
	DirectXWaitTime   = 3                    // DirectX 安装等待时间（秒）

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

// Visual C++ Redistributable 注册表路径
const (
	VCRedistRegistryPath1 = `SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64`
	VCRedistRegistryPath2 = `SOFTWARE\Wow6432Node\Microsoft\VisualStudio\14.0\VC\Runtimes\x64`
)

// 安装参数常量
const (
	VCInstallArgInstall     = "/install"
	VCInstallArgQuiet       = "/quiet"
	VCInstallArgNoRestart   = "/norestart"
	DirectXExtractArgQ      = "/Q"
	DirectXExtractArgT      = "/T:"
	DirectXInstallArgSilent = "/silent"
)

// 注册表查询常量
const (
	RegistryHKLMPrefix = "HKLM\\"
	RegistryQueryV     = "/v"
	RegistryVersionKey = "Version"
)
