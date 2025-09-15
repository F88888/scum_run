package config

import (
	"embed"
	"encoding/json"
	"os"
)

//go:embed config.json
var embeddedConfig embed.FS

// SteamToolsConfig holds Steam++ integration configuration
type SteamToolsConfig struct {
	Enabled         bool     `json:"enabled"`                    // 是否启用 Steam++ 集成
	ExecutablePath  string   `json:"executable_path,omitempty"`  // Steam++ 可执行文件路径
	AutoStart       bool     `json:"auto_start"`                 // 是否自动启动 Steam++
	AutoAccelerate  bool     `json:"auto_accelerate"`            // 是否自动启用网络加速
	WaitTimeout     int      `json:"wait_timeout"`               // 等待 Steam++ 启动的超时时间（秒）
	AccelerateItems []string `json:"accelerate_items,omitempty"` // 要加速的项目列表

	// 自动下载配置
	AutoDownload     bool   `json:"auto_download"`               // 是否自动下载 Steam++
	DownloadUrl      string `json:"download_url"`                // Steam++ 下载地址
	InstallPath      string `json:"install_path,omitempty"`      // 安装路径（留空使用默认路径）
	VerifyChecksum   bool   `json:"verify_checksum"`             // 是否验证文件校验和
	ExpectedChecksum string `json:"expected_checksum,omitempty"` // 期望的文件校验和
}

// AutoInstallConfig holds auto installation configuration
type AutoInstallConfig struct {
	Enabled               bool   `json:"enabled"`                  // 是否启用自动安装
	InstallPath           string `json:"install_path,omitempty"`   // SCUM 服务器安装路径
	SteamCmdPath          string `json:"steamcmd_path,omitempty"`  // SteamCmd 路径
	ForceReinstall        bool   `json:"force_reinstall"`          // 是否强制重新安装
	InstallTimeout        int    `json:"install_timeout"`          // 安装超时时间（秒）
	AutoStartAfterInstall bool   `json:"auto_start_after_install"` // 安装完成后是否自动启动服务器
}

// Config holds the configuration for the SCUM Run client
type Config struct {
	Token       string            `json:"token"`
	ServerAddr  string            `json:"server_addr"`
	SteamDir    string            `json:"steam_dir,omitempty"`
	LogLevel    string            `json:"log_level"`
	SteamTools  SteamToolsConfig  `json:"steam_tools"`
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
