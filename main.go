package main

import (
	"flag"
	"os"
	"os/signal"
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
