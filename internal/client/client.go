package client

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"scum_run/config"
	_const "scum_run/internal/const"
	"scum_run/internal/database"
	"scum_run/internal/logger"
	"scum_run/internal/logmonitor"
	"scum_run/internal/process"
	"scum_run/internal/steam"
	"scum_run/internal/steamtools"
	"scum_run/internal/websocket_client"
	"scum_run/model"
	"scum_run/model/request"
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
	installing bool       // 安装状态标志
	installMux sync.Mutex // 安装锁
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
	MsgTypeServerUpdate     = "server_update"     // 服务器更新
	MsgTypeScheduledRestart = "scheduled_restart" // 定时重启
)

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
	// Start Steam++ first for network acceleration
	if c.config.SteamTools.Enabled {
		c.logger.Info("正在启动 Steam++ 网络加速...")
		if err := c.steamTools.Start(); err != nil {
			c.logger.Warn("Steam++ 启动失败，继续运行但可能影响 Steam 服务访问: %v", err)
		} else {
			c.logger.Info("Steam++ 启动成功，网络加速已启用")
		}
	}

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
	authMsg := request.WebSocketMessage{
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

	// 检查SCUM服务器是否已安装
	isInstalled := c.checkServerInstallation(steamDetector)

	if !isInstalled {
		c.logger.Warn("SCUM Dedicated Server is not installed")

		// 检查是否启用自动安装
		if c.config.AutoInstall.Enabled {
			c.logger.Info("Auto-install is enabled, starting SCUM server installation...")
			go c.performAutoInstall()
		} else {
			c.logger.Info("Please install SCUM Dedicated Server first, or use the web interface to install it")
		}
		c.logger.Info("Database and log monitoring will be initialized when server is installed")
	} else {
		c.logger.Info("SCUM Dedicated Server is installed, initializing components...")
		c.initializeServerComponents(steamDetector)
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
			var msg request.WebSocketMessage
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
func (c *Client) handleMessage(msg request.WebSocketMessage) {
	c.logger.Debug("Received message: %s", msg.Type)

	switch msg.Type {
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
		// 安装消息已移除，不再处理此消息类型
		c.logger.Debug("Received install_server message (deprecated)")
	case MsgTypeDownloadSteamCmd:
		c.handleDownloadSteamCmd(msg.Data)
	case MsgTypeServerUpdate:
		c.handleServerUpdate(msg.Data)
	case MsgTypeScheduledRestart:
		c.handleScheduledRestart(msg.Data)
	case MsgTypeHeartbeat:
		// Heartbeat messages from server are handled silently
		c.logger.Debug("Received heartbeat from server")
	case MsgTypeAuth:
		// Handle authentication response from server
		c.handleAuthResponse(msg)
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

	// Initialize log monitor if not already done
	if c.logMonitor == nil && steamDetector.IsSCUMLogsDirectoryAvailable(c.steamDir) {
		logsPath := steamDetector.GetSCUMLogsPath(c.steamDir)
		c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
		if err := c.logMonitor.Start(); err != nil {
			c.logger.Warn("Failed to start log monitor: %v", err)
		}
	}

	// Start the server process first
	if err := c.process.Start(); err != nil {
		c.sendResponse(MsgTypeServerStart, nil, fmt.Sprintf("Failed to start server: %v", err))
		return
	}

	// After server starts, try to initialize database connection
	// This is done after server start because the database file is created by SCUM server
	go func() {
		// Wait a bit for the server to create the database file
		time.Sleep(5 * time.Second)
		c.logger.Info("Attempting to initialize database connection after server start...")

		// Check if database is available before trying to initialize
		if c.db.IsAvailable() {
			if err := c.db.Initialize(); err != nil {
				c.logger.Warn("Failed to initialize database after server start: %v", err)
			} else {
				c.logger.Info("Database connection initialized successfully after server start")
			}
		} else {
			c.logger.Info("Database file not yet available, will retry later")
		}
	}()

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
			heartbeatMsg := request.WebSocketMessage{
				Type: MsgTypeHeartbeat,
				Data: map[string]interface{}{
					"timestamp": time.Now().Unix(),
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
	response := request.WebSocketMessage{
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
	syncMsg := request.WebSocketMessage{
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
	config := &model.ServerConfig{}

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
		config.GamePort = _const.DefaultGamePort
	}

	if maxPlayers, ok := configData["max_players"].(float64); ok {
		config.MaxPlayers = int(maxPlayers)
	} else {
		config.MaxPlayers = _const.DefaultMaxPlayers
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

	// 检查是否需要自动启动服务器（仅在配置同步时，而非配置更新时）
	if c.config.AutoInstall.AutoStartAfterConfig {
		steamDetector := steam.NewDetector(c.logger)
		if steamDetector.IsSCUMServerInstalled(c.steamDir) && !c.process.IsRunning() {
			c.logger.Info("Auto-start after config sync is enabled and server is installed, starting SCUM server...")
			go func() {
				// 等待一段时间让配置完全更新
				time.Sleep(2 * time.Second)
				c.handleServerStart()
			}()
		}
	}

	// 发送配置更新确认
	response := request.WebSocketMessage{
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

// handleInstallServer 已移除 - 客户端自动处理安装，不再响应服务器端安装请求

// handleAuthResponse handles authentication response from server
func (c *Client) handleAuthResponse(msg request.WebSocketMessage) {
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

// handleDownloadSteamCmd handles SteamCmd download requests
func (c *Client) handleDownloadSteamCmd(data interface{}) {
	c.logger.Info("Received SteamCmd download request")

	// 在后台执行SteamCmd下载
	go c.performSteamCmdDownload()
}

// performAutoInstall performs automatic SCUM server installation on startup
func (c *Client) performAutoInstall() {
	c.logger.Info("Starting automatic SCUM server installation...")

	// 检查是否已经在安装中
	c.installMux.Lock()
	if c.installing {
		c.installMux.Unlock()
		c.logger.Info("Installation already in progress, skipping auto-install")
		return
	}
	c.installing = true
	c.installMux.Unlock()

	defer func() {
		c.installMux.Lock()
		c.installing = false
		c.installMux.Unlock()
	}()

	// 获取配置参数
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	steamCmdPath := c.config.AutoInstall.SteamCmdPath
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
	}

	forceReinstall := c.config.AutoInstall.ForceReinstall

	// 执行安装
	c.performServerInstallation(installPath, steamCmdPath, forceReinstall)

	// 安装完成后，重新初始化组件
	c.initializeComponentsAfterInstall()
}

// initializeComponentsAfterInstall initializes components after server installation
func (c *Client) initializeComponentsAfterInstall() {
	c.logger.Info("Initializing components after server installation...")

	// 使用安装路径而不是steamDir来验证安装
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	// 转换为绝对路径
	absInstallPath, err := filepath.Abs(installPath)
	if err != nil {
		c.logger.Warn("Failed to get absolute path for install directory: %v", err)
		absInstallPath = installPath
	}

	steamDetector := steam.NewDetector(c.logger)
	if !steamDetector.IsSCUMServerInstalled(absInstallPath) {
		c.logger.Error("Server installation failed, SCUM server still not found")
		return
	}

	c.logger.Info("SCUM Dedicated Server installation verified, initializing components...")

	// 更新steamDir为实际安装路径
	c.steamDir = absInstallPath

	// Initialize database connection
	if steamDetector.IsSCUMDatabaseAvailable(c.steamDir) {
		c.logger.Info("Initializing SCUM database connection...")
		if err := c.db.Initialize(); err != nil {
			c.logger.Warn("Failed to initialize database after installation: %v", err)
		}
	}

	// Initialize log monitor
	if steamDetector.IsSCUMLogsDirectoryAvailable(c.steamDir) {
		logsPath := steamDetector.GetSCUMLogsPath(c.steamDir)
		c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
		if err := c.logMonitor.Start(); err != nil {
			c.logger.Warn("Failed to start log monitor after installation: %v", err)
		}
	}

	c.logger.Info("Components initialized successfully after installation")

	// 检查是否需要自动启动服务器
	if c.config.AutoInstall.AutoStartAfterInstall {
		c.logger.Info("Auto-start is enabled, starting SCUM server after installation...")
		go func() {
			// 等待一段时间让组件完全初始化
			time.Sleep(2 * time.Second)
			c.handleServerStart()
		}()
	}
}

// performServerInstallation performs the actual server installation
func (c *Client) performServerInstallation(installPath, steamCmdPath string, forceReinstall bool) {
	c.logger.Info("Starting SCUM server installation...")
	c.logger.Info("Installation parameters - installPath: %s, steamCmdPath: %s, forceReinstall: %t", installPath, steamCmdPath, forceReinstall)

	// 开始安装 - 不再发送状态消息

	// 设置默认SteamCmd路径（如果为空）
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
		c.logger.Info("Using default SteamCmd path: %s", steamCmdPath)
	}

	// 将相对路径转换为绝对路径
	absPath, err := filepath.Abs(steamCmdPath)
	if err != nil {
		c.logger.Warn("Failed to get absolute path for SteamCmd, using original path: %v", err)
		absPath = steamCmdPath
	} else {
		steamCmdPath = absPath
		c.logger.Info("SteamCmd absolute path: %s", steamCmdPath)
	}

	// 确保路径使用正确的分隔符
	steamCmdPath = filepath.Clean(steamCmdPath)

	// 检查SteamCmd是否存在
	c.logger.Info("Checking if SteamCmd exists at: %s", steamCmdPath)
	if _, err := os.Stat(steamCmdPath); os.IsNotExist(err) {
		c.logger.Info("SteamCmd not found at path: %s, downloading...", steamCmdPath)
		if err := c.downloadSteamCmd(); err != nil {
			c.logger.Error("Failed to download SteamCmd: %v", err)
			return
		}
		c.logger.Info("SteamCmd downloaded successfully")

		// 再次检查SteamCmd是否存在，使用绝对路径
		absDownloadPath, _ := filepath.Abs(_const.DefaultSteamCmdPath)
		if _, err := os.Stat(absDownloadPath); os.IsNotExist(err) {
			c.logger.Error("SteamCmd still not found after download at path: %s", absDownloadPath)
			return
		}
		// 更新steamCmdPath为下载后的绝对路径
		steamCmdPath = absDownloadPath
		c.logger.Info("Updated SteamCmd path after download: %s", steamCmdPath)
	}
	c.logger.Info("SteamCmd found at: %s", steamCmdPath)

	// 设置安装路径
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	// 将安装路径转换为绝对路径
	absInstallPath, err := filepath.Abs(installPath)
	if err != nil {
		c.logger.Warn("Failed to get absolute path for install directory, using original path: %v", err)
		absInstallPath = installPath
	} else {
		installPath = absInstallPath
	}

	// 确保安装路径使用正确的分隔符
	installPath = filepath.Clean(installPath)
	c.logger.Info("Using install path: %s", installPath)

	// 创建安装目录
	if err := os.MkdirAll(installPath, 0755); err != nil {
		c.logger.Error("Failed to create install directory: %v", err)
		return
	}

	c.logger.Info("Installing SCUM server...")

	// 构建SteamCmd命令
	args := []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+app_update", _const.SCUMServerAppID, "validate",
		"+exit",
	}

	c.logger.Info("Executing SteamCmd with command: %s %v", steamCmdPath, args)

	// 再次验证SteamCmd文件是否存在且可执行
	if err := c.validateSteamCmdExecutable(steamCmdPath); err != nil {
		c.logger.Error("SteamCmd validation failed: %v", err)
		return
	}

	// 执行SteamCmd安装
	cmd := exec.Command(steamCmdPath, args...)

	// 设置工作目录为steamcmd的父目录
	steamCmdDir := filepath.Dir(steamCmdPath)
	cmd.Dir = steamCmdDir

	// 设置环境变量
	cmd.Env = os.Environ()

	c.logger.Info("Working directory: %s", cmd.Dir)
	c.logger.Info("Full command: %s %v", steamCmdPath, args)

	// 使用管道获取实时输出
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.logger.Error("Failed to create stdout pipe: %v", err)
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.logger.Error("Failed to create stderr pipe: %v", err)
		return
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		c.logger.Error("Failed to start SteamCmd: %v", err)
		return
	}

	// 读取输出 - 使用 bufio.Scanner 进行逐行读取
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			c.logger.Info("SteamCmd stdout: %s", line)

			// 检查安装进度 - 仅记录日志，不发送状态消息
			if strings.Contains(line, "Update state") && strings.Contains(line, "downloading") {
				c.logger.Info("SteamCmd: Downloading SCUM server files...")
			} else if strings.Contains(line, "Update state") && strings.Contains(line, "verifying") {
				c.logger.Info("SteamCmd: Verifying SCUM server files...")
			} else if strings.Contains(line, "Success") {
				c.logger.Info("SteamCmd operation completed successfully")
			} else if strings.Contains(line, "Error") || strings.Contains(line, "Failed") {
				c.logger.Error("SteamCmd error detected: %s", line)
			}
		}
		if err := scanner.Err(); err != nil {
			c.logger.Error("Error reading SteamCmd stdout: %v", err)
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			c.logger.Warn("SteamCmd stderr: %s", line)
		}
		if err := scanner.Err(); err != nil {
			c.logger.Error("Error reading SteamCmd stderr: %v", err)
		}
	}()

	// 等待命令完成
	err = cmd.Wait()

	if err != nil {
		c.logger.Error("SteamCmd installation failed: %v", err)
		return
	}

	// 验证安装是否成功
	scumServerExe := filepath.Join(installPath, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	if _, err := os.Stat(scumServerExe); err != nil {
		c.logger.Error("SCUM server executable not found after installation: %s", scumServerExe)
		c.logger.Error("Installation completed but SCUM server executable not found")
		return
	}

	c.logger.Info("SCUM server installation completed successfully")
}

// checkServerInstallation checks if SCUM server is installed in multiple possible locations
func (c *Client) checkServerInstallation(steamDetector *steam.Detector) bool {
	// 首先检查配置的steamDir
	if c.steamDir != "" && steamDetector.IsSCUMServerInstalled(c.steamDir) {
		c.logger.Debug("SCUM server found in configured steam directory: %s", c.steamDir)
		return true
	}

	// 检查自动安装路径
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	absInstallPath, err := filepath.Abs(installPath)
	if err == nil && steamDetector.IsSCUMServerInstalled(absInstallPath) {
		c.logger.Debug("SCUM server found in auto-install directory: %s", absInstallPath)
		// 更新steamDir为实际安装路径
		c.steamDir = absInstallPath
		return true
	}

	c.logger.Debug("SCUM server not found in any configured locations")
	return false
}

// initializeServerComponents initializes database and log monitoring components
func (c *Client) initializeServerComponents(steamDetector *steam.Detector) {
	// Check if database is available and initialize if possible
	if steamDetector.IsSCUMDatabaseAvailable(c.steamDir) {
		c.logger.Info("SCUM database found, initializing connection...")
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

// handleServerUpdate handles server update requests from the web interface
func (c *Client) handleServerUpdate(data interface{}) {
	c.logger.Info("Received server update request")

	updateData, ok := data.(map[string]interface{})
	if !ok {
		c.sendResponse(MsgTypeServerUpdate, nil, "Invalid update request data format")
		return
	}

	// 检查更新类型
	updateType, ok := updateData["type"].(string)
	if !ok {
		c.sendResponse(MsgTypeServerUpdate, nil, "Missing update type")
		return
	}

	switch updateType {
	case "check":
		c.handleServerUpdateCheck()
	case "install":
		c.handleServerUpdateInstall(updateData)
	default:
		c.sendResponse(MsgTypeServerUpdate, nil, fmt.Sprintf("Unknown update type: %s", updateType))
	}
}

// handleServerUpdateCheck checks for server updates
func (c *Client) handleServerUpdateCheck() {
	c.logger.Info("Checking for SCUM server updates...")

	// 这里可以实现检查更新的逻辑
	// 比如检查Steam上的最新版本信息

	c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
		"type":    "check",
		"status":  "completed",
		"message": "Update check completed",
	}, "")
}

// handleServerUpdateInstall performs server update installation
func (c *Client) handleServerUpdateInstall(updateData map[string]interface{}) {
	c.logger.Info("Starting SCUM server update installation...")

	// 检查是否已经在安装中
	c.installMux.Lock()
	if c.installing {
		c.installMux.Unlock()
		c.sendResponse(MsgTypeServerUpdate, nil, "Update installation already in progress")
		return
	}
	c.installing = true
	c.installMux.Unlock()

	defer func() {
		c.installMux.Lock()
		c.installing = false
		c.installMux.Unlock()
	}()

	// 在更新前先优雅关闭SCUM服务端
	if c.process != nil && c.process.IsRunning() {
		c.logger.Info("Stopping SCUM server before update...")
		if err := c.process.Stop(); err != nil {
			c.logger.Warn("Failed to stop server before update: %v", err)
		} else {
			c.logger.Info("SCUM server stopped successfully before update")
		}
	}

	// 强制重新安装以更新到最新版本
	forceReinstall := true
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	steamCmdPath := c.config.AutoInstall.SteamCmdPath
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
	}

	// 执行更新安装
	c.logger.Info("Performing server update installation (force reinstall)...")
	go func() {
		c.performServerInstallation(installPath, steamCmdPath, forceReinstall)

		// 安装完成后重新初始化组件
		c.initializeComponentsAfterInstall()

		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":    "install",
			"status":  "completed",
			"message": "Server update installation completed",
		}, "")
	}()

	c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
		"type":    "install",
		"status":  "started",
		"message": "Server update installation started",
	}, "")
}

