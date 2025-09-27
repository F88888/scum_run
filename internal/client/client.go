package client

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/saintfish/chardet"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"scum_run/config"
	_const "scum_run/internal/const"
	"scum_run/internal/database"
	"scum_run/internal/logger"
	"scum_run/internal/logmonitor"
	"scum_run/internal/monitor"
	"scum_run/internal/process"
	"scum_run/internal/steam"
	"scum_run/internal/steamtools"
	"scum_run/internal/updater"
	"scum_run/internal/websocket_client"
	"scum_run/model"
	"scum_run/model/request"
	"sort"
	"strings"
	"sync"
	"time"
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
	sysMonitor *monitor.SystemMonitor
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	installing bool       // 安装状态标志
	installMux sync.Mutex // 安装锁

	// 日志批量处理
	logBuffer     []string
	logBufferMux  sync.Mutex
	logTicker     *time.Ticker
	lastLogSend   time.Time
	maxLogRate    int           // 每秒最大日志发送数量
	logRateWindow time.Duration // 日志频率控制窗口
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
	MsgTypeFileBrowse = "file_browse" // 文件浏览
	MsgTypeFileList   = "file_list"   // 文件列表响应
	MsgTypeFileRead   = "file_read"   // 文件内容读取
	MsgTypeFileWrite  = "file_write"  // 文件内容写入

	// System monitoring
	MsgTypeSystemMonitor = "system_monitor" // 系统监控数据

	// Backup related
	MsgTypeBackupStart    = "backup_start"    // 开始备份
	MsgTypeBackupStop     = "backup_stop"     // 停止备份
	MsgTypeBackupStatus   = "backup_status"   // 备份状态
	MsgTypeBackupList     = "backup_list"     // 备份列表
	MsgTypeBackupDelete   = "backup_delete"   // 删除备份
	MsgTypeBackupProgress = "backup_progress" // 备份进度

	// File transfer related
	MsgTypeFileTransfer  = "file_transfer"  // 文件传输
	MsgTypeFileUpload    = "file_upload"    // 文件上传
	MsgTypeFileDownload  = "file_download"  // 文件下载
	MsgTypeFileDelete    = "file_delete"    // 文件删除
	MsgTypeCloudUpload   = "cloud_upload"   // 云存储上传
	MsgTypeCloudDownload = "cloud_download" // 云存储下载
)

