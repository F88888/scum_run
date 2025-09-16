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
	"sort"
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
	"scum_run/internal/updater"
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
	MsgTypeServerCommand    = "server_command"    // 服务器命令
	MsgTypeCommandResult    = "command_result"    // 命令结果
	MsgTypeLogData          = "log_data"          // 日志数据
	MsgTypeClientUpdate     = "client_update"     // 客户端更新

	// File management
	MsgTypeFileBrowse    = "file_browse"    // 文件浏览
	MsgTypeFileList      = "file_list"      // 文件列表响应
	MsgTypeFileOperation = "file_operation" // 文件操作
)

// New creates a new SCUM Run client
func New(cfg *config.Config, steamDir string, logger *logger.Logger) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	steamDetector := steam.NewDetector(logger)

	client := &Client{
		config:     cfg,
		steamDir:   steamDir,
		logger:     logger,
		ctx:        ctx,
		cancel:     cancel,
		db:         database.New(steamDetector.GetSCUMDatabasePath(steamDir), logger),
		process:    process.New(steamDetector.GetSCUMServerPath(steamDir), logger),
		steamTools: steamtools.New(&cfg.SteamTools, logger),
	}

	// 设置进程输出回调函数
	client.process.SetOutputCallback(client.handleProcessOutput)

	return client
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

	// 设置重连回调
	c.wsClient.SetCallbacks(
		func() {
			c.logger.Info("WebSocket connected, sending authentication...")
			// 连接成功后自动发送认证
			authMsg := request.WebSocketMessage{
				Type: MsgTypeAuth,
				Data: map[string]interface{}{
					"token": c.config.Token,
				},
			}
			if err := c.wsClient.SendMessage(authMsg); err != nil {
				c.logger.Error("Failed to send authentication: %v", err)
			} else {
				c.logger.Info("Authentication message sent successfully")
			}
		},
		func() {
			c.logger.Warn("WebSocket disconnected")
		},
		func() {
			c.logger.Info("WebSocket reconnected, re-authenticating...")
			// 重连成功后重新发送认证
			authMsg := request.WebSocketMessage{
				Type: MsgTypeAuth,
				Data: map[string]interface{}{
					"token": c.config.Token,
				},
			}
			if err := c.wsClient.SendMessage(authMsg); err != nil {
				c.logger.Error("Failed to send re-authentication: %v", err)
			} else {
				c.logger.Info("Re-authentication message sent successfully")
			}
		},
	)

	// 使用自动重连连接
	if err := c.wsClient.ConnectWithAutoReconnect(); err != nil {
		return fmt.Errorf("failed to connect to WebSocket server: %w", err)
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
	c.logger.Info("Received message: %s, Success: %v", msg.Type, msg.Success)
	if msg.Error != "" {
		c.logger.Error("Message error: %s", msg.Error)
	}

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
	case MsgTypeServerCommand:
		c.handleServerCommand(msg.Data)
	case MsgTypeClientUpdate:
		c.handleClientUpdate(msg.Data)
	case MsgTypeFileBrowse:
		c.handleFileBrowse(msg.Data)
	case MsgTypeFileList:
		c.handleFileList(msg.Data)
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

	// 同时发送实时日志数据给Web终端
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			c.sendLogData(line)
		}
	}
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

// handleServerCommand handles server command requests from web terminal
func (c *Client) handleServerCommand(data interface{}) {
	c.logger.Info("DEBUG: Received server command request")

	commandData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("DEBUG: Invalid command data format")
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"success": false,
			"output":  "Invalid command data format",
		}, "Invalid command data format")
		return
	}

	command, ok := commandData["command"].(string)
	if !ok || command == "" {
		c.logger.Error("DEBUG: Command is empty or not a string")
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"success": false,
			"output":  "Command is required",
		}, "Command is required")
		return
	}

	c.logger.Info("DEBUG: Executing server command: %s", command)

	// 执行服务器命令
	output, err := c.executeServerCommand(command)
	if err != nil {
		c.logger.Error("DEBUG: Command execution failed: %v", err)
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"command": command,
			"success": false,
			"output":  fmt.Sprintf("Command execution failed: %v", err),
		}, "")
	} else {
		c.logger.Info("DEBUG: Command executed successfully: %s", command)
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"command": command,
			"success": true,
			"output":  output,
		}, "")
	}
}

