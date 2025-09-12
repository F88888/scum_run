package main

import (
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"scum_run/config"
	"scum_run/internal/client"
	"scum_run/internal/logger"
	"scum_run/internal/steam"
)

func main() {
	var (
		configFile = flag.String("config", "config.json", "Configuration file path")
		token      = flag.String("token", "", "Server authentication token")
		serverAddr = flag.String("server", "", "Server WebSocket address")
	)
	flag.Parse()

	// Initialize logger
	logger := logger.New()

	// Load configuration
	cfg, err := config.Load(*configFile)

	// 如果默认配置文件加载失败，尝试查找同目录下的配置文件
	if err != nil && *configFile == "config.json" {
		logger.Info("Default config file not found, searching for packaged config...")

		// 尝试查找与可执行文件同名的配置文件
		exePath, err2 := os.Executable()
		if err2 == nil {
			exeDir := filepath.Dir(exePath)
			exeName := strings.TrimSuffix(filepath.Base(exePath), filepath.Ext(exePath))

			// 查找可能的配置文件
			possibleConfigs := []string{
				filepath.Join(exeDir, exeName+"_config.json"),
				filepath.Join(exeDir, "config.json"),
			}

			for _, configPath := range possibleConfigs {
				if _, err2 := os.Stat(configPath); err2 == nil {
					logger.Info("Found config file: %s", configPath)
					cfg, err = config.Load(configPath)
					if err == nil {
						break
					}
				}
			}
		}
	}

	// 如果仍然无法加载配置，退出程序
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	// Override config with command line arguments
	if *token != "" {
		cfg.Token = *token
	}
	if *serverAddr != "" {
		cfg.ServerAddr = *serverAddr
	}

	// Validate required configuration
	if cfg.Token == "" {
		logger.Error("Token is required")
		os.Exit(1)
	}
	if cfg.ServerAddr == "" {
		logger.Error("Server address is required")
		os.Exit(1)
	}

	// Detect Steam directory if not specified in config
	steamDir := cfg.SteamDir
	if steamDir == "" {
		logger.Info("Steam directory not specified in config, attempting auto-detection...")
		steamDetector := steam.NewDetector(logger)
		detectedSteamDir := steamDetector.DetectSteamDirectory()
		if detectedSteamDir == "" {
			logger.Error("Failed to detect Steam directory. Please specify steam_dir in config.json")
			os.Exit(1)
		}
		steamDir = detectedSteamDir
		cfg.SteamDir = steamDir
	}

	logger.Info("Steam directory: %s", steamDir)

	// Initialize SCUM client
	scumClient := client.New(cfg, steamDir, logger)

	// Setup graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		logger.Info("Shutting down gracefully...")
		scumClient.Stop()
		os.Exit(0)
	}()

	// Start the client
	logger.Info("Starting SCUM Run client...")
	if err := scumClient.Start(); err != nil {
		logger.Error("Failed to start client: %v", err)
		os.Exit(1)
	}

	// Keep the application running
	select {}
}