// New creates a new SCUM Run client
func New(cfg *config.Config, steamDir string, logger *logger.Logger) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	steamDetector := steam.NewDetector(logger)

	client := &Client{
		config:        cfg,
		steamDir:      steamDir,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		db:            database.New(steamDetector.GetSCUMDatabasePath(steamDir), logger),
		process:       process.New(steamDetector.GetSCUMServerPath(steamDir), logger),
		steamTools:    steamtools.New(&cfg.SteamTools, logger),
		sysMonitor:    monitor.New(logger, 10*time.Second),                    // 每10秒监控一次
		logBuffer:     make([]string, 0, 100),                                 // 预分配100条日志的缓冲区
		maxLogRate:    _const.LogMaxRatePerSecond,                             // 每秒最多发送日志数量
		logRateWindow: time.Duration(_const.LogRateWindow) * time.Millisecond, // 频率控制窗口
	}

	// 设置进程输出回调函数
	client.process.SetOutputCallback(client.handleProcessOutput)

	// 设置系统监控回调函数
	client.sysMonitor.SetCallback(client.handleSystemMonitorData)

	// 启动日志批量处理定时器
	client.logTicker = time.NewTicker(time.Duration(_const.LogBatchInterval) * time.Millisecond) // 批量发送间隔
	go client.logBatchProcessor()

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

	// Start system monitoring
	if err := c.sysMonitor.Start(); err != nil {
		c.logger.Error("Failed to start system monitor: %v", err)
	} else {
		c.logger.Info("System monitor started successfully")
	}

	// WebSocket client handles heartbeat automatically

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

	// 停止日志批量处理定时器
	if c.logTicker != nil {
		c.logTicker.Stop()
	}

	// 发送剩余的日志缓冲区
	c.flushLogBuffer()

	if c.logMonitor != nil {
		c.logMonitor.Stop()
	}

	if c.sysMonitor != nil {
		c.sysMonitor.Stop()
	}

	if c.process != nil {
		if err := c.process.Stop(); err != nil {
			c.logger.Warn("Failed to stop process: %v", err)
		}
	}

	if c.db != nil {
		if err := c.db.Close(); err != nil {
			c.logger.Warn("Failed to close database: %v", err)
		}
	}

	if c.wsClient != nil {
		if err := c.wsClient.Close(); err != nil {
			c.logger.Warn("Failed to close WebSocket client: %v", err)
		}
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

// ForceStop forcefully stops the client and all associated processes
func (c *Client) ForceStop() {
	c.logger.Info("Force stopping SCUM Run client and all processes...")

	c.cancel()

	// 停止日志批量处理定时器
	if c.logTicker != nil {
		c.logTicker.Stop()
	}

	// 发送剩余的日志缓冲区
	c.flushLogBuffer()

	if c.logMonitor != nil {
		c.logMonitor.Stop()
	}

	// Force stop the SCUM server process and all child processes
	if c.process != nil {
		c.process.CleanupOnExit()
	}

	if c.db != nil {
		if err := c.db.Close(); err != nil {
			c.logger.Warn("Failed to close database: %v", err)
		}
	}

	if c.wsClient != nil {
		if err := c.wsClient.Close(); err != nil {
			c.logger.Warn("Failed to close WebSocket client: %v", err)
		}
	}

	// Stop Steam++ last
	if c.steamTools != nil {
		if err := c.steamTools.Stop(); err != nil {
			c.logger.Warn("Failed to stop Steam++: %v", err)
		}
	}

	c.wg.Wait()
	c.logger.Info("SCUM Run client force stopped")
}

// handleMessages handles incoming WebSocket messages
func (c *Client) handleMessages() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// 检查WebSocket客户端是否仍然连接
			if !c.wsClient.IsConnected() {
				c.logger.Debug("WebSocket not connected, waiting for reconnection...")
				time.Sleep(2 * time.Second)
				continue
			}

			var msg request.WebSocketMessage
			if err := c.wsClient.ReadMessage(&msg); err != nil {
				// 使用更详细的错误处理
				if strings.Contains(err.Error(), "connection not running") ||
					strings.Contains(err.Error(), "websocket: close") {
					c.logger.Debug("WebSocket connection closed, waiting for reconnection...")
					time.Sleep(2 * time.Second)
				} else {
					c.logger.Error("Failed to read WebSocket message: %v", err)
					time.Sleep(1 * time.Second)
				}
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
	case MsgTypeFileRead:
		c.handleFileRead(msg.Data)
	case MsgTypeFileWrite:
		c.handleFileWrite(msg.Data)
	case MsgTypeHeartbeat:
		// Heartbeat messages from server are handled silently
		c.logger.Debug("Received heartbeat from server")
	case MsgTypeAuth:
		// Handle authentication response from server
		c.handleAuthResponse(msg)
	case MsgTypeBackupStart:
		c.handleBackupStart(msg.Data)
	case MsgTypeBackupStop:
		c.handleBackupStop(msg.Data)
	case MsgTypeBackupStatus:
		c.handleBackupStatus(msg.Data)
	case MsgTypeBackupList:
		c.handleBackupList(msg.Data)
	case MsgTypeBackupDelete:
		c.handleBackupDelete(msg.Data)
	case MsgTypeFileTransfer:
		c.handleFileTransfer(msg.Data)
	case MsgTypeFileUpload:
		c.handleFileUpload(msg.Data)
	case MsgTypeFileDownload:
		c.handleFileDownload(msg.Data)
	case MsgTypeFileDelete:
		c.handleFileDelete(msg.Data)
	case MsgTypeCloudUpload:
		c.handleCloudUpload(msg.Data)
	case MsgTypeCloudDownload:
		c.handleCloudDownload(msg.Data)
	case MsgTypeSystemMonitor:
		c.handleSystemMonitor(msg.Data)
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

	// 先发送启动开始的响应，避免长时间无响应导致连接超时
	c.sendResponse(MsgTypeServerStart, map[string]interface{}{
		"status":  "starting",
		"message": "Server startup initiated...",
	}, "")

	// Start the server process in a goroutine to avoid blocking WebSocket
	go func() {
		if err := c.process.Start(); err != nil {
			c.logger.Error("Failed to start server: %v", err)
			c.sendResponse(MsgTypeServerStart, nil, fmt.Sprintf("Failed to start server: %v", err))
			return
		}

		// Send success response after process starts
		c.sendResponse(MsgTypeServerStart, map[string]interface{}{
			"status": "started",
			"pid":    c.process.GetPID(),
		}, "")

		// After server starts, try to initialize database connection
		// This is done after server start because the database file is created by SCUM server
		go func() {
			// 减少等待时间，提高响应速度
			time.Sleep(2 * time.Second)
			c.logger.Info("Attempting to initialize database connection after server start...")

			// 使用重试机制而不是单次检查
			maxRetries := 5
			for i := 0; i < maxRetries; i++ {
				if c.db.IsAvailable() {
					if err := c.db.Initialize(); err != nil {
						c.logger.Warn("Failed to initialize database after server start (attempt %d): %v", i+1, err)
					} else {
						c.logger.Info("Database connection initialized successfully after server start")
						return
					}
				} else {
					c.logger.Info("Database file not yet available, retrying in 1 second (attempt %d/%d)", i+1, maxRetries)
				}
				time.Sleep(1 * time.Second)
			}
			c.logger.Warn("Failed to initialize database after %d attempts", maxRetries)
		}()
	}()
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

	// 减少等待时间，提高重启速度
	time.Sleep(1 * time.Second)

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

	// 将日志行添加到批量缓冲区，而不是立即发送
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			c.addLogToBuffer(line)
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
	serverConfig := &model.ServerConfig{}

	if installPath, ok := configData["install_path"].(string); ok && installPath != "" {
		serverConfig.ExecPath = installPath + "\\SCUM\\Binaries\\Win64\\SCUMServer.exe"
	} else {
		// 如果没有配置路径，使用Steam检测的路径
		steamDetector := steam.NewDetector(c.logger)
		serverConfig.ExecPath = steamDetector.GetSCUMServerPath(c.steamDir)
	}

	if gamePort, ok := configData["game_port"].(float64); ok {
		serverConfig.GamePort = int(gamePort)
	} else {
		serverConfig.GamePort = _const.DefaultGamePort
	}

	if maxPlayers, ok := configData["max_players"].(float64); ok {
		serverConfig.MaxPlayers = int(maxPlayers)
	} else {
		serverConfig.MaxPlayers = _const.DefaultMaxPlayers
	}

	if enableBattlEye, ok := configData["enable_battleye"].(bool); ok {
		serverConfig.EnableBattlEye = enableBattlEye
	}

	if serverIP, ok := configData["server_ip"].(string); ok {
		serverConfig.ServerIP = serverIP
	}

	if additionalArgs, ok := configData["additional_args"].(string); ok {
		serverConfig.AdditionalArgs = additionalArgs
	}

	// 更新进程管理器配置
	if c.process != nil {
		c.process.UpdateConfig(serverConfig)
		c.logger.Info("Updated server configuration - Path: %s, Port: %d, MaxPlayers: %d, BattlEye: %v",
			serverConfig.ExecPath, serverConfig.GamePort, serverConfig.MaxPlayers, serverConfig.EnableBattlEye)
	} else {
		// 如果进程管理器还未创建，则创建一个新的
		c.process = process.NewWithConfig(serverConfig, c.logger)
		c.logger.Info("Created new process manager with server configuration")
	}

	// 发送配置更新确认 - 先发送确认再执行耗时操作
	response := request.WebSocketMessage{
		Type:    MsgTypeConfigUpdate,
		Success: true,
		Data: map[string]interface{}{
			"config_updated": true,
			"current_config": map[string]interface{}{
				"exec_path":       serverConfig.ExecPath,
				"game_port":       serverConfig.GamePort,
				"max_players":     serverConfig.MaxPlayers,
				"enable_battleye": serverConfig.EnableBattlEye,
				"server_ip":       serverConfig.ServerIP,
				"additional_args": serverConfig.AdditionalArgs,
			},
		},
	}
	if err := c.wsClient.SendMessage(response); err != nil {
		c.logger.Error("Failed to send config update confirmation: %v", err)
	}

	// 检查是否需要自动启动服务器（仅在配置同步时，而非配置更新时）
	if c.config.AutoInstall.AutoStartAfterConfig {
		steamDetector := steam.NewDetector(c.logger)
		if steamDetector.IsSCUMServerInstalled(c.steamDir) && !c.process.IsRunning() {
			c.logger.Info("Auto-start after config sync is enabled and server is installed, scheduling server start...")
			// 使用更长的延迟，确保WebSocket连接稳定
			go func() {
				// 等待更长时间确保配置完全更新且连接稳定
				time.Sleep(5 * time.Second)
				c.logger.Info("Starting SCUM server after config sync...")
				c.handleServerStart()
			}()
		}
	}
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
func (c *Client) handleDownloadSteamCmd(_ interface{}) {
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
		// 更新steamDir为实际安装路径
		c.steamDir = absInstallPath
		return true
	}

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
	}

	// Initialize log monitor
	if steamDetector.IsSCUMLogsDirectoryAvailable(c.steamDir) {
		logsPath := steamDetector.GetSCUMLogsPath(c.steamDir)
		c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
		if err := c.logMonitor.Start(); err != nil {
			c.logger.Warn("Failed to start log monitor: %v", err)
		}
	}
}

// handleServerUpdate handles server update requests from the web interface
func (c *Client) handleServerUpdate(data interface{}) {
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
func (c *Client) handleServerUpdateInstall(_ map[string]interface{}) {
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
	// 创建目录
	if err := os.MkdirAll(steamCmdDir, 0755); err != nil {
		return fmt.Errorf("failed to create steamcmd directory: %w", err)
	}

	// 下载文件
	response, err := http.Get(steamCmdURL)
	if err != nil {
		return fmt.Errorf("failed to download steamcmd: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			c.logger.Warn("Failed to close response body: %v", err)
		}
	}()

	// 创建临时文件
	tempFile := filepath.Join(steamCmdDir, "steamcmd.zip")
	out, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		if err := out.Close(); err != nil {
			c.logger.Warn("Failed to close temp file: %v", err)
		}
	}()

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
	if err := os.Remove(tempFile); err != nil {
		c.logger.Warn("Failed to remove temp file %s: %v", tempFile, err)
	}

	// 验证SteamCmd是否成功解压
	expectedPath := _const.DefaultSteamCmdPath
	if _, err := os.Stat(expectedPath); err != nil {
		return fmt.Errorf("steamcmd.exe not found after extraction at %s: %w", expectedPath, err)
	}
	return nil
}

// extractZip extracts a zip file to the specified directory
func (c *Client) extractZip(src, dest string) error {
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
			_ = rc.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		_ = outFile.Close()
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

// handleServerCommand handles server command requests from web terminal
func (c *Client) handleServerCommand(data interface{}) {
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
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"command": command,
			"success": true,
			"output":  output,
		}, "")
	}
}

// executeServerCommand executes a SCUM server command
func (c *Client) executeServerCommand(command string) (string, error) {
	// 检查服务器是否在运行
	if c.process == nil {
		c.logger.Error("DEBUG: Process manager is nil")
		return "", fmt.Errorf("process manager is not initialized")
	}

	if !c.process.IsRunning() {
		c.logger.Error("DEBUG: Server is not running")
		return "", fmt.Errorf("server is not running")
	}

	// 发送命令到SCUM服务器
	if err := c.process.SendCommand(command); err != nil {
		c.logger.Error("DEBUG: Failed to send command to server: %v", err)
		return "", fmt.Errorf("failed to send command to server: %w", err)
	}
	// 发送日志数据显示命令已执行

	return fmt.Sprintf("Command '%s' has been sent to the server", command), nil
}

