package request

// ScumServerConfigData SCUM服务器配置数据
type ScumServerConfigData struct {
	InstallPath    string `json:"install_path"`
	GamePort       int    `json:"game_port"`
	MaxPlayers     int    `json:"max_players"`
	EnableBattlEye bool   `json:"enable_battleye"`
	ServerIP       string `json:"server_ip"`
	AdditionalArgs string `json:"additional_args"`
	SteamCmdPath   string `json:"steamcmd_path"`
	AutoUpdate     bool   `json:"auto_update"`
}
