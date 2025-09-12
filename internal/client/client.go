package client

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"scum_run/config"
	"scum_run/internal/database"
	"scum_run/internal/logger"
	"scum_run/internal/logmonitor"
	"scum_run/internal/process"
	"scum_run/internal/steam"
	"scum_run/internal/steamtools"
	"scum_run/internal/websocket_client"
)

// Client represents the SCUM Run client
type Client struct {
	config     *config.Config
	steamDir   string
	logger     *logger.Logger
	wsClient   *websocket_client.Client
	db         *database.Client
	logMonitor *logmonitor.Monitor
	process    *process.Manager
	steamTools *steamtools.Manager
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// Message types for WebSocket communication
const (
	MsgTypeAuth             = "auth"
	MsgTypeServerStart      = "server_start"
	MsgTypeServerStop       = "server_stop"
	MsgTypeServerRestart    = "server_restart"
	MsgTypeServerStatus     = "server_status"
	MsgTypeDBQuery          = "db_query"
	MsgTypeLogUpdate        = "log_update"
	MsgTypeHeartbeat        = "heartbeat"
	MsgTypeSteamToolsStatus = "steamtools_status"
	MsgTypeConfigSync       = "config_sync"       // 配置同步
	MsgTypeConfigUpdate     = "config_update"     // 配置更新
	MsgTypeInstallServer    = "install_server"    // 安装服务器
	MsgTypeDownloadSteamCmd = "download_steamcmd" // 下载SteamCmd
)

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
	Type    string      `json:"type"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Success bool        `json:"success"`
}

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

// New creates a new SCUM Run client
func New(cfg *config.Config, steamDir string, logger *logger.Logger) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	steamDetector := steam.NewDetector(logger)

	return &Client{
		config:     cfg,
		steamDir:   steamDir,
		logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
		db:         database.New(steamDetector.GetSCUMDatabasePath(steamDir), logger),
		process:    process.New(steamDetector.GetSCUMServerPath(steamDir), logger),
		steamTools: steamtools.New(&cfg.SteamTools, logger),
	}
}

// Start starts the client
func (c *Client) Start() error {
	// Note: Steam++ will be started based on server configuration after authentication
	// This allows for dynamic Steam++ management based on server requirements

	// Connect to WebSocket server
	u, err := url.Parse(c.config.ServerAddr)
	if err != nil {
		return fmt.Errorf("invalid server address: %w", err)
	}

	c.wsClient = websocket_client.New(u.String(), c.logger)
	if err := c.wsClient.Connect(); err != nil {
		return fmt.Errorf("failed to connect to WebSocket server: %w", err)
	}

	// Authenticate
	authMsg := WebSocketMessage{
		Type: MsgTypeAuth,
		Data: map[string]string{
			"token": c.config.Token,
		},
	}
	if err := c.wsClient.SendMessage(authMsg); err != nil {
		return fmt.Errorf("failed to send authentication: %w", err)
	}

	// Request configuration sync after authentication
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		// Wait a bit for authentication to complete
		time.Sleep(2 * time.Second)
		c.requestConfigSync()
	}()

	// Start message handler
	c.wg.Add(1)
	go c.handleMessages()

	// Start heartbeat
	c.wg.Add(1)
	go c.heartbeat()

	// Check if SCUM server is installed before initializing database and log monitor
	steamDetector := steam.NewDetector(c.logger)
	if !steamDetector.IsSCUMServerInstalled(c.steamDir) {
		c.logger.Warn("SCUM Dedicated Server is not installed")
		c.logger.Info("Please install SCUM Dedicated Server first, or use the web interface to install it")
		c.logger.Info("Database and log monitoring will be initialized when server is installed")
	} else {
		c.logger.Info("SCUM Dedicated Server is installed, initializing components...")

		// Initialize database connection and set WAL mode
		if steamDetector.IsSCUMDatabaseAvailable(c.steamDir) {
			c.logger.Info("Initializing SCUM database connection...")
			if err := c.db.Initialize(); err != nil {
				c.logger.Warn("Failed to initialize database on startup: %v", err)
				c.logger.Info("Database will be initialized when server starts")
			}
		} else {
			c.logger.Info("SCUM database not found, will be created when server starts")
		}

		// Initialize log monitor
		if steamDetector.IsSCUMLogsDirectoryAvailable(c.steamDir) {
			logsPath := steamDetector.GetSCUMLogsPath(c.steamDir)
			c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
			if err := c.logMonitor.Start(); err != nil {
				c.logger.Warn("Failed to start log monitor: %v", err)
			}
		} else {
			c.logger.Info("SCUM logs directory not found, will be created when server starts")
		}
	}

	c.logger.Info("SCUM Run client started successfully")
	return nil
}

// Stop stops the client
func (c *Client) Stop() {
	c.logger.Info("Stopping SCUM Run client...")

	c.cancel()

	if c.logMonitor != nil {
		c.logMonitor.Stop()
	}

	if c.process != nil {
		c.process.Stop()
	}

	if c.db != nil {
		c.db.Close()
	}

	if c.wsClient != nil {
		c.wsClient.Close()
	}

	// Stop Steam++ last
	if c.steamTools != nil {
		if err := c.steamTools.Stop(); err != nil {
			c.logger.Warn("Failed to stop Steam++: %v", err)
		}
	}

	c.wg.Wait()
	c.logger.Info("SCUM Run client stopped")
}

// handleMessages handles incoming WebSocket messages
func (c *Client) handleMessages() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			var msg WebSocketMessage
			if err := c.wsClient.ReadMessage(&msg); err != nil {
				c.logger.Error("Failed to read WebSocket message: %v", err)
				time.Sleep(time.Second)
				continue
			}

			c.handleMessage(msg)
		}
	}
}

// handleMessage handles a single WebSocket message
func (c *Client) handleMessage(msg WebSocketMessage) {
	c.logger.Debug("Received message: %s", msg.Type)

	switch msg.Type {
	case MsgTypeAuth:
		c.handleAuthResponse(msg)
	case MsgTypeHeartbeat:
		c.handleHeartbeatResponse(msg)
	case MsgTypeServerStart:
		c.handleServerStart()
	case MsgTypeServerStop:
		c.handleServerStop()
	case MsgTypeServerRestart:
		c.handleServerRestart()
	case MsgTypeServerStatus:
		c.handleServerStatus()
	case MsgTypeDBQuery:
		c.handleDBQuery(msg.Data)
	case MsgTypeSteamToolsStatus:
		c.handleSteamToolsStatus()
	case MsgTypeConfigSync:
		c.handleConfigSync(msg.Data)
	case MsgTypeConfigUpdate:
		c.handleConfigUpdate(msg.Data)
	case MsgTypeInstallServer:
		c.handleInstallServer(msg.Data)
	case MsgTypeDownloadSteamCmd:
		c.handleDownloadSteamCmd(msg.Data)
	default:
		c.logger.Warn("Unknown message type: %s", msg.Type)
	}
}

// handleServerStart handles server start request
func (c *Client) handleServerStart() {
	c.logger.Info("Starting SCUM server...")

	// Check if SCUM server is installed before attempting to start
	steamDetector := steam.NewDetector(c.logger)
	if !steamDetector.IsSCUMServerInstalled(c.steamDir) {
		c.sendResponse(MsgTypeServerStart, nil, "SCUM Dedicated Server is not installed. Please install it first.")
		return
	}

	// Ensure database connection is initialized and set WAL mode before starting server
	c.logger.Info("Ensuring SCUM database connection is ready...")
	if err := c.db.Initialize(); err != nil {
		c.sendResponse(MsgTypeServerStart, nil, fmt.Sprintf("Failed to initialize database: %v", err))
		return
	}

	// Initialize log monitor if not already done
	if c.logMonitor == nil && steamDetector.IsSCUMLogsDirectoryAvailable(c.steamDir) {
		logsPath := steamDetector.GetSCUMLogsPath(c.steamDir)
		c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
		if err := c.logMonitor.Start(); err != nil {
			c.logger.Warn("Failed to start log monitor: %v", err)
		}
	}

	if err := c.process.Start(); err != nil {
		c.sendResponse(MsgTypeServerStart, nil, fmt.Sprintf("Failed to start server: %v", err))
		return
	}

	c.sendResponse(MsgTypeServerStart, map[string]interface{}{
		"status": "started",
		"pid":    c.process.GetPID(),
	}, "")
}

// handleServerStop handles server stop request
func (c *Client) handleServerStop() {
	c.logger.Info("Stopping SCUM server...")

	if err := c.process.Stop(); err != nil {
		c.sendResponse(MsgTypeServerStop, nil, fmt.Sprintf("Failed to stop server: %v", err))
		return
	}

	c.sendResponse(MsgTypeServerStop, map[string]interface{}{
		"status": "stopped",
	}, "")
}

// handleServerRestart handles server restart request
func (c *Client) handleServerRestart() {
	c.logger.Info("Restarting SCUM server...")

	// Stop first
	if err := c.process.Stop(); err != nil {
		c.logger.Warn("Failed to stop server gracefully: %v", err)
	}

	// Wait a moment for cleanup
	time.Sleep(2 * time.Second)

	// Start again
	if err := c.process.Start(); err != nil {
		c.sendResponse(MsgTypeServerRestart, nil, fmt.Sprintf("Failed to restart server: %v", err))
		return
	}

	c.sendResponse(MsgTypeServerRestart, map[string]interface{}{
		"status": "restarted",
		"pid":    c.process.GetPID(),
	}, "")
}

// handleServerStatus handles server status request
func (c *Client) handleServerStatus() {
	status := map[string]interface{}{
		"running": c.process.IsRunning(),
		"pid":     c.process.GetPID(),
	}

	c.sendResponse(MsgTypeServerStatus, status, "")
}

// handleSteamToolsStatus handles Steam++ status request
func (c *Client) handleSteamToolsStatus() {
	status := c.steamTools.GetStatus()
	c.sendResponse(MsgTypeSteamToolsStatus, status, "")
}

// handleDBQuery handles database query request
func (c *Client) handleDBQuery(data interface{}) {
	queryData, ok := data.(map[string]interface{})
	if !ok {
		c.sendResponse(MsgTypeDBQuery, nil, "Invalid query data format")
		return
	}

	query, ok := queryData["query"].(string)
	if !ok {
		c.sendResponse(MsgTypeDBQuery, nil, "Missing or invalid query")
		return
	}

	result, err := c.db.Query(query)
	if err != nil {
		c.sendResponse(MsgTypeDBQuery, nil, fmt.Sprintf("Query failed: %v", err))
		return
	}

	c.sendResponse(MsgTypeDBQuery, result, "")
}

// onLogUpdate handles log file updates
func (c *Client) onLogUpdate(filename string, lines []string) {
	logData := map[string]interface{}{
		"filename":  filename,
		"lines":     lines,
		"timestamp": time.Now().Unix(),
	}

	c.sendResponse(MsgTypeLogUpdate, logData, "")
}

// heartbeat sends periodic heartbeat messages
func (c *Client) heartbeat() {
	defer c.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			heartbeatMsg := WebSocketMessage{
				Type: MsgTypeHeartbeat,
				Data: map[string]interface{}{
					"timestamp":         time.Now().Unix(),
					"steamtools_status": c.steamTools.GetStatus(),
				},
				Success: true,
			}

			if err := c.wsClient.SendMessage(heartbeatMsg); err != nil {
				c.logger.Error("Failed to send heartbeat: %v", err)
			}
		}
	}
}

// sendResponse sends a response message to the server
func (c *Client) sendResponse(msgType string, data interface{}, errorMsg string) {
	response := WebSocketMessage{
		Type:    msgType,
		Data:    data,
		Success: errorMsg == "",
	}

	if errorMsg != "" {
		response.Error = errorMsg
	}

	if err := c.wsClient.SendMessage(response); err != nil {
		c.logger.Error("Failed to send response: %v", err)
	}
}

// requestConfigSync requests configuration sync from server
func (c *Client) requestConfigSync() {
	syncMsg := WebSocketMessage{
		Type: MsgTypeConfigSync,
		Data: map[string]interface{}{
			"request_config": true,
		},
	}
	if err := c.wsClient.SendMessage(syncMsg); err != nil {
		c.logger.Error("Failed to request config sync: %v", err)
	} else {
		c.logger.Info("Requested configuration sync from server")
	}
}

// handleConfigSync handles configuration sync from server
func (c *Client) handleConfigSync(data interface{}) {
	configData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid config sync data format")
		return
	}

	c.logger.Info("Received configuration sync from server")
	c.updateServerConfig(configData)
}

// handleAuthResponse handles authentication response from server
func (c *Client) handleAuthResponse(msg WebSocketMessage) {
	if msg.Success {
		c.logger.Info("Authentication successful")
		if data, ok := msg.Data.(map[string]interface{}); ok {
			if serverName, exists := data["server_name"]; exists {
				c.logger.Info("Connected to server: %v", serverName)
			}
		}
	} else {
		c.logger.Error("Authentication failed: %s", msg.Error)
	}
}

// handleHeartbeatResponse handles heartbeat response from server
func (c *Client) handleHeartbeatResponse(msg WebSocketMessage) {
	if msg.Success {
		c.logger.Debug("Heartbeat acknowledged by server")
	} else {
		c.logger.Warn("Heartbeat failed: %s", msg.Error)
	}
}

// handleConfigUpdate handles configuration updates from server
func (c *Client) handleConfigUpdate(data interface{}) {
	configData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid config update data format")
		return
	}

	c.logger.Info("Received configuration update from server")
	c.updateServerConfig(configData)
}

// updateServerConfig updates the local server configuration
func (c *Client) updateServerConfig(configData map[string]interface{}) {
	config := &process.ServerConfig{}

	if installPath, ok := configData["install_path"].(string); ok && installPath != "" {
		config.ExecPath = installPath + "\\SCUM\\Binaries\\Win64\\SCUMServer.exe"
	} else {
		// 如果没有配置路径，使用Steam检测的路径
		steamDetector := steam.NewDetector(c.logger)
		config.ExecPath = steamDetector.GetSCUMServerPath(c.steamDir)
	}

	if gamePort, ok := configData["game_port"].(float64); ok {
		config.GamePort = int(gamePort)
	} else {
		config.GamePort = 7779 // 默认端口
	}

	if maxPlayers, ok := configData["max_players"].(float64); ok {
		config.MaxPlayers = int(maxPlayers)
	} else {
		config.MaxPlayers = 128 // 默认最大玩家数
	}

	if enableBattlEye, ok := configData["enable_battleye"].(bool); ok {
		config.EnableBattlEye = enableBattlEye
	}

	if serverIP, ok := configData["server_ip"].(string); ok {
		config.ServerIP = serverIP
	}

	if additionalArgs, ok := configData["additional_args"].(string); ok {
		config.AdditionalArgs = additionalArgs
	}

	// 检查是否需要启动Steam++
	if installSteamPlus, ok := configData["install_steam_plus"].(bool); ok && installSteamPlus {
		if !c.config.SteamTools.Enabled {
			c.logger.Info("服务器配置要求启动 Steam++，正在启用...")
			c.config.SteamTools.Enabled = true
			if err := c.steamTools.Start(); err != nil {
				c.logger.Warn("Steam++ 启动失败: %v", err)
			} else {
				c.logger.Info("Steam++ 已启动并配置完成")
			}
		}
	}

	// 更新进程管理器配置
	if c.process != nil {
		c.process.UpdateConfig(config)
		c.logger.Info("Updated server configuration - Path: %s, Port: %d, MaxPlayers: %d, BattlEye: %v",
			config.ExecPath, config.GamePort, config.MaxPlayers, config.EnableBattlEye)
	} else {
		// 如果进程管理器还未创建，则创建一个新的
		c.process = process.NewWithConfig(config, c.logger)
		c.logger.Info("Created new process manager with server configuration")
	}

	// 发送配置更新确认
	response := WebSocketMessage{
		Type:    MsgTypeConfigUpdate,
		Success: true,
		Data: map[string]interface{}{
			"config_updated": true,
			"current_config": map[string]interface{}{
				"exec_path":       config.ExecPath,
				"game_port":       config.GamePort,
				"max_players":     config.MaxPlayers,
				"enable_battleye": config.EnableBattlEye,
				"server_ip":       config.ServerIP,
				"additional_args": config.AdditionalArgs,
			},
		},
	}
	c.wsClient.SendMessage(response)
}

// handleInstallServer handles server installation requests
func (c *Client) handleInstallServer(data interface{}) {
	installData, ok := data.(map[string]interface{})
	if !ok {
		c.sendResponse(MsgTypeInstallServer, nil, "Invalid installation data format")
		return
	}

	c.logger.Info("Received server installation request")

	// 获取安装参数
	installPath, _ := installData["install_path"].(string)
	steamCmdPath, _ := installData["steamcmd_path"].(string)
	forceReinstall, _ := installData["force_reinstall"].(bool)

	// 在后台执行安装
	go c.performServerInstallation(installPath, steamCmdPath, forceReinstall)
}

// handleDownloadSteamCmd handles SteamCmd download requests
func (c *Client) handleDownloadSteamCmd(data interface{}) {
	c.logger.Info("Received SteamCmd download request")

	// 在后台执行SteamCmd下载
	go c.performSteamCmdDownload()
}

// performServerInstallation performs the actual server installation
func (c *Client) performServerInstallation(installPath, steamCmdPath string, forceReinstall bool) {
	c.logger.Info("Starting SCUM server installation...")

	// 更新安装状态
	c.sendInstallStatus("downloading", 0, "Starting installation...")

	// 如果没有SteamCmd路径，先下载SteamCmd
	if steamCmdPath == "" {
		c.logger.Info("SteamCmd path not provided, downloading...")
		if err := c.downloadSteamCmd(); err != nil {
			c.sendInstallStatus("failed", 0, fmt.Sprintf("Failed to download SteamCmd: %v", err))
			return
		}
		steamCmdPath = "./steamcmd/steamcmd.exe"
	}

	// 检查SteamCmd是否存在
	if _, err := os.Stat(steamCmdPath); os.IsNotExist(err) {
		c.sendInstallStatus("failed", 0, "SteamCmd not found")
		return
	}

	// 设置安装路径
	if installPath == "" {
		installPath = "./scumserver"
	}

	c.sendInstallStatus("installing", 25, "Installing SCUM server...")

	// 构建SteamCmd命令
	args := []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+app_update", "3792580", "validate",
		"+exit",
	}

	// 执行SteamCmd安装
	cmd := exec.Command(steamCmdPath, args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		c.logger.Error("SteamCmd installation failed: %v, output: %s", err, string(output))
		c.sendInstallStatus("failed", 0, fmt.Sprintf("Installation failed: %v", err))
		return
	}

	c.sendInstallStatus("installed", 100, "SCUM server installation completed successfully")
	c.logger.Info("SCUM server installation completed successfully")
}

// performSteamCmdDownload downloads SteamCmd
func (c *Client) performSteamCmdDownload() {
	if err := c.downloadSteamCmd(); err != nil {
		c.sendResponse(MsgTypeDownloadSteamCmd, nil, fmt.Sprintf("Failed to download SteamCmd: %v", err))
	} else {
		c.sendResponse(MsgTypeDownloadSteamCmd, map[string]interface{}{
			"downloaded": true,
			"path":       "./steamcmd/steamcmd.exe",
		}, "")
	}
}

// downloadSteamCmd downloads and extracts SteamCmd
func (c *Client) downloadSteamCmd() error {
	const steamCmdURL = "https://www.npc0.com/steamcmd.zip"
	const steamCmdDir = "./steamcmd"

	c.logger.Info("Downloading SteamCmd from %s", steamCmdURL)

	// 创建目录
	if err := os.MkdirAll(steamCmdDir, 0755); err != nil {
		return fmt.Errorf("failed to create steamcmd directory: %w", err)
	}

	// 下载文件
	response, err := http.Get(steamCmdURL)
	if err != nil {
		return fmt.Errorf("failed to download steamcmd: %w", err)
	}
	defer response.Body.Close()

	// 创建临时文件
	tempFile := filepath.Join(steamCmdDir, "steamcmd.zip")
	out, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	// 写入文件
	_, err = io.Copy(out, response.Body)
	if err != nil {
		return fmt.Errorf("failed to write steamcmd.zip: %w", err)
	}

	// 解压文件
	if err := c.extractZip(tempFile, steamCmdDir); err != nil {
		return fmt.Errorf("failed to extract steamcmd.zip: %w", err)
	}

	// 删除临时文件
	os.Remove(tempFile)

	c.logger.Info("SteamCmd downloaded and extracted successfully")
	return nil
}

// extractZip extracts a zip file to the specified directory
func (c *Client) extractZip(src, dest string) error {
	c.logger.Info("Extracting %s to %s", src, dest)

	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Create destination directory
	err = os.MkdirAll(dest, 0755)
	if err != nil {
		return err
	}

	// Extract files
	for _, f := range r.File {
		// Clean the file path to prevent directory traversal
		path := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(path, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid file path: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			// Create directory
			err = os.MkdirAll(path, f.FileInfo().Mode())
			if err != nil {
				return err
			}
			continue
		}

		// Create the directories for this file
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}

		// Extract file
		rc, err := f.Open()
		if err != nil {
			return err
		}

		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	c.logger.Info("Zip extraction completed successfully")
	return nil
}

// sendInstallStatus sends installation status to server
func (c *Client) sendInstallStatus(status string, progress int, message string) {
	statusMsg := WebSocketMessage{
		Type:    MsgTypeInstallServer,
		Success: status != "failed",
		Data: map[string]interface{}{
			"status":   status,
			"progress": progress,
			"message":  message,
		},
	}
	c.wsClient.SendMessage(statusMsg)
}
