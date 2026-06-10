package hostagent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultPollInterval      = 2 * time.Second
	defaultRequestTimeout    = 15 * time.Second
)

var (
	compiledServerURL         string
	compiledRegistrationToken string
	compiledAgentID           string
	compiledVersion           string
)

// Config 表示 scum_run host agent 模式的运行配置。
type Config struct {
	// ServerURL 是 scum_server 控制面的基础地址。
	ServerURL string
	// RegistrationToken 是 host agent 注册时使用的注册令牌。
	RegistrationToken string
	// AgentID 是 host agent 的稳定唯一标识。
	AgentID string
	// DisplayName 是 host agent 的展示名称。
	DisplayName string
	// Version 是 host agent 上报给 scum_server 的版本号。
	Version string
	// Address 是 host agent 上报的地址或节点标识。
	Address string
	// DatabasePath 是本地 SCUM.db 的绝对路径；为空时会尝试自动探测。
	DatabasePath string
	// SteamDir 是显式指定的 Steam 根目录；当 DatabasePath 为空时用于推导数据库路径。
	SteamDir string
	// HeartbeatInterval 是向 scum_server 上报心跳的时间间隔。
	HeartbeatInterval time.Duration
	// PollInterval 是轮询数据库操作队列的时间间隔。
	PollInterval time.Duration
	// RequestTimeout 是单次 HTTP 请求的超时时间。
	RequestTimeout time.Duration
}

// ModeEnabled reports whether scum_run should start in host-agent mode.
// It takes no parameters, compiled defaults are considered alongside environment variables, and it returns true when host-agent startup information is available.
func ModeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SCUM_RUN_MODE")), "host-agent") ||
		strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_SERVER_URL")) != "" ||
		strings.TrimSpace(compiledServerURL) != ""
}

// LoadConfigFromEnv loads host-agent mode configuration from environment variables and compiled defaults.
// It takes no parameters, environment variables override compiled-in values when both exist, and it returns the parsed config or a validation error when required values are still missing.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		ServerURL:         strings.TrimRight(firstNonEmpty(strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_SERVER_URL")), compiledServerURL), "/"),
		RegistrationToken: firstNonEmpty(strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_REGISTRATION_TOKEN")), compiledRegistrationToken),
		AgentID:           firstNonEmpty(strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_ID")), compiledAgentID),
		DisplayName:       strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_DISPLAY_NAME")),
		Version:           firstNonEmpty(strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_VERSION")), compiledVersion),
		Address:           strings.TrimSpace(os.Getenv("SCUM_HOST_AGENT_ADDRESS")),
		DatabasePath:      strings.TrimSpace(os.Getenv("SCUM_RUN_DATABASE_PATH")),
		SteamDir:          strings.TrimSpace(os.Getenv("SCUM_RUN_STEAM_DIR")),
		HeartbeatInterval: envDuration("SCUM_HOST_AGENT_HEARTBEAT_INTERVAL", defaultHeartbeatInterval),
		PollInterval:      envDuration("SCUM_HOST_AGENT_POLL_INTERVAL", defaultPollInterval),
		RequestTimeout:    envDuration("SCUM_HOST_AGENT_REQUEST_TIMEOUT", defaultRequestTimeout),
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = cfg.AgentID
	}
	if cfg.Version == "" {
		cfg.Version = "host-agent-dev"
	}
	if cfg.Address == "" {
		hostname, err := os.Hostname()
		if err == nil && strings.TrimSpace(hostname) != "" {
			cfg.Address = hostname
		}
	}
	return cfg, cfg.Validate()
}

// Validate checks whether the host-agent configuration is complete and internally consistent.
// c is the parsed host-agent config, and the method returns nil when valid or an error describing the invalid fields.
func (c Config) Validate() error {
	var errs []error
	if c.ServerURL == "" {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_SERVER_URL is required"))
	}
	if c.RegistrationToken == "" {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_REGISTRATION_TOKEN is required"))
	}
	if c.AgentID == "" {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_ID is required"))
	}
	if c.Address == "" {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_ADDRESS is required"))
	}
	if c.HeartbeatInterval <= 0 {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_HEARTBEAT_INTERVAL must be positive"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_POLL_INTERVAL must be positive"))
	}
	if c.RequestTimeout <= 0 {
		errs = append(errs, errors.New("SCUM_HOST_AGENT_REQUEST_TIMEOUT must be positive"))
	}
	return errors.Join(errs...)
}

// firstNonEmpty returns the first non-empty trimmed string from the provided candidates.
// values is the candidate string list, it does not take additional parameters, and the function returns the first usable value or an empty string when every candidate is blank.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// envDuration parses one duration environment variable with a fallback value.
// key identifies the environment variable, fallback is the default duration, and the function returns the parsed duration or the fallback when unset or invalid.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

// ResolveDatabasePath decides which local SCUM database path the host agent should use.
// c contains explicit database path and optional Steam directory, and the function returns the resolved database path or an error when it cannot be determined.
func (c Config) ResolveDatabasePath() (string, error) {
	if c.DatabasePath != "" {
		return c.DatabasePath, nil
	}
	if c.SteamDir != "" {
		return databasePathFromSteamDir(c.SteamDir), nil
	}
	return "", fmt.Errorf("SCUM_RUN_DATABASE_PATH or SCUM_RUN_STEAM_DIR is required for host-agent mode")
}