// sendLogData sends real-time log data to web terminals (deprecated - use addLogToBuffer instead)
func (c *Client) sendLogData(content string) {
	// 使用新的批量处理机制
	c.addLogToBuffer(content)
}

// handleProcessOutput handles real-time output from SCUM server process
func (c *Client) handleProcessOutput(_ string, line string) {
	// 直接发送原始日志内容，不添加前缀
	c.sendLogData(line)
}

// handleClientUpdate handles client update requests
func (c *Client) handleClientUpdate(data interface{}) {
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

	// 获取请求ID用于响应匹配
	requestID, _ := dataMap["request_id"].(string)

	// 扫描指定路径的文件和目录
	fileList, err := c.scanDirectory(path)
	if err != nil {
		c.logger.Error("Failed to scan directory %s: %v", path, err)
		// 在错误响应中也包含请求ID
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileList, errorData, fmt.Sprintf("Failed to scan directory: %v", err))
		return
	}

	// 发送文件列表响应
	responseData := map[string]interface{}{
		"current_path": path,
		"files":        fileList,
		"total":        len(fileList),
	}

	// 在响应中包含请求ID
	if requestID != "" {
		responseData["request_id"] = requestID
	}

	c.sendResponse(MsgTypeFileList, responseData, "")
}

// handleFileList 处理文件列表响应（通常不会在客户端收到）
func (c *Client) handleFileList(_ interface{}) {
	c.logger.Debug("Received file list response (unexpected)")
}

// handleFileRead 处理文件内容读取请求
func (c *Client) handleFileRead(data interface{}) {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file read request data")
		c.sendResponse(MsgTypeFileRead, nil, "Invalid request data")
		return
	}

	path, _ := dataMap["path"].(string)
	encoding, _ := dataMap["encoding"].(string)
	requestID, _ := dataMap["request_id"].(string)

	if path == "" {
		c.logger.Error("File path is required")
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileRead, errorData, "File path is required")
		return
	}

	if encoding == "" {
		encoding = "utf-8"
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(path, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(path, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, path)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileRead, errorData, "Access denied: path outside allowed directory")
		return
	}

	// 检查文件是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		c.logger.Error("File does not exist: %s", fullPath)
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileRead, errorData, fmt.Sprintf("File does not exist: %s", path))
		return
	}

	// 读取文件内容
	content, err := c.readFileWithEncoding(fullPath, encoding)
	if err != nil {
		c.logger.Error("Failed to read file %s: %v", fullPath, err)
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileRead, errorData, fmt.Sprintf("Failed to read file: %v", err))
		return
	}

	// 发送文件内容响应
	responseData := map[string]interface{}{
		"content":  content,
		"encoding": encoding,
		"size":     len(content),
	}

	// 在响应中包含请求ID
	if requestID != "" {
		responseData["request_id"] = requestID
	}

	c.sendResponse(MsgTypeFileRead, responseData, "")
}

// handleFileWrite 处理文件内容写入请求
func (c *Client) handleFileWrite(data interface{}) {
	c.logger.Debug("Handling file write request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file write request data")
		c.sendResponse(MsgTypeFileWrite, nil, "Invalid request data")
		return
	}

	path, _ := dataMap["path"].(string)
	content, _ := dataMap["content"].(string)
	encoding, _ := dataMap["encoding"].(string)
	requestID, _ := dataMap["request_id"].(string)

	if path == "" {
		c.logger.Error("File path is required")
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileWrite, errorData, "File path is required")
		return
	}

	if content == "" {
		c.logger.Error("File content is required")
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileWrite, errorData, "File content is required")
		return
	}

	if encoding == "" {
		encoding = "utf-8"
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(path, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(path, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, path)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileWrite, errorData, "Access denied: path outside allowed directory")
		return
	}

	// 确保目录存在
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.logger.Error("Failed to create directory %s: %v", dir, err)
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileWrite, errorData, fmt.Sprintf("Failed to create directory: %v", err))
		return
	}

	// 写入文件内容
	err := c.writeFileWithEncoding(fullPath, content, encoding)
	if err != nil {
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileWrite, errorData, fmt.Sprintf("Failed to write file: %v", err))
		return
	}

	// 获取文件信息
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		c.logger.Warn("Failed to get file info after write: %v", err)
	}

	// 发送写入成功响应
	responseData := map[string]interface{}{
		"path":     path,
		"encoding": encoding,
		"size":     len(content),
	}

	if fileInfo != nil {
		responseData["file_size"] = fileInfo.Size()
		responseData["modified_at"] = fileInfo.ModTime().Format("2006-01-02 15:04:05")
	}

	// 在响应中包含请求ID
	if requestID != "" {
		responseData["request_id"] = requestID
	}

	c.sendResponse(MsgTypeFileWrite, responseData, "")
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
func getFileOwner(_ os.FileInfo) string {
	// 在Windows上，这个功能比较复杂，暂时返回"system"
	// 在Linux上可以使用syscall.Getuid()等
	return "system"
}

// addLogToBuffer adds a log line to the buffer for batch processing
func (c *Client) addLogToBuffer(content string) {
	c.logBufferMux.Lock()
	defer c.logBufferMux.Unlock()

	// 检查消息大小限制（单条日志最大1KB）
	if len(content) > 1024 {
		content = content[:1024] + "... [truncated]"
	}

	// 检查频率限制 - 放宽限制避免日志丢失
	now := time.Now()
	if now.Sub(c.lastLogSend) < c.logRateWindow && len(c.logBuffer) < _const.LogBatchSize/2 {
		// 只有在缓冲区未满一半时才跳过日志
		return
	}

	// 添加到缓冲区
	c.logBuffer = append(c.logBuffer, content)

	// 如果缓冲区满了，立即发送
	if len(c.logBuffer) >= _const.LogBatchSize { // 批量大小限制
		c.flushLogBufferUnsafe()
	}
}

// logBatchProcessor processes log batches at regular intervals
func (c *Client) logBatchProcessor() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.logTicker.C:
			c.flushLogBuffer()
		}
	}
}

// flushLogBuffer sends all buffered logs to the server
func (c *Client) flushLogBuffer() {
	c.logBufferMux.Lock()
	defer c.logBufferMux.Unlock()
	c.flushLogBufferUnsafe()
}

// flushLogBufferUnsafe sends all buffered logs without locking (caller must hold lock)
func (c *Client) flushLogBufferUnsafe() {
	if len(c.logBuffer) == 0 {
		return
	}

	// 检查发送频率限制
	now := time.Now()
	if now.Sub(c.lastLogSend) < c.logRateWindow {
		return
	}

	// 限制批量大小，避免单次发送过多数据
	batchSize := len(c.logBuffer)
	if batchSize > c.maxLogRate {
		batchSize = c.maxLogRate
	}

	// 发送批量日志数据
	batch := make([]string, batchSize)
	copy(batch, c.logBuffer[:batchSize])

	// 从缓冲区移除已发送的日志
	c.logBuffer = c.logBuffer[batchSize:]

	// 发送批量日志
	c.sendBatchLogData(batch)

	// 更新最后发送时间
	c.lastLogSend = now
}

// sendBatchLogData sends a batch of log data to web terminals
func (c *Client) sendBatchLogData(logs []string) {
	if len(logs) == 0 {
		return
	}

	// 确保日志数据格式正确
	var logContents []interface{}
	for _, log := range logs {
		if strings.TrimSpace(log) != "" {
			logContents = append(logContents, log)
		}
	}

	if len(logContents) == 0 {
		return
	}

	logData := map[string]interface{}{
		"content": logContents,
		"batch":   true, // 标识这是批量数据
	}

	c.sendResponse(MsgTypeLogData, logData, "")
}