// executeServerCommand executes a SCUM server command
func (c *Client) executeServerCommand(command string) (string, error) {
	c.logger.Info("DEBUG: executeServerCommand called with command: %s", command)

	// 检查服务器是否在运行
	if c.process == nil {
		c.logger.Error("DEBUG: Process manager is nil")
		return "", fmt.Errorf("process manager is not initialized")
	}

	if !c.process.IsRunning() {
		c.logger.Error("DEBUG: Server is not running")
		return "", fmt.Errorf("server is not running")
	}

	c.logger.Info("DEBUG: Server is running, sending command to process manager")

	// 发送命令到SCUM服务器
	if err := c.process.SendCommand(command); err != nil {
		c.logger.Error("DEBUG: Failed to send command to server: %v", err)
		return "", fmt.Errorf("failed to send command to server: %w", err)
	}

	c.logger.Info("DEBUG: Successfully sent command to server: %s", command)

	// 发送日志数据显示命令已执行
	c.sendLogData(fmt.Sprintf("Command executed: %s", command))

	return fmt.Sprintf("Command '%s' has been sent to the server", command), nil
}

// sendLogData sends real-time log data to web terminals
func (c *Client) sendLogData(content string) {
	logData := map[string]interface{}{
		"content":   content,
		"timestamp": float64(time.Now().Unix()),
	}

	c.sendResponse(MsgTypeLogData, logData, "")
}

// handleProcessOutput handles real-time output from SCUM server process
func (c *Client) handleProcessOutput(source string, line string) {
	// 格式化输出内容
	formattedLine := fmt.Sprintf("[%s] %s", source, line)

	// 发送到WebSocket终端
	c.sendLogData(formattedLine)
}

// handleClientUpdate handles client update requests
func (c *Client) handleClientUpdate(data interface{}) {
	c.logger.Info("Received client update request")

	updateData, ok := data.(map[string]interface{})
	if !ok {
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, "Invalid update request data format")
		return
	}

	// 检查更新动作
	action, ok := updateData["action"].(string)
	if !ok {
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, "Missing update action")
		return
	}

	switch action {
	case "update":
		// 检查是否需要先停止服务器
		stopServer, _ := updateData["stop_server"].(bool)
		if stopServer {
			c.logger.Info("Stopping SCUM server before client update...")
			if c.process != nil && c.process.IsRunning() {
				if err := c.process.Stop(); err != nil {
					c.logger.Error("Failed to stop server before update: %v", err)
					c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
						"type":   "self_update",
						"status": _const.UpdateStatusFailed,
					}, fmt.Sprintf("Failed to stop server: %v", err))
					return
				}
				c.logger.Info("Server stopped successfully, proceeding with client update")
			}
		}

		// 启动自我更新流程
		go c.performSelfUpdate()
	default:
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Unknown update action: %s", action))
	}
}

// performSelfUpdate performs the self-update process using external updater
func (c *Client) performSelfUpdate() {
	c.logger.Info("Starting self-update process...")

	// 发送更新开始状态
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusChecking,
	}, "Checking for updates...")

	// 1. 检查更新
	latestVersion, downloadURL, err := c.checkForUpdates()
	if err != nil {
		c.logger.Error("Failed to check for updates: %v", err)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Failed to check for updates: %v", err))
		return
	}

	if latestVersion == "" {
		c.logger.Info("No updates available")
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusNoUpdate,
		}, "No updates available")
		return
	}

	c.logger.Info("New version available: %s", latestVersion)

	// 2. 准备更新配置
	currentExe, err := os.Executable()
	if err != nil {
		c.logger.Error("Failed to get executable path: %v", err)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Failed to get executable path: %v", err))
		return
	}

	updateConfig := updater.UpdaterConfig{
		CurrentExePath: currentExe,
		UpdateURL:      downloadURL,
		Args:           os.Args[1:], // 排除程序名本身
	}

	// 3. 发送更新状态并启动外部更新器
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusDownloading,
	}, fmt.Sprintf("Starting updater for version %s...", latestVersion))

	// 启动外部更新器
	if err := updater.ExecuteUpdate(updateConfig); err != nil {
		c.logger.Error("Failed to start updater: %v", err)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Failed to start updater: %v", err))
		return
	}

	c.logger.Info("External updater started, shutting down current process...")

	// 发送最终状态
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusInstalling,
	}, "Updater started, shutting down for update...")

	// 延迟一段时间让消息发送完成，然后退出让更新器接管
	go func() {
		time.Sleep(2 * time.Second)
		c.logger.Info("Exiting for update...")
		os.Exit(0)
	}()
}

