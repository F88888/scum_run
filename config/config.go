package config

import (
	"encoding/json"
	"os"
)

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

// Config holds the configuration for the SCUM Run client
type Config struct {
	Token      string           `json:"token"`
	ServerAddr string           `json:"server_addr"`
	SteamDir   string           `json:"steam_dir,omitempty"`
	LogLevel   string           `json:"log_level"`
	SteamTools SteamToolsConfig `json:"steam_tools"`
}

// Load loads configuration from a JSON file
func Load(filename string) (*Config, error) {
	// 读取配置文件
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// 解析配置文件
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
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