// readFileWithEncoding 根据指定编码读取文件内容
func (c *Client) readFileWithEncoding(filePath, encoding string) (string, error) {
	// 读取文件原始字节
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// 根据编码转换内容
	switch strings.ToLower(encoding) {
	case "binary":
		// 对于二进制文件，直接返回原始字节数据（base64编码）
		return string(fileData), nil
	case "utf-8", "utf8":
		return string(fileData), nil
	case "utf-16le":
		// 对于UTF-16LE编码，尝试转换
		decoder := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
		reader := transform.NewReader(strings.NewReader(string(fileData)), decoder)
		decoded, err := io.ReadAll(reader)
		if err != nil {
			// 如果转换失败，返回原始内容
			c.logger.Warn("Failed to convert UTF-16LE to UTF-8, returning raw content: %v", err)
			return string(fileData), nil
		}
		return string(decoded), nil
	case "utf-16be":
		// 对于UTF-16BE编码，尝试转换
		decoder := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()
		reader := transform.NewReader(strings.NewReader(string(fileData)), decoder)
		decoded, err := io.ReadAll(reader)
		if err != nil {
			// 如果转换失败，返回原始内容
			c.logger.Warn("Failed to convert UTF-16BE to UTF-8, returning raw content: %v", err)
			return string(fileData), nil
		}
		return string(decoded), nil
	case "gbk":
		// 对于GBK编码，尝试转换
		decoder := simplifiedchinese.GBK.NewDecoder()
		utf8Data, err := decoder.Bytes(fileData)
		if err != nil {
			// 如果转换失败，返回原始内容
			c.logger.Warn("Failed to convert GBK to UTF-8, returning raw content: %v", err)
			return string(fileData), nil
		}
		return string(utf8Data), nil
	case "gb2312":
		// 对于GB2312编码，尝试转换
		decoder := simplifiedchinese.GB18030.NewDecoder()
		utf8Data, err := decoder.Bytes(fileData)
		if err != nil {
			// 如果转换失败，返回原始内容
			c.logger.Warn("Failed to convert GB2312 to UTF-8, returning raw content: %v", err)
			return string(fileData), nil
		}
		return string(utf8Data), nil
	default:
		// 对于其他编码，尝试自动检测
		detector := chardet.NewTextDetector()
		result, err := detector.DetectBest(fileData)
		if err != nil {
			c.logger.Warn("Failed to detect encoding, using UTF-8: %v", err)
			return string(fileData), nil
		}

		c.logger.Debug("Detected encoding: %s (confidence: %.2f)", result.Charset, result.Confidence)

		// 如果检测到的编码不是UTF-8，尝试转换
		if result.Charset != "UTF-8" {
			// 这里可以添加更多编码转换逻辑
			// 目前只处理常见的编码
			switch strings.ToLower(result.Charset) {
			case "gbk", "gb2312":
				decoder := simplifiedchinese.GBK.NewDecoder()
				utf8Data, err := decoder.Bytes(fileData)
				if err != nil {
					c.logger.Warn("Failed to convert detected encoding to UTF-8: %v", err)
					return string(fileData), nil
				}
				return string(utf8Data), nil
			default:
				c.logger.Warn("Unsupported encoding detected: %s", result.Charset)
				return string(fileData), nil
			}
		}

		return string(fileData), nil
	}
}

// writeFileWithEncoding 根据指定编码写入文件内容
func (c *Client) writeFileWithEncoding(filePath, content, encoding string) error {
	var fileData []byte
	var err error

	// 根据编码转换内容
	switch strings.ToLower(encoding) {
	case "utf-8", "utf8":
		fileData = []byte(content)
	case "utf-16le":
		// 对于UTF-16LE编码，尝试转换
		encoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder()
		reader := transform.NewReader(strings.NewReader(content), encoder)
		fileData, err = io.ReadAll(reader)
		if err != nil {
			// 如果转换失败，使用原始内容
			c.logger.Warn("Failed to convert UTF-8 to UTF-16LE, using raw content: %v", err)
			fileData = []byte(content)
		}
	case "utf-16be":
		// 对于UTF-16BE编码，尝试转换
		encoder := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewEncoder()
		reader := transform.NewReader(strings.NewReader(content), encoder)
		fileData, err = io.ReadAll(reader)
		if err != nil {
			// 如果转换失败，使用原始内容
			c.logger.Warn("Failed to convert UTF-8 to UTF-16BE, using raw content: %v", err)
			fileData = []byte(content)
		}
	case "gbk":
		// 对于GBK编码，尝试转换
		encoder := simplifiedchinese.GBK.NewEncoder()
		fileData, err = encoder.Bytes([]byte(content))
		if err != nil {
			// 如果转换失败，使用原始内容
			c.logger.Warn("Failed to convert UTF-8 to GBK, using raw content: %v", err)
			fileData = []byte(content)
		}
	case "gb2312":
		// 对于GB2312编码，尝试转换
		encoder := simplifiedchinese.GB18030.NewEncoder()
		fileData, err = encoder.Bytes([]byte(content))
		if err != nil {
			// 如果转换失败，使用原始内容
			c.logger.Warn("Failed to convert UTF-8 to GB2312, using raw content: %v", err)
			fileData = []byte(content)
		}
	default:
		// 对于其他编码，使用原始内容
		c.logger.Warn("Unsupported encoding for writing: %s, using UTF-8", encoding)
		fileData = []byte(content)
	}

	// 写入文件
	err = os.WriteFile(filePath, fileData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// handleSystemMonitor 处理系统监控消息
func (c *Client) handleSystemMonitor(data interface{}) {
	//c.logger.Debug("Received system monitor message")

	// 系统监控消息通常是从服务器发送的配置或控制指令
	// 这里可以根据需要处理服务器发送的系统监控相关指令
	if data != nil {
		//c.logger.Debug("System monitor data: %+v", data)
	}
}

// handleSystemMonitorData 处理系统监控数据
func (c *Client) handleSystemMonitorData(data *request.SystemMonitorData) {
	// 检查WebSocket连接是否可用
	if !c.wsClient.IsConnected() {
		c.logger.Debug("WebSocket not connected, skipping system monitor data")
		return
	}

	// 创建系统监控消息
	msg := request.WebSocketMessage{
		Type: MsgTypeSystemMonitor,
		Data: data,
	}

	// 发送系统监控数据
	if err := c.wsClient.SendMessage(msg); err != nil {
		c.logger.Error("Failed to send system monitor data: %v", err)
	}
}

// handleBackupStart 处理开始备份请求
func (c *Client) handleBackupStart(data interface{}) {
	c.logger.Info("Received backup start request")

	backupData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid backup data format")
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"success": false,
			"message": "Invalid backup data format",
		})
		return
	}

	serverID, ok := backupData["server_id"].(float64)
	if !ok {
		c.logger.Error("Server ID is missing or invalid")
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"success": false,
			"message": "Server ID is missing or invalid",
		})
		return
	}

	// 获取备份路径，如果没有提供则根据服务器类型使用默认路径
	backupPath, ok := backupData["backup_path"].(string)
	if !ok || backupPath == "" {
		// 根据服务器类型设置默认备份路径
		backupPath = c.getDefaultBackupPath(uint(serverID))
	} else {
		// 验证用户提供的备份路径
		cfg, err := c.getServerConfig(uint(serverID))
		if err != nil {
			c.logger.Error("Failed to get server config for path validation: %v", err)
			c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
				"success": false,
				"message": "无法获取服务器配置",
			})
			return
		}

		installPath := cfg.AutoInstall.InstallPath
		if installPath == "" {
			installPath = "C:/scumserver"
		}

		if err := c.validateBackupPath(backupPath, installPath); err != nil {
			c.logger.Error("Invalid backup path: %v", err)
			c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	}

	description, _ := backupData["description"].(string)
	if description == "" {
		description = "手动备份"
	}

	// 异步执行备份
	go c.executeBackup(uint(serverID), backupPath, description)
}