// handleScheduledRestart handles scheduled restart requests
func (c *Client) handleScheduledRestart(data interface{}) {
	c.logger.Info("Received scheduled restart request")

	restartData, ok := data.(map[string]interface{})
	if !ok {
		c.sendResponse(MsgTypeScheduledRestart, nil, "Invalid restart request data format")
		return
	}

	// 获取重启原因
	reason := "Scheduled restart"
	if reasonStr, exists := restartData["reason"].(string); exists && reasonStr != "" {
		reason = reasonStr
	}

	c.logger.Info("Performing scheduled restart: %s", reason)

	// 检查服务器是否在运行
	if c.process == nil || !c.process.IsRunning() {
		c.logger.Info("Server is not running, skipping scheduled restart")
		c.sendResponse(MsgTypeScheduledRestart, map[string]interface{}{
			"status":  "skipped",
			"reason":  "Server is not running",
			"message": "Scheduled restart skipped - server is not running",
		}, "")
		return
	}

	// 执行重启
	if err := c.process.Restart(); err != nil {
		c.sendResponse(MsgTypeScheduledRestart, nil, fmt.Sprintf("Failed to restart server: %v", err))
		return
	}

	c.sendResponse(MsgTypeScheduledRestart, map[string]interface{}{
		"status":  "restarted",
		"reason":  reason,
		"pid":     c.process.GetPID(),
		"message": "Scheduled restart completed successfully",
	}, "")
}