// checkForUpdates checks if there are any available updates
func (c *Client) checkForUpdates() (version string, downloadURL string, err error) {
	// 这里应该实现检查更新的逻辑
	// 可以从GitHub API获取最新版本信息
	// 目前返回空表示无更新可用

	c.logger.Info("Checking for updates from: %s", _const.UpdateCheckURL)

	// TODO: 实现实际的更新检查逻辑
	// 1. 获取当前版本
	// 2. 从GitHub API获取最新版本
	// 3. 比较版本号
	// 4. 如果有新版本，返回版本号和下载URL

	return "", "", nil // 暂时返回无更新
}

// sendInstallStatus function removed - installation no longer sends status messages

// handleFileBrowse 处理文件浏览请求
func (c *Client) handleFileBrowse(data interface{}) {
	c.logger.Debug("Handling file browse request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file browse request data")
		c.sendResponse(MsgTypeFileList, nil, "Invalid request data")
		return
	}

	path, _ := dataMap["path"].(string)
	if path == "" {
		path = "/"
	}

	// 扫描指定路径的文件和目录
	fileList, err := c.scanDirectory(path)
	if err != nil {
		c.logger.Error("Failed to scan directory %s: %v", path, err)
		c.sendResponse(MsgTypeFileList, nil, fmt.Sprintf("Failed to scan directory: %v", err))
		return
	}

	// 发送文件列表响应
	responseData := map[string]interface{}{
		"current_path": path,
		"files":        fileList,
		"total":        len(fileList),
	}

	c.sendResponse(MsgTypeFileList, responseData, "")
	c.logger.Debug("Sent file list for path: %s (%d items)", path, len(fileList))
}

// handleFileList 处理文件列表响应（通常不会在客户端收到）
func (c *Client) handleFileList(data interface{}) {
	c.logger.Debug("Received file list response (unexpected)")
}

// scanDirectory 扫描指定目录并返回文件列表
func (c *Client) scanDirectory(path string) ([]map[string]interface{}, error) {
	// 构建完整路径
	var fullPath string
	if path == "/" {
		fullPath = c.steamDir
	} else {
		fullPath = filepath.Join(c.steamDir, path)
	}

	// 检查路径是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("path does not exist: %s", path)
	}

	// 读取目录内容
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %v", err)
	}

	var fileList []map[string]interface{}

	for _, entry := range entries {
		// 跳过隐藏文件（以.开头）
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		var info os.FileInfo
		info, err = entry.Info()
		if err != nil {
			c.logger.Warn("Failed to get file info for %s: %v", entry.Name(), err)
			continue
		}

		// 构建相对路径
		relativePath := filepath.Join(path, entry.Name())
		if path == "/" {
			relativePath = "/" + entry.Name()
		}

		fileInfo := map[string]interface{}{
			"name":         entry.Name(),
			"path":         relativePath,
			"size":         info.Size(),
			"type":         getFileType(info),
			"is_directory": info.IsDir(),
			"permissions":  getFilePermissions(info.Mode()),
			"owner":        getFileOwner(info),
			"created_at":   info.ModTime().Format("2006-01-02 15:04:05"),
			"updated_at":   info.ModTime().Format("2006-01-02 15:04:05"),
		}

		fileList = append(fileList, fileInfo)
	}

	// 排序：目录在前，文件在后，按名称排序
	sort.Slice(fileList, func(i, j int) bool {
		iIsDir, _ := fileList[i]["is_directory"].(bool)
		jIsDir, _ := fileList[j]["is_directory"].(bool)
		iName, _ := fileList[i]["name"].(string)
		jName, _ := fileList[j]["name"].(string)

		if iIsDir != jIsDir {
			return iIsDir // 目录在前
		}
		return iName < jName // 按名称排序
	})

	return fileList, nil
}

// getFileType 获取文件类型
func getFileType(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}

	ext := strings.ToLower(filepath.Ext(info.Name()))
	switch ext {
	case ".exe":
		return "executable"
	case ".dll":
		return "library"
	case ".ini", ".cfg", ".conf":
		return "config"
	case ".log", ".txt":
		return "text"
	case ".zip", ".rar", ".7z":
		return "archive"
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp":
		return "image"
	case ".mp3", ".wav", ".ogg":
		return "audio"
	case ".mp4", ".avi", ".mkv":
		return "video"
	default:
		return "file"
	}
}

// getFilePermissions 获取文件权限字符串
func getFilePermissions(mode os.FileMode) string {
	perm := mode.Perm()
	return fmt.Sprintf("%o", perm)
}

// getFileOwner 获取文件所有者（简化版本）
func getFileOwner(info os.FileInfo) string {
	// 在Windows上，这个功能比较复杂，暂时返回"system"
	// 在Linux上可以使用syscall.Getuid()等
	return "system"
}