// handleBackupStop 处理停止备份请求
func (c *Client) handleBackupStop(data interface{}) {
	c.logger.Info("Received backup stop request")
	// 这里可以实现停止备份的逻辑
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"message": "Backup stop request received",
	})
}

// handleBackupStatus 处理备份状态请求
func (c *Client) handleBackupStatus(data interface{}) {
	c.logger.Info("Received backup status request")
	// 返回当前备份状态
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"status":  "idle",
		"message": "No backup in progress",
	})
}

// handleBackupList 处理备份列表请求
func (c *Client) handleBackupList(data interface{}) {
	c.logger.Info("Received backup list request")
	// 这里可以实现获取备份列表的逻辑
	c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
		"success": true,
		"list":    []interface{}{},
		"message": "Backup list retrieved",
	})
}

// handleBackupDelete 处理删除备份请求
func (c *Client) handleBackupDelete(data interface{}) {
	c.logger.Info("Received backup delete request")
	// 这里可以实现删除备份的逻辑
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"message": "Backup delete request received",
	})
}

// executeBackup 执行备份操作
func (c *Client) executeBackup(serverID uint, backupPath, description string) {
	c.logger.Info("Starting backup for server %d, path: %s", serverID, backupPath)

	// 创建性能监控器
	perfMonitor := monitor.NewPerformanceMonitor(c.logger, 10*time.Second) // 每10秒监控一次
	perfMonitor.Start()
	defer perfMonitor.Stop()

	// 清空之前的性能数据
	perfMonitor.ClearData()

	// 发送备份开始状态
	c.sendBackupResponse(MsgTypeBackupProgress, map[string]interface{}{
		"server_id": serverID,
		"status":    1, // 备份中
		"progress":  0,
		"message":   "开始备份...",
	})

	// 检查备份路径是否存在
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		c.logger.Error("Backup path does not exist: %s", backupPath)
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"server_id": serverID,
			"success":   false,
			"message":   "备份路径不存在",
		})
		return
	}

	// 创建备份目录
	backupDir := filepath.Join(filepath.Dir(os.Args[0]), "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		c.logger.Error("Failed to create backup directory: %v", err)
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"server_id": serverID,
			"success":   false,
			"message":   "创建备份目录失败",
		})
		return
	}

	// 生成备份文件名
	timestamp := time.Now().Format("20060102_150405")
	fileName := fmt.Sprintf("backup_%d_%s.zip", serverID, timestamp)
	filePath := filepath.Join(backupDir, fileName)

	// 记录备份开始时间
	backupStartTime := time.Now()

	// 执行备份
	fileCount, err := c.createBackupArchive(backupPath, filePath, serverID, perfMonitor)
	if err != nil {
		c.logger.Error("Backup failed: %v", err)
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"server_id": serverID,
			"success":   false,
			"message":   fmt.Sprintf("备份失败: %v", err),
		})
		return
	}

	// 记录备份结束时间
	backupEndTime := time.Now()
	backupDuration := int(backupEndTime.Sub(backupStartTime).Seconds())

	// 获取备份文件信息
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		c.logger.Error("Failed to get backup file info: %v", err)
		c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
			"server_id": serverID,
			"success":   false,
			"message":   "获取备份文件信息失败",
		})
		return
	}

	// 获取平均性能数据
	avgPerfData := perfMonitor.GetAverageData()

	// 计算备份性能指标
	backupSize := fileInfo.Size()
	compressionRatio := float64(backupSize) / float64(c.getSourceSize(backupPath))
	filesPerSecond := float64(fileCount) / float64(backupDuration)
	dataThroughput := float64(backupSize) / (1024 * 1024) / float64(backupDuration) // MB/s

	// 创建备份结果
	backupResult := &model.BackupResult{
		BackupID:         fmt.Sprintf("backup_%d_%s", serverID, timestamp),
		BackupSize:       backupSize,
		FileCount:        fileCount,
		Duration:         backupDuration,
		CompressionRatio: compressionRatio,
		BackupPath:       filePath,
		Checksum:         c.calculateFileChecksum(filePath),

		// 性能监控数据
		CPUUsage:       avgPerfData.CPUUsage,
		MemoryUsage:    avgPerfData.MemoryUsage,
		DiskUsage:      avgPerfData.DiskUsage,
		NetworkIn:      avgPerfData.NetworkIn,
		NetworkOut:     avgPerfData.NetworkOut,
		DiskReadSpeed:  avgPerfData.DiskReadSpeed,
		DiskWriteSpeed: avgPerfData.DiskWriteSpeed,
		ProcessCount:   avgPerfData.ProcessCount,
		LoadAverage:    avgPerfData.LoadAverage,

		// 系统资源信息
		CPUCores:        avgPerfData.CPUCores,
		TotalMemory:     avgPerfData.TotalMemory,
		AvailableMemory: avgPerfData.AvailableMemory,
		TotalDiskSpace:  avgPerfData.TotalDiskSpace,
		FreeDiskSpace:   avgPerfData.FreeDiskSpace,

		// 备份性能指标
		FilesPerSecond:  filesPerSecond,
		DataThroughput:  dataThroughput,
		CompressionTime: int(float64(backupDuration) * 0.2),  // 假设压缩占20%时间
		EncryptionTime:  int(float64(backupDuration) * 0.05), // 假设加密占5%时间

		CreatedAt: backupStartTime,
	}

	// 清理旧备份（保留最新的20个）
	c.cleanOldBackups(backupDir, serverID, 20)

	// 发送备份完成状态，包含详细的性能数据
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"server_id": serverID,
		"success":   true,
		"message":   "备份完成",
		"file_name": fileName,
		"file_size": fileInfo.Size(),
		"file_path": filePath,
		"result":    backupResult,
	})

	c.logger.Info("Backup completed successfully for server %d: %s", serverID, fileName)
}

// createBackupArchive 创建备份压缩包
func (c *Client) createBackupArchive(sourcePath, targetPath string, serverID uint, perfMonitor *monitor.PerformanceMonitor) (int, error) {
	// 创建ZIP文件
	zipFile, err := os.Create(targetPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create backup file: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	fileCount := 0
	progressTicker := time.NewTicker(5 * time.Second) // 每5秒发送一次进度
	defer progressTicker.Stop()

	// 遍历源目录并添加到ZIP
	err = filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 跳过无法访问的文件
			c.logger.Warn("Skipping file %s: %v", path, err)
			return nil
		}

		// 跳过目录
		if info.IsDir() {
			// 排除 Logs 目录
			if filepath.Base(path) == "Logs" {
				c.logger.Info("Skipping Logs directory: %s", path)
				return filepath.SkipDir
			}
			return nil
		}

		// 计算相对路径
		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}

		// 创建ZIP文件条目
		zipEntry, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		// 打开源文件
		sourceFile, err := os.Open(path)
		if err != nil {
			// 跳过锁定的文件
			c.logger.Warn("Skipping locked file %s: %v", path, err)
			return nil
		}
		defer sourceFile.Close()

		// 复制文件内容
		_, err = io.Copy(zipEntry, sourceFile)
		if err != nil {
			c.logger.Warn("Failed to copy file %s: %v", path, err)
			return nil
		}

		fileCount++

		// 发送进度更新
		select {
		case <-progressTicker.C:
			c.sendBackupResponse(MsgTypeBackupProgress, map[string]interface{}{
				"server_id": serverID,
				"status":    1,                                 // 备份中
				"progress":  float64(fileCount) / 1000.0 * 100, // 假设最多1000个文件
				"message":   fmt.Sprintf("已处理 %d 个文件...", fileCount),
			})
		default:
		}

		return nil
	})

	if err != nil {
		os.Remove(targetPath) // 清理失败的文件
		return 0, fmt.Errorf("failed to create backup archive: %w", err)
	}

	return fileCount, nil
}

// getSourceSize 获取源目录总大小
func (c *Client) getSourceSize(sourcePath string) int64 {
	var totalSize int64
	filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// 排除 Logs 目录
		if info.IsDir() && filepath.Base(path) == "Logs" {
			return filepath.SkipDir
		}

		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize
}