// validateSteamCmdExecutable validates that the SteamCmd executable is valid and accessible
func (c *Client) validateSteamCmdExecutable(steamCmdPath string) error {
	// 检查文件是否存在
	fileInfo, err := os.Stat(steamCmdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SteamCmd file does not exist at path: %s", steamCmdPath)
		}
		return fmt.Errorf("cannot access SteamCmd file: %v", err)
	}

	// 检查是否是目录
	if fileInfo.IsDir() {
		return fmt.Errorf("SteamCmd path is a directory, not a file: %s", steamCmdPath)
	}

	// 检查文件大小（steamcmd.exe应该有一定的大小）
	if fileInfo.Size() < 1024 { // 小于1KB可能是无效文件
		return fmt.Errorf("SteamCmd file seems too small (%d bytes), possibly corrupted: %s", fileInfo.Size(), steamCmdPath)
	}

	// 检查文件扩展名（Windows）
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(steamCmdPath), ".exe") {
		return fmt.Errorf("SteamCmd file should have .exe extension on Windows: %s", steamCmdPath)
	}

	c.logger.Info("SteamCmd validation passed: %s (size: %d bytes)", steamCmdPath, fileInfo.Size())
	return nil
}

// performSteamCmdDownload downloads SteamCmd
func (c *Client) performSteamCmdDownload() {
	if err := c.downloadSteamCmd(); err != nil {
		c.sendResponse(MsgTypeDownloadSteamCmd, nil, fmt.Sprintf("Failed to download SteamCmd: %v", err))
	} else {
		c.sendResponse(MsgTypeDownloadSteamCmd, map[string]interface{}{
			"downloaded": true,
			"path":       _const.DefaultSteamCmdPath,
		}, "")
	}
}

