package model

// ServerConfig 服务器配置结构体
type ServerConfig struct {
	ExecPath       string // 可执行文件路径
	GamePort       int    // 游戏端口
	MaxPlayers     int    // 最大玩家数
	EnableBattlEye bool   // 是否启用BattlEye
	ServerIP       string // 服务器IP
	AdditionalArgs string // 额外参数
}