// calculateFileChecksum 计算文件校验和
func (c *Client) calculateFileChecksum(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}

// cleanOldBackups 清理旧备份文件
func (c *Client) cleanOldBackups(backupDir string, serverID uint, keepCount int) {
	// 查找该服务器的所有备份文件
	pattern := fmt.Sprintf("backup_%d_*.zip", serverID)
	matches, err := filepath.Glob(filepath.Join(backupDir, pattern))
	if err != nil {
		c.logger.Error("Failed to find backup files: %v", err)
		return
	}

	// 按修改时间排序
	sort.Slice(matches, func(i, j int) bool {
		info1, _ := os.Stat(matches[i])
		info2, _ := os.Stat(matches[j])
		return info1.ModTime().After(info2.ModTime())
	})

	// 删除多余的备份文件
	if len(matches) > keepCount {
		toDelete := matches[keepCount:]
		for _, file := range toDelete {
			if err := os.Remove(file); err != nil {
				c.logger.Warn("Failed to delete old backup file %s: %v", file, err)
			} else {
				c.logger.Info("Deleted old backup file: %s", file)
			}
		}
	}
}

// sendBackupResponse 发送备份响应
func (c *Client) sendBackupResponse(msgType string, data interface{}) {
	if !c.wsClient.IsConnected() {
		c.logger.Debug("WebSocket not connected, skipping backup response")
		return
	}

	msg := request.WebSocketMessage{
		Type: msgType,
		Data: data,
	}

	if err := c.wsClient.SendMessage(msg); err != nil {
		c.logger.Error("Failed to send backup response: %v", err)
	}
}

// generateTaskID 生成任务ID
func generateTaskID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}

// getDefaultBackupPath 根据服务器类型获取默认备份路径
func (c *Client) getDefaultBackupPath(serverID uint) string {
	// 获取服务器配置信息
	cfg, err := c.getServerConfig(serverID)
	if err != nil {
		c.logger.Error("Failed to get server config: %v", err)
		// 使用默认路径
		return "C:/scumserver/backups"
	}

	// 获取安装路径
	installPath := cfg.AutoInstall.InstallPath
	if installPath == "" {
		installPath = "C:/scumserver"
	}

	// 根据服务器类型设置不同的备份路径
	// 这里需要根据实际的服务器类型判断逻辑
	// 暂时通过检查路径结构来判断服务器类型
	var backupPath string

	// 检查是否存在 SCUM 目录结构来判断是否为 SCUM 自建服
	scumSavePath := filepath.Join(installPath, "SCUM", "Saved", "SaveFiles")
	if _, err := os.Stat(scumSavePath); err == nil {
		// SCUM 自建服：备份路径是 \SCUM\Saved\SaveFiles
		backupPath = scumSavePath
		c.logger.Info("Detected SCUM self-hosted server, using SaveFiles path")
	} else {
		// CMD 服务器：备份路径是根目录
		backupPath = installPath
		c.logger.Info("Detected CMD server, using root directory")
	}

	c.logger.Info("Server %d backup path: %s (install path: %s)", serverID, backupPath, installPath)
	return backupPath
}

// getServerConfig 获取服务器配置信息
func (c *Client) getServerConfig(serverID uint) (*config.Config, error) {
	// 返回当前客户端的配置
	return c.config, nil
}

// validateBackupPath 验证备份路径是否安全
func (c *Client) validateBackupPath(path string, installPath string) error {
	// 检查路径是否包含危险字符
	if strings.Contains(path, "..") || strings.Contains(path, "../") || strings.Contains(path, "..\\") {
		return fmt.Errorf("备份路径包含危险字符，不允许使用相对路径")
	}

	// 检查路径是否在安装目录内
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("无法解析备份路径: %v", err)
	}

	absInstallPath, err := filepath.Abs(installPath)
	if err != nil {
		return fmt.Errorf("无法解析安装路径: %v", err)
	}

	// 检查备份路径是否在安装目录内
	if !strings.HasPrefix(absPath, absInstallPath) {
		return fmt.Errorf("备份路径必须在安装目录内")
	}

	return nil
}

// handleFileTransfer 处理文件传输请求
func (c *Client) handleFileTransfer(data interface{}) {
	c.logger.Debug("Handling file transfer request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file transfer request data")
		c.sendResponse(MsgTypeFileTransfer, nil, "Invalid request data")
		return
	}

	operation, _ := dataMap["operation"].(string)
	transferID, _ := dataMap["transfer_id"].(string)

	switch operation {
	case "upload":
		c.handleFileUpload(data)
	case "download":
		c.handleFileDownload(data)
	default:
		c.logger.Error("Unknown file transfer operation: %s", operation)
		c.sendResponse(MsgTypeFileTransfer, map[string]interface{}{
			"transfer_id": transferID,
		}, "Unknown operation")
	}
}

// handleFileUpload 处理文件上传请求
func (c *Client) handleFileUpload(data interface{}) {
	c.logger.Debug("Handling file upload request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file upload request data")
		c.sendResponse(MsgTypeFileUpload, nil, "Invalid request data")
		return
	}

	filePath, _ := dataMap["file_path"].(string)
	content, _ := dataMap["content"].(string)
	encoding, _ := dataMap["encoding"].(string)
	transferID, _ := dataMap["transfer_id"].(string)

	if filePath == "" {
		c.logger.Error("File path is required")
		c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, "File path is required")
		return
	}

	if content == "" {
		c.logger.Error("File content is required")
		c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, "File content is required")
		return
	}

	if encoding == "" {
		encoding = "utf-8"
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(filePath, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(filePath, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
		c.logger.Debug("Converting absolute path %s to %s", filePath, fullPath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, filePath)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		c.logger.Error("Access denied: path outside Steam directory: %s (resolved to %s, steamDir: %s)", filePath, cleanFullPath, cleanSteamDir)
		c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, "Access denied: path outside allowed directory")
		return
	}

	c.logger.Debug("Writing file: %s (encoding: %s, size: %d bytes)", fullPath, encoding, len(content))

	// 确保目录存在
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.logger.Error("Failed to create directory %s: %v", dir, err)
		c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("Failed to create directory: %v", err))
		return
	}

	// 写入文件内容
	err := c.writeFileWithEncoding(fullPath, content, encoding)
	if err != nil {
		c.logger.Error("Failed to write file %s: %v", fullPath, err)
		c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("Failed to write file: %v", err))
		return
	}

	// 发送成功响应
	c.sendResponse(MsgTypeFileUpload, map[string]interface{}{
		"transfer_id": transferID,
		"file_path":   filePath,
		"file_size":   len(content),
	}, "")
	c.logger.Debug("File uploaded successfully: %s, transfer_id: %s", filePath, transferID)
}

// handleFileDownload 处理文件下载请求
func (c *Client) handleFileDownload(data interface{}) {
	c.logger.Debug("Handling file download request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file download request data")
		c.sendResponse(MsgTypeFileDownload, nil, "Invalid request data")
		return
	}

	filePath, _ := dataMap["file_path"].(string)
	encoding, _ := dataMap["encoding"].(string)
	transferID, _ := dataMap["transfer_id"].(string)

	if filePath == "" {
		c.logger.Error("File path is required")
		c.sendResponse(MsgTypeFileDownload, map[string]interface{}{
			"transfer_id": transferID,
		}, "File path is required")
		return
	}

	if encoding == "" {
		encoding = "binary"
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(filePath, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(filePath, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
		c.logger.Debug("Converting absolute path %s to %s", filePath, fullPath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, filePath)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		c.logger.Error("Access denied: path outside Steam directory: %s (resolved to %s, steamDir: %s)", filePath, cleanFullPath, cleanSteamDir)
		c.sendResponse(MsgTypeFileDownload, map[string]interface{}{
			"transfer_id": transferID,
		}, "Access denied: path outside allowed directory")
		return
	}

	c.logger.Debug("Reading file: %s (encoding: %s)", fullPath, encoding)

	// 检查文件是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		c.logger.Error("File does not exist: %s", fullPath)
		c.sendResponse(MsgTypeFileDownload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("File does not exist: %s", filePath))
		return
	}

	// 读取文件内容
	content, err := c.readFileWithEncoding(fullPath, encoding)
	if err != nil {
		c.logger.Error("Failed to read file %s: %v", fullPath, err)
		c.sendResponse(MsgTypeFileDownload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("Failed to read file: %v", err))
		return
	}

	// 发送文件内容响应
	responseData := map[string]interface{}{
		"transfer_id": transferID,
		"content":     content,
		"encoding":    encoding,
		"size":        len(content),
	}

	c.sendResponse(MsgTypeFileDownload, responseData, "")
	c.logger.Debug("File downloaded successfully: %s (%d bytes), transfer_id: %s", filePath, len(content), transferID)
}