// downloadSteamCmd downloads and extracts SteamCmd
func (c *Client) downloadSteamCmd() error {
	steamCmdURL := _const.DefaultSteamCmdURL
	steamCmdDir := _const.DefaultSteamCmdDir

	c.logger.Info("Downloading SteamCmd from %s to directory %s", steamCmdURL, steamCmdDir)

	// 创建目录
	c.logger.Info("Creating directory: %s", steamCmdDir)
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
	c.logger.Info("Extracting SteamCmd from %s to %s", tempFile, steamCmdDir)
	if err := c.extractZip(tempFile, steamCmdDir); err != nil {
		return fmt.Errorf("failed to extract steamcmd.zip: %w", err)
	}

	// 删除临时文件
	c.logger.Info("Cleaning up temporary file: %s", tempFile)
	os.Remove(tempFile)

	// 验证SteamCmd是否成功解压
	expectedPath := _const.DefaultSteamCmdPath
	c.logger.Info("Verifying SteamCmd at expected path: %s", expectedPath)
	if _, err := os.Stat(expectedPath); err != nil {
		return fmt.Errorf("steamcmd.exe not found after extraction at %s: %w", expectedPath, err)
	}

	c.logger.Info("SteamCmd downloaded and extracted successfully to %s", expectedPath)
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

// sendInstallStatus function removed - installation no longer sends status messages
