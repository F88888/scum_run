package config

import (
	"embed"
	"encoding/json"
	"os"
)

//go:embed config.json
var embeddedConfig embed.FS

// AutoInstallConfig holds auto installation configuration
type AutoInstallConfig struct {
	Enabled               bool   `json:"enabled"`                  // 是否启用自动安装
	InstallPath           string `json:"install_path,omitempty"`   // SCUM 服务器安装路径
	SteamCmdPath          string `json:"steamcmd_path,omitempty"`  // SteamCmd 路径
	ForceReinstall        bool   `json:"force_reinstall"`          // 是否强制重新安装
	InstallTimeout        int    `json:"install_timeout"`          // 安装超时时间（秒）
	AutoStartAfterInstall bool   `json:"auto_start_after_install"` // 安装完成后是否自动启动服务器
	AutoStartAfterConfig  bool   `json:"auto_start_after_config"`  // 配置同步后是否自动启动服务器
}

// Config holds the configuration for the SCUM Run client
type Config struct {
	// Token 是客户端连接服务端 WebSocket 时使用的认证令牌。
	Token string `json:"token"`
	// ServerAddr 是服务端 WebSocket 地址。
	ServerAddr string `json:"server_addr"`
	// SteamDir 是本地 SCUM 服务器安装目录。
	SteamDir string `json:"steam_dir,omitempty"`
	// LogLevel 是客户端运行日志级别。
	LogLevel string `json:"log_level"`
	// AutoInstall 是 SCUM 服务器自动安装相关配置。
	AutoInstall AutoInstallConfig `json:"auto_install"`
}

// Load loads configuration from embedded config or external file
func Load(filename string) (*Config, error) {
	// 首先尝试使用嵌入的配置
	var err error
	var data []byte
	configText, _ := embeddedConfig.ReadFile("config.json")
	if len(configText) > 0 {
		data = configText
	} else {
		// 如果没有嵌入配置，尝试读取外部文件
		if data, err = os.ReadFile(filename); err != nil {
			return nil, err
		}
	}

	// 解析配置文件
	var cfg Config
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save saves configuration to a JSON file
func (c *Config) Save(filename string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}