// handleFileDelete 处理文件删除请求
func (c *Client) handleFileDelete(data interface{}) {
	c.logger.Debug("Handling file delete request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file delete request data")
		c.sendResponse(MsgTypeFileDelete, nil, "Invalid request data")
		return
	}

	filePath, _ := dataMap["file_path"].(string)

	if filePath == "" {
		c.logger.Error("File path is required")
		c.sendResponse(MsgTypeFileDelete, nil, "File path is required")
		return
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(filePath, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(filePath, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
		c.logger.Debug("Converting absolute path %s to %s", filePath, fullPath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, filePath)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		c.logger.Error("Access denied: path outside Steam directory: %s (resolved to %s, steamDir: %s)", filePath, cleanFullPath, cleanSteamDir)
		c.sendResponse(MsgTypeFileDelete, nil, "Access denied: path outside allowed directory")
		return
	}

	c.logger.Debug("Deleting file: %s", fullPath)

	// 检查文件是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		c.logger.Error("File does not exist: %s", fullPath)
		c.sendResponse(MsgTypeFileDelete, nil, fmt.Sprintf("File does not exist: %s", filePath))
		return
	}

	// 删除文件
	err := os.Remove(fullPath)
	if err != nil {
		c.logger.Error("Failed to delete file %s: %v", fullPath, err)
		c.sendResponse(MsgTypeFileDelete, nil, fmt.Sprintf("Failed to delete file: %v", err))
		return
	}

	// 发送成功响应
	responseData := map[string]interface{}{
		"file_path": filePath,
		"deleted":   true,
	}

	c.sendResponse(MsgTypeFileDelete, responseData, "")
	c.logger.Debug("File deleted successfully: %s", filePath)
}

// handleCloudUpload 处理云存储上传请求
func (c *Client) handleCloudUpload(data interface{}) {
	c.logger.Debug("Handling cloud upload request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid cloud upload request data")
		c.sendResponse(MsgTypeCloudUpload, nil, "Invalid request data")
		return
	}

	filePath, _ := dataMap["file_path"].(string)
	cloudPath, _ := dataMap["cloud_path"].(string)
	transferID, _ := dataMap["transfer_id"].(string)
	uploadSignature, _ := dataMap["upload_signature"].(map[string]interface{})

	if filePath == "" {
		c.logger.Error("File path is required")
		c.sendResponse(MsgTypeCloudUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, "File path is required")
		return
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(filePath, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		// 移除开头的斜杠，然后基于steamDir构建完整路径
		relativePath := strings.TrimPrefix(filePath, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
		c.logger.Debug("Converting absolute path %s to %s", filePath, fullPath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, filePath)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		c.logger.Error("Access denied: path outside Steam directory: %s (resolved to %s, steamDir: %s)", filePath, cleanFullPath, cleanSteamDir)
		c.sendResponse(MsgTypeCloudUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, "Access denied: path outside allowed directory")
		return
	}

	c.logger.Debug("Uploading file to cloud: %s -> %s", fullPath, cloudPath)

	// 检查文件是否存在
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		c.logger.Error("File does not exist: %s", fullPath)
		c.sendResponse(MsgTypeCloudUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("File does not exist: %s", filePath))
		return
	}

	// 实现云存储上传逻辑
	err := c.uploadFileToCloud(fullPath, cloudPath, transferID, uploadSignature)
	if err != nil {
		c.logger.Error("Failed to upload file to cloud: %v", err)
		c.sendResponse(MsgTypeCloudUpload, map[string]interface{}{
			"transfer_id": transferID,
		}, fmt.Sprintf("Failed to upload file to cloud: %v", err))
		return
	}

	c.logger.Info("Successfully uploaded file to cloud: %s -> %s", fullPath, cloudPath)
	c.sendResponse(MsgTypeCloudUpload, map[string]interface{}{
		"transfer_id": transferID,
		"cloud_path":  cloudPath,
		"file_path":   filePath,
	}, "")
}

// uploadFileToCloud 上传文件到云存储
func (c *Client) uploadFileToCloud(filePath, cloudPath, transferID string, uploadSignature map[string]interface{}) error {
	// 验证输入参数
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if cloudPath == "" {
		return fmt.Errorf("cloud path cannot be empty")
	}
	if uploadSignature == nil {
		return fmt.Errorf("upload signature cannot be nil")
	}

	// 读取文件内容
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	if len(fileData) == 0 {
		return fmt.Errorf("file %s is empty", filePath)
	}

	c.logger.Debug("Read file %s (%d bytes), uploading to cloud path: %s", filePath, len(fileData), cloudPath)

	// 记录上传凭证信息（用于调试）
	c.logger.Debug("Upload signature received: %+v", uploadSignature)

	// 检测云存储提供商
	provider := c.detectCloudProvider(uploadSignature)
	if provider == "" {
		return fmt.Errorf("unable to detect cloud storage provider from upload signature")
	}

	c.logger.Debug("Detected cloud storage provider: %s", provider)

	// 根据提供商选择上传方法
	switch provider {
	case "qiniu":
		return c.uploadToQiniu(fileData, cloudPath, uploadSignature)
	case "aliyun":
		return c.uploadToAliyun(fileData, cloudPath, uploadSignature)
	default:
		return fmt.Errorf("unsupported cloud storage provider: %s", provider)
	}
}

// detectCloudProvider 检测云存储提供商
func (c *Client) detectCloudProvider(uploadSignature map[string]interface{}) string {
	// 首先检查明确的provider字段
	if provider, ok := uploadSignature["provider"].(string); ok && provider != "" {
		return provider
	}

	// 根据特征字段推断提供商
	if _, hasToken := uploadSignature["token"]; hasToken {
		return "qiniu"
	}

	if _, hasPolicy := uploadSignature["policy"]; hasPolicy {
		return "aliyun"
	}

	return ""
}

// uploadToQiniu 上传文件到七牛云
func (c *Client) uploadToQiniu(fileData []byte, cloudPath string, uploadSignature map[string]interface{}) error {
	// 验证必需参数
	token, ok := uploadSignature["token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("missing or invalid qiniu upload token")
	}

	key, ok := uploadSignature["key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("missing or invalid qiniu upload key")
	}

	region, _ := uploadSignature["region"].(string)

	// 尝试上传到七牛云，支持区域域名自动切换
	return c.uploadToQiniuWithRetry(fileData, cloudPath, token, key, region)
}

// uploadToQiniuWithRetry 带重试的七牛云上传
func (c *Client) uploadToQiniuWithRetry(fileData []byte, cloudPath, token, key, region string) error {
	// 如果没有提供区域信息，使用默认值
	if region == "" {
		region = "z0" // 默认华东-浙江区域
	}

	// 根据区域构建上传URL
	uploadURL := c.buildQiniuUploadURL(region)

	// 尝试上传
	err := c.uploadToQiniuURL(fileData, cloudPath, token, key, uploadURL)
	if err == nil {
		// 上传成功
		c.logger.Info("Successfully uploaded file to Qiniu: %s (%d bytes)", cloudPath, len(fileData))
		return nil
	}

	// 如果上传失败且是区域错误，尝试解析错误信息获取正确的区域
	if strings.Contains(err.Error(), "incorrect region") && strings.Contains(err.Error(), "please use") {
		correctRegion := c.parseRegionFromError(err.Error())
		if correctRegion != "" && correctRegion != region {
			correctURL := c.buildQiniuUploadURL(correctRegion)
			err = c.uploadToQiniuURL(fileData, cloudPath, token, key, correctURL)
			if err == nil {
				c.logger.Info("Successfully uploaded file to Qiniu with corrected region: %s (%d bytes)", cloudPath, len(fileData))
				return nil
			}
		}
	}

	return fmt.Errorf("七牛云上传失败: %w", err)
}

// buildQiniuUploadURL 根据区域构建七牛云上传URL
func (c *Client) buildQiniuUploadURL(region string) string {
	// 七牛云区域域名映射
	regionMap := map[string]string{
		"z0":             "https://up-z0.qiniup.com",             // 华东-浙江
		"cn-east-2":      "https://up-cn-east-2.qiniup.com",      // 华东-浙江2
		"z1":             "https://up-z1.qiniup.com",             // 华北-河北
		"z2":             "https://up-z2.qiniup.com",             // 华南-广东
		"cn-northwest-1": "https://up-cn-northwest-1.qiniup.com", // 西北-陕西1
		"na0":            "https://up-na0.qiniup.com",            // 北美-洛杉矶
		"as0":            "https://up-as0.qiniup.com",            // 亚太-新加坡
		"ap-southeast-2": "https://up-ap-southeast-2.qiniup.com", // 亚太-河内
		"ap-southeast-3": "https://up-ap-southeast-3.qiniup.com", // 亚太-胡志明
	}

	if url, exists := regionMap[region]; exists {
		return url
	}

	// 如果区域不存在，使用通用域名
	return "https://upload.qiniup.com"
}

// parseRegionFromError 从错误信息中解析正确的区域
func (c *Client) parseRegionFromError(errorMsg string) string {
	// 解析错误信息中的区域域名
	regionMap := map[string]string{
		"up-z0.qiniup.com":             "z0",
		"up-cn-east-2.qiniup.com":      "cn-east-2",
		"up-z1.qiniup.com":             "z1",
		"up-z2.qiniup.com":             "z2",
		"up-cn-northwest-1.qiniup.com": "cn-northwest-1",
		"up-na0.qiniup.com":            "na0",
		"up-as0.qiniup.com":            "as0",
		"up-ap-southeast-2.qiniup.com": "ap-southeast-2",
		"up-ap-southeast-3.qiniup.com": "ap-southeast-3",
	}

	for domain, region := range regionMap {
		if strings.Contains(errorMsg, domain) {
			return region
		}
	}

	return ""
}

// uploadToQiniuURL 使用指定URL上传到七牛云
func (c *Client) uploadToQiniuURL(fileData []byte, cloudPath, token, key, uploadURL string) error {
	// 创建multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// 添加必需字段
	fields := map[string]string{
		"token": token,
		"key":   key,
	}

	for fieldName, fieldValue := range fields {
		if err := writer.WriteField(fieldName, fieldValue); err != nil {
			return fmt.Errorf("failed to write field %s: %w", fieldName, err)
		}
	}

	// 添加文件字段
	fileName := filepath.Base(cloudPath)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := fileWriter.Write(fileData); err != nil {
		return fmt.Errorf("failed to write file data: %w", err)
	}

	// 关闭writer
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "SCUM-Run-Client/1.0")

	// 发送请求
	httpClient := &http.Client{
		Timeout: 5 * time.Minute,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file to Qiniu: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Warn("Failed to close response body: %v", closeErr)
		}
	}()

	// 读取响应
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// 记录响应信息
	c.logger.Debug("Qiniu upload response: status=%d, body=%s", resp.StatusCode, string(responseBody))

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qiniu upload failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	return nil
}

// uploadToAliyun 上传文件到阿里云OSS
func (c *Client) uploadToAliyun(fileData []byte, cloudPath string, uploadSignature map[string]interface{}) error {
	// 验证必需参数
	policy, ok := uploadSignature["policy"].(string)
	if !ok || policy == "" {
		return fmt.Errorf("missing or invalid aliyun upload policy")
	}

	signature, ok := uploadSignature["signature"].(string)
	if !ok || signature == "" {
		return fmt.Errorf("missing or invalid aliyun upload signature")
	}

	key, ok := uploadSignature["key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("missing or invalid aliyun upload key")
	}

	bucket, ok := uploadSignature["bucket"].(string)
	if !ok || bucket == "" {
		return fmt.Errorf("missing or invalid aliyun upload bucket")
	}

	endpoint, ok := uploadSignature["endpoint"].(string)
	if !ok || endpoint == "" {
		return fmt.Errorf("missing or invalid aliyun upload endpoint")
	}

	accessKeyID, ok := uploadSignature["OSSAccessKeyId"].(string)
	if !ok || accessKeyID == "" {
		return fmt.Errorf("missing or invalid aliyun upload access key ID")
	}

	// 构建上传URL
	uploadURL := fmt.Sprintf("https://%s", endpoint)

	c.logger.Debug("Uploading to Aliyun OSS: %s -> %s (%d bytes)", cloudPath, uploadURL, len(fileData))

	// 创建multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// 添加必需字段
	fields := map[string]string{
		"key":                   key,
		"policy":                policy,
		"OSSAccessKeyId":        accessKeyID,
		"signature":             signature,
		"success_action_status": "200",
	}

	for fieldName, fieldValue := range fields {
		if err := writer.WriteField(fieldName, fieldValue); err != nil {
			return fmt.Errorf("failed to write field %s: %w", fieldName, err)
		}
	}

	// 添加文件字段
	fileName := filepath.Base(cloudPath)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := fileWriter.Write(fileData); err != nil {
		return fmt.Errorf("failed to write file data: %w", err)
	}

	// 关闭writer
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "SCUM-Run-Client/1.0")

	// 发送请求
	httpClient := &http.Client{
		Timeout: 5 * time.Minute,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file to Aliyun OSS: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Warn("Failed to close response body: %v", closeErr)
		}
	}()

	// 读取响应
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Aliyun OSS upload failed",
			"status_code", resp.StatusCode,
			"response", string(responseBody),
			"cloud_path", cloudPath)
		return fmt.Errorf("aliyun OSS upload failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	c.logger.Info("Successfully uploaded file to Aliyun OSS: %s (%d bytes)", cloudPath, len(fileData))
	return nil
}

// handleCloudDownload 处理云存储下载请求
func (c *Client) handleCloudDownload(data interface{}) {
	c.logger.Debug("Handling cloud download request")

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid cloud download request data")
		c.sendResponse(MsgTypeCloudDownload, nil, "Invalid request data")
		return
	}

	filePath, _ := dataMap["file_path"].(string)
	cloudPath, _ := dataMap["cloud_path"].(string)
	transferID, _ := dataMap["transfer_id"].(string)

	if filePath == "" {
		c.logger.Error("File path is required")
		c.sendResponse(MsgTypeCloudDownload, map[string]interface{}{
			"transfer_id": transferID,
		}, "File path is required")
		return
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(filePath, "/") {
		// 绝对路径，直接使用
		fullPath = filePath
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, filePath)
	}

	c.logger.Debug("Downloading file from cloud: %s -> %s", cloudPath, fullPath)

	// TODO: 实现云存储下载逻辑
	// 这里需要根据具体的云存储提供商实现下载逻辑
	// 1. 从云存储下载文件内容
	// 2. 保存到本地文件路径
	// 3. 返回下载结果

	c.logger.Warn("Cloud download not implemented yet")
	c.sendResponse(MsgTypeCloudDownload, map[string]interface{}{
		"transfer_id": transferID,
		"file_path":   filePath,
	}, "Cloud download not implemented yet")
}
