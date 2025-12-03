package client

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/saintfish/chardet"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
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
	"scum_run/internal/utils"
	"scum_run/internal/websocket_client"
	"scum_run/model"
	"scum_run/model/request"
	"sort"
	"strings"
	"sync"
	"syscall"
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

	// 日志文件数据批量处理（用于processLogLine）
	logFileDataBuffer    []string
	logFileDataBufferMux sync.Mutex
	logFileDataTicker    *time.Ticker
	lastLogFileDataSend  time.Time

	// 进程输出批量处理（用于终端显示）
	processOutputBuffer    []string
	processOutputBufferMux sync.Mutex
	processOutputTicker    *time.Ticker
	lastProcessOutputSend  time.Time

	// 通用配置
	maxLogRate    int           // 每秒最大日志发送数量
	logRateWindow time.Duration // 日志频率控制窗口

	// 服务器信息
	serverID    uint // 服务器ID
	ftpProvider uint // FTP服务商类型（3=自建服务器，4=命令行服务器）

	// 自建服务器数据推送
	dataPushTicker *time.Ticker // 数据推送定时器

	// 载具类型的 trade_goods 数据（用于匹配 entity.class）
	vehicleGoodsMap map[string]string // key: entity.class (如 "RIS_ES"), value: trade_goods.name (如 "RIS")
}

// Message types for WebSocket communication
const (
	MsgTypeAuth             = "auth"
	MsgTypeServerStart      = "server_start"
	MsgTypeServerStop       = "server_stop"
	MsgTypeServerRestart    = "server_restart"
	MsgTypeServerStatus     = "server_status"
	MsgTypeDBQuery          = "db_query"
	MsgTypeLogFileData      = "log_file_data"  // SCUM日志文件数据（用于processLogLine处理）
	MsgTypeProcessOutput    = "process_output" // 服务器进程输出（用于终端显示）
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
	MsgTypeClientUpdate     = "client_update"     // 客户端更新

	// File management
	MsgTypeFileBrowse = "file_browse" // 文件浏览
	MsgTypeFileList   = "file_list"   // 文件列表响应
	MsgTypeFileRead   = "file_read"   // 文件内容读取
	MsgTypeFileWrite  = "file_write"  // 文件内容写入

	// System monitoring
	MsgTypeSystemMonitor = "system_monitor"  // 系统监控数据
	MsgTypeGetSystemInfo = "get_system_info" // 获取系统信息

	// Self-built server data push
	MsgTypeSelfBuiltServerData = "self_built_server_data" // 自建服务器数据推送（用户、载具、领地）

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
		config:              cfg,
		steamDir:            steamDir,
		logger:              logger,
		ctx:                 ctx,
		cancel:              cancel,
		db:                  database.New(steamDetector.GetSCUMDatabasePath(steamDir), logger),
		process:             process.New(steamDetector.GetSCUMServerPath(steamDir), logger),
		steamTools:          steamtools.New(&cfg.SteamTools, logger),
		sysMonitor:          monitor.New(logger, 10*time.Second),                    // 每10秒监控一次
		logFileDataBuffer:   make([]string, 0, 100),                                 // 预分配100条日志文件数据的缓冲区
		processOutputBuffer: make([]string, 0, 100),                                 // 预分配100条进程输出的缓冲区
		maxLogRate:          _const.LogMaxRatePerSecond,                             // 每秒最多发送日志数量
		logRateWindow:       time.Duration(_const.LogRateWindow) * time.Millisecond, // 频率控制窗口
	}

	// 设置进程输出回调函数
	client.process.SetOutputCallback(client.handleProcessOutput)

	// 设置系统监控回调函数
	client.sysMonitor.SetCallback(client.handleSystemMonitorData)

	// 启动日志文件数据批量处理定时器
	client.logFileDataTicker = time.NewTicker(time.Duration(_const.LogBatchInterval) * time.Millisecond) // 批量发送间隔
	go client.logFileDataBatchProcessor()

	// 启动进程输出批量处理定时器
	client.processOutputTicker = time.NewTicker(time.Duration(_const.LogBatchInterval) * time.Millisecond) // 批量发送间隔
	go client.processOutputBatchProcessor()

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
			// 连接成功后自动发送认证
			authMsg := request.WebSocketMessage{
				Type: MsgTypeAuth,
				Data: map[string]interface{}{
					"token": c.config.Token,
				},
			}
			if err = c.wsClient.SendMessage(authMsg); err != nil {
				c.logger.Error("Failed to send authentication: %v", err)
			}
		},
		func() {
			c.logger.Warn("WebSocket disconnected")
		},
		func() {
			// 重连成功后重新发送认证
			authMsg := request.WebSocketMessage{
				Type: MsgTypeAuth,
				Data: map[string]interface{}{
					"token": c.config.Token,
				},
			}
			if err = c.wsClient.SendMessage(authMsg); err != nil {
				c.logger.Error("Failed to send re-authentication: %v", err)
			}
		},
	)

	// 使用自动重连连接
	if err = c.wsClient.ConnectWithAutoReconnect(); err != nil {
		return fmt.Errorf("failed to connect to WebSocket server: %w", err)
	}

	// Request configuration sync after authentication
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		// Wait a bit for authentication to complete
		time.Sleep(_const.DefaultWaitTime)
		c.requestConfigSync()
	}()

	// Start message handler
	c.wg.Add(1)
	go c.handleMessages()

	// Start system monitoring
	if err := c.sysMonitor.Start(); err != nil {
		c.logger.Error("Failed to start system monitor: %v", err)
	}
	// WebSocket client handles heartbeat automatically

	// Check if SCUM server is installed before initializing database and log monitor
	steamDetector := steam.NewDetector(c.logger)

	// 检查SCUM服务器是否已安装
	isInstalled := c.checkServerInstallation(steamDetector)

	if !isInstalled {
		// 检查是否启用自动安装
		if c.config.AutoInstall.Enabled {
			c.logger.Info("Auto-install is enabled, starting SCUM server installation...")
			go c.performAutoInstall()
		} else {
			c.logger.Info("Please install SCUM Dedicated Server first, or use the web interface to install it")
		}
	} else {
		c.initializeServerComponents(steamDetector)
	}

	return nil
}

// Stop stops the client
func (c *Client) Stop() {
	c.logger.Info("Stopping SCUM Run client...")

	c.cancel()

	// 停止日志文件数据批量处理定时器
	if c.logFileDataTicker != nil {
		c.logFileDataTicker.Stop()
	}

	// 停止进程输出批量处理定时器
	if c.processOutputTicker != nil {
		c.processOutputTicker.Stop()
	}

	// 停止自建服务器数据推送定时器
	if c.dataPushTicker != nil {
		c.dataPushTicker.Stop()
	}

	// 发送剩余的日志文件数据缓冲区
	c.flushLogFileDataBuffer()

	// 发送剩余的进程输出缓冲区
	c.flushProcessOutputBuffer()

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
}

// ForceStop forcefully stops the client and all associated processes
func (c *Client) ForceStop() {
	c.cancel()

	// 停止日志文件数据批量处理定时器
	if c.logFileDataTicker != nil {
		c.logFileDataTicker.Stop()
	}

	// 停止进程输出批量处理定时器
	if c.processOutputTicker != nil {
		c.processOutputTicker.Stop()
	}

	// 停止自建服务器数据推送定时器
	if c.dataPushTicker != nil {
		c.dataPushTicker.Stop()
	}

	// 发送剩余的日志文件数据缓冲区
	c.flushLogFileDataBuffer()

	// 发送剩余的进程输出缓冲区
	c.flushProcessOutputBuffer()

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
				time.Sleep(_const.DefaultWaitTime)
				continue
			}

			var msg request.WebSocketMessage
			if err := c.wsClient.ReadMessage(&msg); err != nil {
				// 使用更详细的错误处理
				if strings.Contains(err.Error(), "connection not running") ||
					strings.Contains(err.Error(), "websocket: close") {
					time.Sleep(_const.DefaultWaitTime)
				} else {
					c.logger.Error("Failed to read WebSocket message: %v", err)
					time.Sleep(_const.ShortWaitTime)
				}
				continue
			}

			c.handleMessage(msg)
		}
	}
}

// handleMessage handles a single WebSocket message
func (c *Client) handleMessage(msg request.WebSocketMessage) {
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
	case MsgTypeGetSystemInfo:
		c.handleGetSystemInfo()
	default:
		c.logger.Warn("Unknown message type: %s", msg.Type)
	}
}

// handleServerStart handles server start request
func (c *Client) handleServerStart() {
	c.logger.Info("🔍 [DEBUG] 接收到服务器启动请求")
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
		c.logger.Info("🔍 Initializing log monitor for path: %s", logsPath)
		c.logMonitor = logmonitor.New(logsPath, c.logger, c.onLogUpdate)
		if err := c.logMonitor.Start(); err != nil {
			c.logger.Error("❌ Failed to start log monitor: %v", err)
		}
	} else if c.logMonitor == nil {
		c.logger.Warn("⚠️ Log monitor not initialized: SCUM logs directory not available at %s", c.steamDir)
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
			time.Sleep(_const.DefaultWaitTime)

			// 使用重试机制而不是单次检查
			maxRetries := _const.ClientRetryCount
			for i := 0; i < maxRetries; i++ {
				if c.db.IsAvailable() {
					if err := c.db.Initialize(); err != nil {
						c.logger.Warn("Failed to initialize database after server start (attempt %d): %v", i+1, err)
					} else {
						c.logger.Info("Database connection initialized successfully after server start")
						return
					}
				}
				time.Sleep(_const.ShortWaitTime)
			}
		}()
	}()
}

// handleServerStop handles server stop request
func (c *Client) handleServerStop() {
	c.logger.Info("🔍 [DEBUG] 接收到服务器停止请求")
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
	c.logger.Info("🔍 [DEBUG] 接收到服务器重启请求")
	// Stop first
	if err := c.process.Stop(); err != nil {
		c.logger.Warn("Failed to stop server gracefully: %v", err)
	}

	// 减少等待时间，提高重启速度
	time.Sleep(_const.ShortWaitTime)

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

	// 获取query_id用于响应
	queryID, _ := queryData["query_id"].(string)

	// 智能判断是查询还是命令
	queryUpper := strings.ToUpper(strings.TrimSpace(query))
	isCommand := strings.HasPrefix(queryUpper, "INSERT") ||
		strings.HasPrefix(queryUpper, "UPDATE") ||
		strings.HasPrefix(queryUpper, "DELETE") ||
		strings.HasPrefix(queryUpper, "CREATE") ||
		strings.HasPrefix(queryUpper, "DROP") ||
		strings.HasPrefix(queryUpper, "ALTER")

	if isCommand {
		// 执行命令
		rowsAffected, err := c.db.Execute(query)
		if err != nil {
			// 在错误响应中包含query_id
			errorData := map[string]interface{}{}
			if queryID != "" {
				errorData["query_id"] = queryID
			}
			c.sendResponse(MsgTypeDBQuery, errorData, fmt.Sprintf("Command failed: %v", err))
			return
		}

		// 在成功响应中包含query_id和rows_affected
		responseData := map[string]interface{}{
			"rows_affected": rowsAffected,
		}
		if queryID != "" {
			responseData["query_id"] = queryID
		}

		c.sendResponse(MsgTypeDBQuery, responseData, "")
	} else {
		// 执行查询
		result, err := c.db.Query(query)
		if err != nil {
			// 在错误响应中包含query_id
			errorData := map[string]interface{}{}
			if queryID != "" {
				errorData["query_id"] = queryID
			}
			c.sendResponse(MsgTypeDBQuery, errorData, fmt.Sprintf("Query failed: %v", err))
			return
		}

		// 在成功响应中包含query_id和result
		responseData := map[string]interface{}{
			"result": result,
		}
		if queryID != "" {
			responseData["query_id"] = queryID
		}

		c.sendResponse(MsgTypeDBQuery, responseData, "")
	}
}

// onLogUpdate 处理SCUM日志文件更新，只发送日志文件数据给processLogLine处理
func (c *Client) onLogUpdate(filename string, lines []string) {
	// 对日志行进行编码转换
	var convertedLines []string
	if _const.EncodingDetectionEnabled {
		for _, line := range lines {
			convertedLine, encoding, err := utils.ConvertToUTF8(line)
			if err != nil {
				c.logger.Warn("🔤 日志行编码转换失败: %v, 使用原始内容", err)
				convertedLines = append(convertedLines, line)
			} else if encoding != utils.EncodingUTF8 {
				convertedLines = append(convertedLines, convertedLine)
			} else {
				convertedLines = append(convertedLines, line)
			}
		}
	} else {
		convertedLines = lines
	}

	// 只发送SCUM日志文件数据，用于processLogLine处理
	// 不再发送重复的log_update通知
	addedCount := 0
	for _, line := range convertedLines {
		if strings.TrimSpace(line) != "" {
			c.addLogFileDataToBuffer(line)
			addedCount++
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

	// 添加消息发送追踪
	if err := c.wsClient.SendMessage(response); err != nil {
		c.logger.Error("❌ 发送 %s 响应失败: %v", msgType, err)
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
	}
}

// handleConfigSync handles configuration sync from server
func (c *Client) handleConfigSync(data interface{}) {
	configData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid config sync data format")
		return
	}
	c.updateServerConfig(configData)
}

// handleConfigUpdate handles configuration updates from server
func (c *Client) handleConfigUpdate(data interface{}) {
	configData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid config update data format")
		return
	}
	c.updateServerConfig(configData)
}

// updateServerConfig updates the local server configuration
func (c *Client) updateServerConfig(configData map[string]interface{}) {
	serverConfig := &model.ServerConfig{}

	// 保存服务器ID和FTP服务商类型
	if serverID, ok := configData["server_id"].(float64); ok {
		c.serverID = uint(serverID)
	}
	if ftpProvider, ok := configData["ftp_provider"].(float64); ok {
		c.ftpProvider = uint(ftpProvider)
		c.logger.Info("Server FTP provider type: %d", c.ftpProvider)

		// 如果是自建服务器（3）或命令行服务器（4），启动数据推送定时器
		if (c.ftpProvider == 3 || c.ftpProvider == 4) && c.dataPushTicker == nil {
			c.logger.Info("Starting self-built server data push timer (every 3 seconds)")
			c.dataPushTicker = time.NewTicker(3 * time.Second)
			go c.selfBuiltServerDataPusher()
		}
	}

	// 保存载具类型的 trade_goods 数据
	if vehicleGoods, ok := configData["vehicle_goods"].([]interface{}); ok {
		c.vehicleGoodsMap = make(map[string]string)
		for _, item := range vehicleGoods {
			if good, ok := item.(map[string]interface{}); ok {
				name, _ := good["name"].(string)
				code, _ := good["code"].(string)
				if name != "" {
					// 构建 entity.class 格式：name + "_ES"
					entityClass := name + "_ES"
					c.vehicleGoodsMap[entityClass] = name
					c.logger.Debug("Loaded vehicle good: %s -> %s", entityClass, name)
				}
				// 也保存 code 映射（如果 code 包含载具名称）
				if code != "" && strings.HasPrefix(code, "#spawnvehicle ") {
					vehicleName := strings.TrimPrefix(code, "#spawnvehicle ")
					entityClass := vehicleName + "_ES"
					c.vehicleGoodsMap[entityClass] = vehicleName
				}
			}
		}
		c.logger.Info("Loaded %d vehicle goods from server config", len(c.vehicleGoodsMap))
	}

	// 命令行服务器（4）使用不同的配置逻辑
	if c.ftpProvider == 4 {
		// 命令行服务器：install_path 是运行目录，additional_args 是完整的启动命令
		if installPath, ok := configData["install_path"].(string); ok && installPath != "" {
			// 对于命令行服务器，install_path 就是运行目录，不需要拼接 SCUMServer.exe
			serverConfig.ExecPath = installPath
		}
		// 命令行服务器不需要设置 GamePort
		serverConfig.GamePort = 0
	} else {
		// 普通 SCUM 服务器
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

	// 更新SteamCmd路径配置
	if steamCmdPath, ok := configData["steamcmd_path"].(string); ok && steamCmdPath != "" {
		c.config.AutoInstall.SteamCmdPath = steamCmdPath
		c.logger.Info("Updated SteamCmd path from server config: %s", steamCmdPath)
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
				time.Sleep(_const.LongWaitTime)
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
	}
}

// handleDownloadSteamCmd handles SteamCmd download requests
func (c *Client) handleDownloadSteamCmd(_ interface{}) {
	// 在后台执行SteamCmd下载
	go c.performSteamCmdDownload()
}

// performAutoInstall performs automatic SCUM server installation on startup
func (c *Client) performAutoInstall() {
	// 检查是否已经在安装中
	c.installMux.Lock()
	if c.installing {
		c.installMux.Unlock()
		c.logger.Info("安装已在进行中，跳过自动安装")
		return
	}
	c.installing = true
	c.installMux.Unlock()

	defer func() {
		c.installMux.Lock()
		c.installing = false
		c.installMux.Unlock()
	}()

	c.logger.Info("🚀 开始自动安装 SCUM 服务器...")

	// 获取配置参数
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}
	c.logger.Info("安装路径: %s", installPath)

	steamCmdPath := c.config.AutoInstall.SteamCmdPath
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
	}
	c.logger.Info("SteamCmd 路径: %s", steamCmdPath)

	forceReinstall := c.config.AutoInstall.ForceReinstall
	if forceReinstall {
		c.logger.Info("强制重新安装已启用")
	}

	// 执行安装
	c.logger.Info("开始执行 SCUM 服务器安装...")
	c.performServerInstallation(installPath, steamCmdPath, forceReinstall)

	// 安装完成后，重新初始化组件
	c.logger.Info("安装流程完成，正在初始化组件...")
	c.initializeComponentsAfterInstall()
	c.logger.Info("✅ 自动安装流程完成")
}

// initializeComponentsAfterInstall initializes components after server installation
func (c *Client) initializeComponentsAfterInstall() {
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

	// 检查是否需要自动启动服务器
	if c.config.AutoInstall.AutoStartAfterInstall {
		go func() {
			// 等待一段时间让组件完全初始化
			time.Sleep(_const.DefaultWaitTime)
			c.handleServerStart()
		}()
	}
}

// performServerInstallation performs the actual server installation
func (c *Client) performServerInstallation(installPath, steamCmdPath string, forceReinstall bool) {
	c.logger.Info("📦 开始执行 SCUM 服务器安装流程...")

	// 设置默认SteamCmd路径（如果为空）
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
		c.logger.Info("使用默认 SteamCmd 路径: %s", steamCmdPath)
	}

	// 将相对路径转换为绝对路径
	absPath, err := filepath.Abs(steamCmdPath)
	if err != nil {
		c.logger.Warn("无法获取 SteamCmd 绝对路径，使用原始路径: %v", err)
		absPath = steamCmdPath
	} else {
		steamCmdPath = absPath
		c.logger.Info("SteamCmd 绝对路径: %s", steamCmdPath)
	}

	// 确保路径使用正确的分隔符
	steamCmdPath = filepath.Clean(steamCmdPath)

	// 检查SteamCmd是否存在
	if _, err = os.Stat(steamCmdPath); os.IsNotExist(err) {
		c.logger.Info("SteamCmd 未找到，路径: %s，开始下载...", steamCmdPath)
		if err = c.downloadSteamCmd(); err != nil {
			c.logger.Error("❌ SteamCmd 下载失败: %v", err)
			return
		}

		// 再次检查SteamCmd是否存在，使用绝对路径
		absDownloadPath, _ := filepath.Abs(_const.DefaultSteamCmdPath)
		if _, err = os.Stat(absDownloadPath); os.IsNotExist(err) {
			c.logger.Error("❌ SteamCmd 下载后仍未找到，路径: %s", absDownloadPath)
			return
		}
		// 更新steamCmdPath为下载后的绝对路径
		steamCmdPath = absDownloadPath
		c.logger.Info("✅ SteamCmd 下载完成，路径已更新: %s", steamCmdPath)
	} else {
		c.logger.Info("✅ SteamCmd 已存在: %s", steamCmdPath)
	}

	// 设置安装路径
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	// 将安装路径转换为绝对路径
	absInstallPath, err := filepath.Abs(installPath)
	if err != nil {
		c.logger.Warn("无法获取安装目录绝对路径，使用原始路径: %v", err)
		absInstallPath = installPath
	} else {
		installPath = absInstallPath
	}

	// 确保安装路径使用正确的分隔符
	installPath = filepath.Clean(installPath)
	c.logger.Info("SCUM 服务器安装路径: %s", installPath)

	// 创建安装目录
	c.logger.Info("创建安装目录: %s", installPath)
	if err = os.MkdirAll(installPath, 0755); err != nil {
		c.logger.Error("❌ 创建安装目录失败: %v", err)
		return
	}
	c.logger.Info("✅ 安装目录创建成功")

	// 构建SteamCmd命令（使用 +quit 而不是 +exit）
	args := []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+app_update", _const.SCUMServerAppID, "validate",
		"+quit",
	}

	// 再次验证SteamCmd文件是否存在且可执行
	c.logger.Info("验证 SteamCmd 可执行文件...")
	if err := c.validateSteamCmdExecutable(steamCmdPath); err != nil {
		c.logger.Error("❌ SteamCmd 验证失败: %v", err)
		return
	}
	c.logger.Info("✅ SteamCmd 验证通过")

	// 先初始化 SteamCmd（第一次运行时会自动更新）
	c.logger.Info("🔧 初始化 SteamCmd（首次运行会自动更新依赖）...")
	if err := c.initializeSteamCmd(steamCmdPath); err != nil {
		c.logger.Warn("SteamCmd 初始化警告（可能已初始化）: %v", err)
		// 继续执行，因为可能已经初始化过了
	} else {
		c.logger.Info("✅ SteamCmd 初始化完成")
	}

	// 执行SteamCmd安装
	steamCmdDir := filepath.Dir(steamCmdPath)
	c.logger.Info("工作目录: %s", steamCmdDir)
	c.logger.Info("开始执行 SteamCmd 安装命令（这可能需要较长时间，请耐心等待）...")
	if err := c.runSteamCmdWithRealtimeOutput(steamCmdPath, args); err != nil {
		c.logger.Error("SteamCmd 安装失败: %v", err)
		return
	}

	c.logger.Info("SteamCmd 命令执行完成，正在验证安装结果...")

	// 验证安装是否成功
	scumServerExe := filepath.Join(installPath, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	c.logger.Info("检查 SCUM 服务器可执行文件: %s", scumServerExe)
	if _, err := os.Stat(scumServerExe); err != nil {
		c.logger.Error("SCUM 服务器可执行文件未找到: %s", scumServerExe)
		c.logger.Error("安装完成但未找到 SCUM 服务器可执行文件")

		// 列出安装目录内容以便调试
		installDir := filepath.Dir(filepath.Dir(filepath.Dir(scumServerExe)))
		c.logger.Info("检查安装目录: %s", installDir)
		if entries, err := os.ReadDir(installDir); err == nil {
			c.logger.Info("安装目录内容:")
			for _, entry := range entries {
				c.logger.Info("  - %s", entry.Name())
			}
		}
		return
	}

	c.logger.Info("✅ SCUM 服务器安装成功: %s", scumServerExe)
}

// runSteamCmdWithRealtimeOutput 执行SteamCmd命令并实时打印输出
func (c *Client) runSteamCmdWithRealtimeOutput(steamCmdPath string, args []string) error {
	steamCmdDir := filepath.Dir(steamCmdPath)
	cmd := exec.Command(steamCmdPath, args...)
	cmd.Dir = steamCmdDir
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建stdout管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建stderr管道失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动命令失败: %w", err)
	}

	// 实时打印输出
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			c.logger.Info("SteamCmd: %s", scanner.Text())
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			c.logger.Info("SteamCmd: %s", scanner.Text())
		}
	}()

	return cmd.Wait()
}

// initializeSteamCmd initializes SteamCmd by running it once to download updates
func (c *Client) initializeSteamCmd(steamCmdPath string) error {
	c.logger.Info("运行 SteamCmd 进行初始化（首次运行会自动更新）...")
	initArgs := []string{"+quit"}
	if err := c.runSteamCmdWithRealtimeOutput(steamCmdPath, initArgs); err != nil {
		c.logger.Debug("SteamCmd 初始化完成（可能已初始化）: %v", err)
	}
	return nil
}

// checkServerInstallation checks if SCUM server is installed in multiple possible locations
func (c *Client) checkServerInstallation(steamDetector *steam.Detector) bool {
	// 首先检查配置的steamDir
	if c.steamDir != "" && steamDetector.IsSCUMServerInstalled(c.steamDir) {
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

	// 检查SteamCmd是否可用
	steamCmdPath := c.config.AutoInstall.SteamCmdPath
	if steamCmdPath == "" {
		steamCmdPath = _const.DefaultSteamCmdPath
	}

	// 验证SteamCmd是否存在
	if err := c.validateSteamCmdExecutable(steamCmdPath); err != nil {
		c.logger.Error("SteamCmd validation failed: %v", err)
		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":    "check",
			"status":  "failed",
			"message": "SteamCmd not available: " + err.Error(),
		}, "")
		return
	}

	// 使用SteamCmd检查更新
	updateAvailable, err := c.checkSteamUpdate(steamCmdPath)
	if err != nil {
		c.logger.Error("Failed to check for updates: %v", err)
		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":    "check",
			"status":  "failed",
			"message": "Update check failed: " + err.Error(),
		}, "")
		return
	}

	if updateAvailable {
		c.logger.Info("SCUM server update is available")
		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":             "check",
			"status":           "completed",
			"message":          "Update available",
			"update_available": true,
		}, "")
	} else {
		c.logger.Info("SCUM server is up to date")
		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":             "check",
			"status":           "completed",
			"message":          "No updates available",
			"update_available": false,
		}, "")
	}
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
		// 临时禁用自动启动，避免更新后自动启动服务器
		originalAutoStart := c.config.AutoInstall.AutoStartAfterInstall
		c.config.AutoInstall.AutoStartAfterInstall = false

		c.performServerInstallation(installPath, steamCmdPath, forceReinstall)

		// 恢复原始配置
		c.config.AutoInstall.AutoStartAfterInstall = originalAutoStart

		// 安装完成后重新初始化组件
		c.initializeComponentsAfterInstall()

		c.sendResponse(MsgTypeServerUpdate, map[string]interface{}{
			"type":    "install",
			"status":  "completed",
			"message": "Server update installation completed. Server is stopped and ready for manual start.",
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
	c.logger.Info("📅 [定时重启] 接收到定时重启请求，数据: %+v", data)

	restartData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("📅 [定时重启] 数据格式错误，无法解析")
		c.sendResponse(MsgTypeScheduledRestart, nil, "Invalid restart request data format")
		return
	}

	// 获取重启原因
	reason := "Scheduled restart"
	if reasonStr, exists := restartData["reason"].(string); exists && reasonStr != "" {
		reason = reasonStr
	}
	c.logger.Info("📅 [定时重启] 重启原因: %s", reason)

	// 检查服务器是否在运行
	if c.process == nil {
		c.logger.Warn("📅 [定时重启] 进程管理器为空，无法重启")
		c.sendResponse(MsgTypeScheduledRestart, map[string]interface{}{
			"status":  "skipped",
			"reason":  "Process manager is nil",
			"message": "Scheduled restart skipped - process manager is nil",
		}, "")
		return
	}

	if !c.process.IsRunning() {
		c.logger.Info("📅 [定时重启] 服务器未运行，跳过重启")
		c.sendResponse(MsgTypeScheduledRestart, map[string]interface{}{
			"status":  "skipped",
			"reason":  "Server is not running",
			"message": "Scheduled restart skipped - server is not running",
		}, "")
		return
	}

	// 执行重启
	c.logger.Info("📅 [定时重启] 开始执行重启操作...")
	if err := c.process.Restart(); err != nil {
		c.logger.Error("📅 [定时重启] 重启失败: %v", err)
		c.sendResponse(MsgTypeScheduledRestart, nil, fmt.Sprintf("Failed to restart server: %v", err))
		return
	}

	newPID := c.process.GetPID()
	c.logger.Info("📅 [定时重启] 重启成功，新进程PID: %d", newPID)
	c.sendResponse(MsgTypeScheduledRestart, map[string]interface{}{
		"status":  "restarted",
		"reason":  reason,
		"pid":     newPID,
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

	c.logger.Info("开始下载 SteamCmd，URL: %s", steamCmdURL)
	c.logger.Info("SteamCmd 目标目录: %s", steamCmdDir)

	// 创建目录
	if err := os.MkdirAll(steamCmdDir, 0755); err != nil {
		return fmt.Errorf("failed to create steamcmd directory: %w", err)
	}

	// 下载文件
	c.logger.Info("正在下载 SteamCmd...")
	response, err := http.Get(steamCmdURL)
	if err != nil {
		return fmt.Errorf("failed to download steamcmd: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			c.logger.Warn("Failed to close response body: %v", err)
		}
	}()

	// 检查响应状态码
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download steamcmd: HTTP %d", response.StatusCode)
	}

	// 创建临时文件
	tempFile := filepath.Join(steamCmdDir, "steamcmd.zip")
	out, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// 写入文件
	c.logger.Info("正在保存 SteamCmd 到临时文件: %s", tempFile)
	_, err = io.Copy(out, response.Body)
	if err != nil {
		out.Close()
		return fmt.Errorf("failed to write steamcmd.zip: %w", err)
	}

	// 关闭文件句柄，确保数据写入磁盘
	if err := out.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// 获取文件大小
	var fileSize int64
	if info, err := os.Stat(tempFile); err == nil {
		fileSize = info.Size()
	}
	c.logger.Info("SteamCmd 下载完成，文件大小: %d bytes", fileSize)

	// 解压文件
	c.logger.Info("正在解压 SteamCmd...")
	if err := c.extractZip(tempFile, steamCmdDir); err != nil {
		return fmt.Errorf("failed to extract steamcmd.zip: %w", err)
	}
	c.logger.Info("SteamCmd 解压完成")

	// 等待一小段时间，确保所有文件句柄都已关闭
	time.Sleep(500 * time.Millisecond)

	// 删除临时文件
	if err := os.Remove(tempFile); err != nil {
		c.logger.Warn("Failed to remove temp file %s: %v (this is not critical)", tempFile, err)
		// 尝试延迟删除
		go func() {
			time.Sleep(2 * time.Second)
			if err := os.Remove(tempFile); err != nil {
				c.logger.Debug("Failed to remove temp file after delay: %v", err)
			}
		}()
	} else {
		c.logger.Info("临时文件已删除: %s", tempFile)
	}

	// 验证SteamCmd是否成功解压
	expectedPath := _const.DefaultSteamCmdPath
	c.logger.Info("验证 SteamCmd 可执行文件: %s", expectedPath)
	if _, err := os.Stat(expectedPath); err != nil {
		return fmt.Errorf("steamcmd.exe not found after extraction at %s: %w", expectedPath, err)
	}

	c.logger.Info("SteamCmd 下载和安装成功: %s", expectedPath)
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
		c.logger.Error("Invalid command data format")
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"success": false,
			"output":  "Invalid command data format",
		}, "Invalid command data format")
		return
	}

	command, ok := commandData["command"].(string)
	if !ok || command == "" {
		c.logger.Error("Command is empty or not a string")
		c.sendResponse(MsgTypeCommandResult, map[string]interface{}{
			"success": false,
			"output":  "Command is required",
		}, "Command is required")
		return
	}

	// 执行服务器命令
	output, err := c.executeServerCommand(command)
	if err != nil {
		c.logger.Error("Command execution failed: %v", err)
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
		c.logger.Error("Process manager is nil")
		return "", fmt.Errorf("process manager is not initialized")
	}

	if !c.process.IsRunning() {
		c.logger.Error("Server is not running")
		return "", fmt.Errorf("server is not running")
	}

	// 发送命令到SCUM服务器
	if err := c.process.SendCommand(command); err != nil {
		c.logger.Error("Failed to send command to server: %v", err)
		return "", fmt.Errorf("failed to send command to server: %w", err)
	}
	// 发送日志数据显示命令已执行

	return fmt.Sprintf("Command '%s' has been sent to the server", command), nil
}

// sendLogData 发送实时日志数据到Web终端（已弃用 - 使用addProcessOutputToBuffer代替）
func (c *Client) sendLogData(content string) {
	// 使用新的批量处理机制，发送到进程输出缓冲区
	c.addProcessOutputToBuffer(content)
}

// handleProcessOutput 处理SCUM服务器进程的实时输出，发送给终端显示
func (c *Client) handleProcessOutput(_ string, line string) {
	// 发送进程输出数据，用于终端显示
	c.addProcessOutputToBuffer(line)
}

// handleClientUpdate handles client update requests
func (c *Client) handleClientUpdate(data interface{}) {
	c.logger.Info("🔍 [DEBUG] 接收到客户端更新消息，数据: %+v", data)

	updateData, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("❌ 接收到无效的更新请求数据格式")
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, "Invalid update request data format")
		return
	}

	// 检查更新动作
	action, ok := updateData["action"].(string)
	if !ok {
		c.logger.Error("❌ 更新请求缺少action字段")
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, "Missing update action")
		return
	}

	c.logger.Info("🔄 接收到客户端更新请求: action=%s, 完整数据: %+v", action, updateData)

	switch action {
	case "update":
		// 检查是否需要先停止服务器
		stopServer, _ := updateData["stop_server"].(bool)
		if stopServer {
			c.logger.Info("🛑 更新前需要先停止SCUM服务器...")
			if c.process != nil && c.process.IsRunning() {
				if err := c.process.Stop(); err != nil {
					c.logger.Error("❌ 更新前停止服务器失败: %v", err)
					c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
						"type":   "self_update",
						"status": _const.UpdateStatusFailed,
					}, fmt.Sprintf("Failed to stop server: %v", err))
					return
				}
				c.logger.Info("✅ 服务器已成功停止，继续客户端更新")
			}
		}

		// 获取下载链接
		downloadURL, _ := updateData["download_url"].(string)
		c.logger.Info("📥 获取到下载链接: %s", downloadURL)

		// 启动自我更新流程，传递下载链接
		c.logger.Info("🚀 启动客户端自我更新流程...")
		go c.performSelfUpdateWithURL(downloadURL)
	default:
		c.logger.Error("❌ 未知的更新动作: %s", action)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Unknown update action: %s", action))
	}
}

// performSelfUpdateWithURL performs the self-update process using provided download URL
func (c *Client) performSelfUpdateWithURL(downloadURL string) {
	c.logger.Info("🔄 开始执行客户端自我更新流程")

	// 发送更新开始状态
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusChecking,
	}, "Starting update with provided download URL...")

	if downloadURL == "" {
		c.logger.Error("❌ 未提供下载链接")
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, "No download URL provided")
		return
	}

	c.logger.Info("📥 更新下载链接: %s", downloadURL)

	// 在更新前优雅地停止SCUM服务器
	if c.process != nil && c.process.IsRunning() {
		c.logger.Info("🛑 检测到SCUM服务器正在运行，发送Ctrl+C信号进行优雅关闭...")

		// 发送更新状态，告知正在停止服务器
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusChecking,
		}, "Stopping SCUM server before update...")

		// 优雅停止SCUM服务器
		if err := c.process.Stop(); err != nil {
			c.logger.Warn("⚠️ 优雅停止SCUM服务器失败，将强制停止: %v", err)
			// 如果优雅停止失败，尝试强制停止
			if forceErr := c.process.ForceStop(); forceErr != nil {
				c.logger.Error("❌ 强制停止SCUM服务器也失败: %v", forceErr)
			} else {
				c.logger.Info("✅ SCUM服务器已强制停止")
			}
		} else {
			c.logger.Info("✅ SCUM服务器已优雅停止")
		}

		// 等待一段时间确保服务器完全停止
		time.Sleep(_const.LongWaitTime)
	} else {
		c.logger.Info("ℹ️ SCUM服务器未运行，无需停止")
	}

	// 准备更新配置
	currentExe, err := os.Executable()
	if err != nil {
		c.logger.Error("❌ 获取可执行文件路径失败: %v", err)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Failed to get executable path: %v", err))
		return
	}

	c.logger.Info("📁 当前可执行文件路径: %s", currentExe)

	updateConfig := model.UpdaterConfig{
		CurrentExePath: currentExe,
		UpdateURL:      downloadURL,
		Args:           os.Args[1:], // 排除程序名本身
	}

	c.logger.Info("⚙️ 更新配置已准备: URL=%s, Args=%v", updateConfig.UpdateURL, updateConfig.Args)

	// 发送更新状态并启动外部更新器
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusDownloading,
	}, "Starting updater with provided download URL...")

	c.logger.Info("🚀 启动外部更新器...")

	// 启动外部更新器
	if err := updater.ExecuteUpdate(updateConfig); err != nil {
		c.logger.Error("❌ 启动更新器失败: %v", err)
		c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
			"type":   "self_update",
			"status": _const.UpdateStatusFailed,
		}, fmt.Sprintf("Failed to start updater: %v", err))
		return
	}

	c.logger.Info("✅ 外部更新器已启动，准备关闭当前进程...")

	// 发送最终状态
	c.sendResponse(MsgTypeClientUpdate, map[string]interface{}{
		"type":   "self_update",
		"status": _const.UpdateStatusInstalling,
	}, "Updater started, shutting down for update...")

	// 延迟一段时间让消息发送完成，然后强制退出让更新器接管
	go func() {
		time.Sleep(_const.ShortWaitTime) // 减少等待时间，确保更新器脚本先启动
		c.logger.Info("🔄 正在退出以进行更新...")
		c.logger.Info("🔍 [DEBUG] 即将执行 os.Exit(0) 进行客户端更新")
		// 使用 syscall.Exit 强制退出，不等待子进程
		if runtime.GOOS == "windows" {
			syscall.Exit(0)
		} else {
			os.Exit(0)
		}
	}()
}

// performSelfUpdate performs the self-update process using external updater (legacy method)
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

	updateConfig := model.UpdaterConfig{
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
		time.Sleep(_const.DefaultWaitTime)
		c.logger.Info("Exiting for update...")
		c.logger.Info("🔍 [DEBUG] 即将执行 os.Exit(0) 进行客户端更新 (legacy方法)")
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
	// 文件列表响应通常不会在客户端收到
}

// handleFileRead 处理文件内容读取请求 - 只传输文件，不进行转码
func (c *Client) handleFileRead(data interface{}) {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid file read request data")
		c.sendResponse(MsgTypeFileRead, nil, "Invalid request data")
		return
	}

	path, _ := dataMap["path"].(string)
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

	// 直接读取文件原始字节，不进行任何转码
	// 转码工作交由前端处理
	fileData, err := os.ReadFile(fullPath)
	if err != nil {
		c.logger.Error("Failed to read file %s: %v", fullPath, err)
		errorData := map[string]interface{}{}
		if requestID != "" {
			errorData["request_id"] = requestID
		}
		c.sendResponse(MsgTypeFileRead, errorData, fmt.Sprintf("Failed to read file: %v", err))
		return
	}

	// 发送文件内容响应 - 返回原始字节数据
	responseData := map[string]interface{}{
		"content": string(fileData), // 直接返回原始字节数据
		"size":    len(fileData),
	}

	// 在响应中包含请求ID
	if requestID != "" {
		responseData["request_id"] = requestID
	}

	c.logger.Info("Successfully read file: %s (size: %d bytes)", path, len(fileData))
	c.sendResponse(MsgTypeFileRead, responseData, "")
}

// handleFileWrite 处理文件内容写入请求
func (c *Client) handleFileWrite(data interface{}) {

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

	// 允许空内容，用户可能想要清空文件
	// 不再检查 content 是否为空

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

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// addLogFileDataToBuffer 添加SCUM日志文件数据到缓冲区，用于processLogLine处理
func (c *Client) addLogFileDataToBuffer(content string) {
	c.logFileDataBufferMux.Lock()
	defer c.logFileDataBufferMux.Unlock()

	// 编码检测和转换
	if _const.EncodingDetectionEnabled {
		convertedContent, encoding, err := utils.ConvertToUTF8(content)
		if err != nil {
			c.logger.Warn("🔤 日志文件数据编码转换失败: %v, 使用原始内容", err)
		} else if encoding != utils.EncodingUTF8 {
			content = convertedContent
		}
	}

	// 检查消息大小限制（单条日志最大1KB）
	if len(content) > _const.MaxLogLineLength {
		content = content[:_const.MaxLogLineLength] + _const.TruncateSuffix + " [truncated]"
	}

	// 检查频率限制
	now := time.Now()
	timeSinceLastSend := now.Sub(c.lastLogFileDataSend)
	if timeSinceLastSend < c.logRateWindow && len(c.logFileDataBuffer) < _const.LogBatchSize/2 {
		return
	}

	// 添加到缓冲区
	c.logFileDataBuffer = append(c.logFileDataBuffer, content)
	// 如果缓冲区满了，立即发送
	if len(c.logFileDataBuffer) >= _const.LogBatchSize {
		c.flushLogFileDataBufferUnsafe()
	}
}

// addProcessOutputToBuffer 添加进程输出到缓冲区，用于终端显示
func (c *Client) addProcessOutputToBuffer(content string) {
	c.processOutputBufferMux.Lock()
	defer c.processOutputBufferMux.Unlock()

	// 编码检测和转换
	if _const.EncodingDetectionEnabled {
		convertedContent, encoding, err := utils.ConvertToUTF8(content)
		if err != nil {
			c.logger.Warn("🔤 进程输出编码转换失败: %v, 使用原始内容", err)
		} else if encoding != utils.EncodingUTF8 {
			content = convertedContent
		}
	}

	// 检查消息大小限制
	if len(content) > _const.MaxLogLineLength {
		content = content[:_const.MaxLogLineLength] + _const.TruncateSuffix + " [truncated]"
	}

	// 检查频率限制
	now := time.Now()
	timeSinceLastSend := now.Sub(c.lastProcessOutputSend)
	if timeSinceLastSend < c.logRateWindow && len(c.processOutputBuffer) < _const.LogBatchSize/2 {
		return
	}

	// 添加到缓冲区
	c.processOutputBuffer = append(c.processOutputBuffer, content)
	// 如果缓冲区满了，立即发送
	if len(c.processOutputBuffer) >= _const.LogBatchSize {
		c.flushProcessOutputBufferUnsafe()
	}
}

// logFileDataBatchProcessor 定期处理日志文件数据批次
func (c *Client) logFileDataBatchProcessor() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.logFileDataTicker.C:
			c.flushLogFileDataBuffer()
		}
	}
}

// processOutputBatchProcessor 定期处理进程输出批次
func (c *Client) processOutputBatchProcessor() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.processOutputTicker.C:
			c.flushProcessOutputBuffer()
		}
	}
}

// flushLogFileDataBuffer 发送所有缓冲的日志文件数据到服务器
func (c *Client) flushLogFileDataBuffer() {
	c.logFileDataBufferMux.Lock()
	defer c.logFileDataBufferMux.Unlock()
	c.flushLogFileDataBufferUnsafe()
}

// flushLogFileDataBufferUnsafe 发送所有缓冲的日志文件数据（调用者必须持有锁）
func (c *Client) flushLogFileDataBufferUnsafe() {
	if len(c.logFileDataBuffer) == 0 {
		return
	}

	// 检查发送频率限制
	now := time.Now()
	timeSinceLastSend := now.Sub(c.lastLogFileDataSend)
	if timeSinceLastSend < c.logRateWindow {
		return
	}

	// 限制批量大小，避免单次发送过多数据
	batchSize := len(c.logFileDataBuffer)
	if batchSize > c.maxLogRate {
		batchSize = c.maxLogRate
	}

	// 发送批量日志文件数据
	batch := make([]string, batchSize)
	copy(batch, c.logFileDataBuffer[:batchSize])

	// 从缓冲区移除已发送的日志
	c.logFileDataBuffer = c.logFileDataBuffer[batchSize:]

	// 发送批量日志文件数据
	c.sendBatchLogFileData(batch)

	// 更新最后发送时间
	c.lastLogFileDataSend = now
}

// flushProcessOutputBuffer 发送所有缓冲的进程输出到服务器
func (c *Client) flushProcessOutputBuffer() {
	c.processOutputBufferMux.Lock()
	defer c.processOutputBufferMux.Unlock()
	c.flushProcessOutputBufferUnsafe()
}

// flushProcessOutputBufferUnsafe 发送所有缓冲的进程输出（调用者必须持有锁）
func (c *Client) flushProcessOutputBufferUnsafe() {
	if len(c.processOutputBuffer) == 0 {
		return
	}

	// 检查发送频率限制
	now := time.Now()
	timeSinceLastSend := now.Sub(c.lastProcessOutputSend)
	if timeSinceLastSend < c.logRateWindow {
		return
	}

	// 限制批量大小，避免单次发送过多数据
	batchSize := len(c.processOutputBuffer)
	if batchSize > c.maxLogRate {
		batchSize = c.maxLogRate
	}

	// 发送批量进程输出
	batch := make([]string, batchSize)
	copy(batch, c.processOutputBuffer[:batchSize])

	// 从缓冲区移除已发送的输出
	c.processOutputBuffer = c.processOutputBuffer[batchSize:]

	// 发送批量进程输出
	c.sendBatchProcessOutput(batch)

	// 更新最后发送时间
	c.lastProcessOutputSend = now
}

// sendBatchLogFileData 发送一批日志文件数据到服务器（用于processLogLine处理）
func (c *Client) sendBatchLogFileData(logs []string) {
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

	c.logger.Info("📡 发送批量日志文件数据到服务器: %d 条日志", len(logContents))
	c.sendResponse(MsgTypeLogFileData, logData, "")
}

// sendBatchProcessOutput 发送一批进程输出到服务器（用于终端显示）
func (c *Client) sendBatchProcessOutput(outputs []string) {
	if len(outputs) == 0 {
		return
	}

	// 确保输出数据格式正确
	var outputContents []interface{}
	for _, output := range outputs {
		if strings.TrimSpace(output) != "" {
			outputContents = append(outputContents, output)
		}
	}

	if len(outputContents) == 0 {
		return
	}

	outputData := map[string]interface{}{
		"content": outputContents,
		"batch":   true, // 标识这是批量数据
	}

	c.sendResponse(MsgTypeProcessOutput, outputData, "")
}

// readFileWithEncoding 根据指定编码读取文件内容
// 已弃用：转码工作已移至前端处理，此函数仅保留用于向后兼容
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

		// 如果检测到的编码不是UTF-8，尝试转换
		if result.Charset != "UTF-8" {
			// 这里可以添加更多编码转换逻辑
			// 目前只处理常见的编码
			switch strings.ToLower(result.Charset) {
			case "gbk", "gb2312":
				decoder := simplifiedchinese.GBK.NewDecoder()
				utf8Data, err := decoder.Bytes(fileData)
				if err != nil {
					return string(fileData), nil
				}
				return string(utf8Data), nil
			default:
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
	// 系统监控消息通常是从服务器发送的配置或控制指令
	// 这里可以根据需要处理服务器发送的系统监控相关指令
}

// handleGetSystemInfo 处理获取系统信息请求
func (c *Client) handleGetSystemInfo() {

	// 收集实时系统监控数据
	var cpuUsage, memoryUsage, diskUsage float64
	var networkStatus string

	// 直接收集系统数据
	if data, err := c.collectSystemDataDirectly(); err == nil {
		cpuUsage = data.CPUUsage
		memoryUsage = data.MemUsage
		diskUsage = data.DiskUsage
		if data.NetIncome > 0 || data.NetOutcome > 0 {
			networkStatus = "active"
		} else {
			networkStatus = "idle"
		}
	}

	// 获取系统运行时间
	uptime := c.getSystemUptime()

	// 获取操作系统信息
	osInfo := c.getOSInfo()

	// 构建系统信息响应
	systemInfo := map[string]interface{}{
		"os":             osInfo,
		"cpu_usage":      cpuUsage,
		"memory_usage":   memoryUsage,
		"disk_usage":     diskUsage,
		"network_status": networkStatus,
		"uptime_seconds": uptime,
		"last_updated":   time.Now().Format(time.RFC3339),
	}

	// 发送响应
	c.sendResponse(MsgTypeGetSystemInfo, systemInfo, "")
}

// collectSystemDataDirectly 直接收集系统数据
func (c *Client) collectSystemDataDirectly() (*request.SystemMonitorData, error) {
	data := &request.SystemMonitorData{
		Timestamp: time.Now().Unix(),
	}

	// 收集CPU使用率
	if err := c.collectCPUUsage(data); err != nil {
		c.logger.Warn("Failed to collect CPU usage: %v", err)
	}

	// 收集内存使用率
	if err := c.collectMemoryUsage(data); err != nil {
		c.logger.Warn("Failed to collect memory usage: %v", err)
	}

	// 收集磁盘使用率
	if err := c.collectDiskUsage(data); err != nil {
		c.logger.Warn("Failed to collect disk usage: %v", err)
	}

	// 收集网络流量
	if err := c.collectNetworkUsage(data); err != nil {
		c.logger.Warn("Failed to collect network usage: %v", err)
	}

	return data, nil
}

// collectCPUUsage 收集CPU使用率
func (c *Client) collectCPUUsage(data *request.SystemMonitorData) error {
	percentages, err := cpu.Percent(time.Second, false)
	if err != nil {
		return fmt.Errorf("failed to get CPU percentage: %w", err)
	}

	if len(percentages) > 0 {
		data.CPUUsage = percentages[0]
	}

	return nil
}

// collectMemoryUsage 收集内存使用率
func (c *Client) collectMemoryUsage(data *request.SystemMonitorData) error {
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return fmt.Errorf("failed to get memory info: %w", err)
	}

	data.MemUsage = memInfo.UsedPercent
	return nil
}

// collectDiskUsage 收集磁盘使用率
func (c *Client) collectDiskUsage(data *request.SystemMonitorData) error {
	// 获取SCUM服务器安装目录的磁盘使用情况
	steamDir := c.steamDir
	if steamDir == "" {
		steamDir = "C:/scumserver" // 默认路径
	}

	diskInfo, err := disk.Usage(steamDir)
	if err != nil {
		return fmt.Errorf("failed to get disk usage: %w", err)
	}

	data.DiskUsage = diskInfo.UsedPercent
	return nil
}

// collectNetworkUsage 收集网络流量
func (c *Client) collectNetworkUsage(data *request.SystemMonitorData) error {
	// 这里可以实现网络流量收集逻辑
	// 暂时返回0，表示没有网络活动
	data.NetIncome = 0
	data.NetOutcome = 0
	return nil
}

// getSystemUptime 获取系统运行时间
func (c *Client) getSystemUptime() int64 {
	// 获取系统启动时间
	bootTime, err := host.BootTime()
	if err != nil {
		c.logger.Warn("Failed to get boot time: %v", err)
		return 0
	}

	// 计算运行时间（秒）
	return time.Now().Unix() - int64(bootTime)
}

// getOSInfo 获取操作系统信息
func (c *Client) getOSInfo() string {
	hostInfo, err := host.Info()
	if err != nil {
		c.logger.Warn("Failed to get host info: %v", err)
		return "Unknown"
	}

	return fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)
}

// handleSystemMonitorData 处理系统监控数据
func (c *Client) handleSystemMonitorData(data *request.SystemMonitorData) {
	// 检查WebSocket连接是否可用
	if !c.wsClient.IsConnected() {
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
		cfg, err := c.getServerConfig()
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
	// 这里可以实现停止备份的逻辑
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"message": "Backup stop request received",
	})
}

// handleBackupStatus 处理备份状态请求
func (c *Client) handleBackupStatus(data interface{}) {
	// 返回当前备份状态
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"status":  "idle",
		"message": "No backup in progress",
	})
}

// handleBackupList 处理备份列表请求
func (c *Client) handleBackupList(data interface{}) {
	// 解析请求数据
	requestData, ok := data.(map[string]interface{})
	if !ok {
		c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
			"success": false,
			"message": "Invalid request data format",
			"list":    []interface{}{},
		})
		return
	}

	// 获取 serverID
	serverIDFloat, ok := requestData["server_id"].(float64)
	if !ok {
		c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
			"success": false,
			"message": "Server ID is missing or invalid",
			"list":    []interface{}{},
		})
		return
	}
	serverID := uint(serverIDFloat)

	// 获取 limit，默认为 50
	limit := 50
	if limitFloat, ok := requestData["limit"].(float64); ok {
		limit = int(limitFloat)
	} else if limitInt, ok := requestData["limit"].(int); ok {
		limit = limitInt
	}

	// 获取备份目录
	backupDir := filepath.Join(filepath.Dir(os.Args[0]), "backup")

	// 检查备份目录是否存在
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
			"success": true,
			"message": "Backup directory does not exist",
			"list":    []interface{}{},
		})
		return
	}

	// 查找该服务器的所有备份文件
	pattern := fmt.Sprintf("backup_%d_*.zip", serverID)
	matches, err := filepath.Glob(filepath.Join(backupDir, pattern))
	if err != nil {
		c.logger.Error("Failed to find backup files: %v", err)
		c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
			"success": false,
			"message": fmt.Sprintf("Failed to find backup files: %v", err),
			"list":    []interface{}{},
		})
		return
	}

	// 按修改时间排序（最新的在前）
	sort.Slice(matches, func(i, j int) bool {
		info1, err1 := os.Stat(matches[i])
		info2, err2 := os.Stat(matches[j])
		if err1 != nil || err2 != nil {
			return false
		}
		return info1.ModTime().After(info2.ModTime())
	})

	// 限制返回数量
	if len(matches) > limit {
		matches = matches[:limit]
	}

	// 构建备份列表
	var backupList []interface{}
	for _, filePath := range matches {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			c.logger.Warn("Failed to get file info for %s: %v", filePath, err)
			continue
		}

		// 从文件名提取备份ID和时间戳
		fileName := filepath.Base(filePath)
		// 文件名格式：backup_{serverID}_{timestamp}.zip
		// 备份ID格式：backup_{serverID}_{timestamp}
		backupID := strings.TrimSuffix(fileName, ".zip")

		// 尝试从文件名解析时间戳
		var createdAt time.Time
		parts := strings.Split(backupID, "_")
		if len(parts) >= 3 {
			// 时间戳格式：20060102_150405
			timestampStr := strings.Join(parts[2:], "_")
			if t, err := time.Parse("20060102_150405", timestampStr); err == nil {
				createdAt = t
			} else {
				// 如果解析失败，使用文件修改时间
				createdAt = fileInfo.ModTime()
			}
		} else {
			// 如果无法解析，使用文件修改时间
			createdAt = fileInfo.ModTime()
		}

		// 构建备份信息
		backupInfo := map[string]interface{}{
			"backup_id":               backupID,
			"server_id":               serverID,
			"backup_type":             "full", // 默认为全量备份
			"backup_size_bytes":       fileInfo.Size(),
			"backup_duration_seconds": 0,         // 文件系统无法获取，设为0
			"backup_status":           "success", // 文件存在即认为成功
			"backup_path":             filePath,
			"file_count":              0,   // 文件系统无法获取，设为0
			"compression_ratio":       0.0, // 文件系统无法获取，设为0
			"error_message":           "",

			// 性能监控数据（文件系统无法获取，设为0）
			"cpu_usage":         0.0,
			"memory_usage":      0.0,
			"disk_usage":        0.0,
			"network_in_bytes":  int64(0),
			"network_out_bytes": int64(0),
			"disk_read_speed":   0.0,
			"disk_write_speed":  0.0,
			"process_count":     0,
			"load_average":      []float64{},

			// 系统资源信息（文件系统无法获取，设为0）
			"cpu_cores":              0,
			"total_memory_bytes":     int64(0),
			"available_memory_bytes": int64(0),
			"total_disk_space_bytes": int64(0),
			"free_disk_space_bytes":  int64(0),

			// 备份性能指标（文件系统无法获取，设为0）
			"files_per_second":         0.0,
			"data_throughput":          0.0,
			"compression_time_seconds": 0,
			"encryption_time_seconds":  0,

			// 创建时间
			"created_at": createdAt.Format(time.RFC3339),
		}

		backupList = append(backupList, backupInfo)
	}

	// 发送响应
	c.sendBackupResponse(MsgTypeBackupList, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Found %d backup files", len(backupList)),
		"list":    backupList,
	})
}

// handleBackupDelete 处理删除备份请求
func (c *Client) handleBackupDelete(data interface{}) {
	// 这里可以实现删除备份的逻辑
	c.sendBackupResponse(MsgTypeBackupStatus, map[string]interface{}{
		"success": true,
		"message": "Backup delete request received",
	})
}

// executeBackup 执行备份操作
func (c *Client) executeBackup(serverID uint, backupPath, description string) {
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
			if err = os.Remove(file); err != nil {
				c.logger.Warn("Failed to delete old backup file %s: %v", file, err)
			}
		}
	}
}

// sendBackupResponse 发送备份响应
func (c *Client) sendBackupResponse(msgType string, data interface{}) {
	if !c.wsClient.IsConnected() {
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
	cfg, err := c.getServerConfig()
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
	} else {
		// CMD 服务器：备份路径是根目录
		backupPath = installPath
	}

	return backupPath
}

// getServerConfig 获取服务器配置信息
func (c *Client) getServerConfig() (*config.Config, error) {
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
}

// handleFileDownload 处理文件下载请求
func (c *Client) handleFileDownload(data interface{}) {

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
}

// handleFileDelete 处理文件删除请求
func (c *Client) handleFileDelete(data interface{}) {

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
}

// handleCloudUpload 处理云存储上传请求
func (c *Client) handleCloudUpload(data interface{}) {

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

	// 检测云存储提供商
	provider := c.detectCloudProvider(uploadSignature)
	if provider == "" {
		return fmt.Errorf("unable to detect cloud storage provider from upload signature")
	}

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

	return nil
}

// handleCloudDownload 处理云存储下载请求
func (c *Client) handleCloudDownload(data interface{}) {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		c.logger.Error("Invalid cloud download request data")
		c.sendResponse(MsgTypeCloudDownload, nil, "Invalid request data")
		return
	}

	targetPath, _ := dataMap["target_path"].(string)
	downloadURL, _ := dataMap["download_url"].(string)
	cloudPath, _ := dataMap["cloud_path"].(string)

	if targetPath == "" {
		c.logger.Error("Target path is required")
		c.sendResponse(MsgTypeCloudDownload, nil, "Target path is required")
		return
	}

	if downloadURL == "" {
		c.logger.Error("Download URL is required")
		c.sendResponse(MsgTypeCloudDownload, nil, "Download URL is required")
		return
	}

	// 构建完整文件路径
	var fullPath string
	if strings.HasPrefix(targetPath, "/") {
		// 绝对路径，将其视为相对于steamDir的路径
		relativePath := strings.TrimPrefix(targetPath, "/")
		fullPath = filepath.Join(c.steamDir, relativePath)
	} else {
		// 相对路径，基于Steam目录
		fullPath = filepath.Join(c.steamDir, targetPath)
	}

	// 验证最终路径是否在允许的目录内
	cleanFullPath := filepath.Clean(fullPath)
	cleanSteamDir := filepath.Clean(c.steamDir)
	if !strings.HasPrefix(cleanFullPath, cleanSteamDir) {
		c.logger.Error("Access denied: path outside Steam directory: %s (resolved to %s, steamDir: %s)", targetPath, cleanFullPath, cleanSteamDir)
		c.sendResponse(MsgTypeCloudDownload, nil, "Access denied: path outside allowed directory")
		return
	}

	// 确保目标目录存在
	targetDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		c.logger.Error("Failed to create target directory %s: %v", targetDir, err)
		c.sendResponse(MsgTypeCloudDownload, nil, fmt.Sprintf("Failed to create target directory: %v", err))
		return
	}

	c.logger.Info("开始从云存储下载文件: %s -> %s", cloudPath, fullPath)

	// 下载文件
	err := c.downloadFileFromURL(downloadURL, fullPath)
	if err != nil {
		c.logger.Error("Failed to download file from cloud: %v", err)
		c.sendResponse(MsgTypeCloudDownload, map[string]interface{}{
			"target_path": targetPath,
			"cloud_path":  cloudPath,
		}, fmt.Sprintf("Failed to download file from cloud: %v", err))
		return
	}

	c.logger.Info("云存储文件下载完成: %s", fullPath)
	c.sendResponse(MsgTypeCloudDownload, map[string]interface{}{
		"target_path": targetPath,
		"cloud_path":  cloudPath,
		"file_path":   fullPath,
	}, "")
}

// downloadFileFromURL 从URL下载文件到指定路径
func (c *Client) downloadFileFromURL(url, filepath string) error {
	// 创建 HTTP 请求
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute) // 10分钟超时
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置 User-Agent
	req.Header.Set("User-Agent", "SCUM-Run-Client/1.0")

	// 发送请求
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，HTTP状态码: %d", resp.StatusCode)
	}

	// 创建目标文件
	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer out.Close()

	// 获取文件大小用于显示进度
	contentLength := resp.ContentLength

	// 创建进度报告器
	progressReader := &progressReader{
		Reader:        resp.Body,
		contentLength: contentLength,
		logger:        c.logger,
	}

	// 复制文件内容
	_, err = io.Copy(out, progressReader)
	if err != nil {
		return fmt.Errorf("下载文件内容失败: %w", err)
	}

	c.logger.Info("文件下载完成: %s", filepath)
	return nil
}

// progressReader 实现下载进度显示
type progressReader struct {
	io.Reader
	contentLength int64
	bytesRead     int64
	logger        *logger.Logger
	lastReport    time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.bytesRead += int64(n)

	// 每5秒报告一次进度
	now := time.Now()
	if now.Sub(pr.lastReport) >= 5*time.Second {
		if pr.contentLength > 0 {
			percentage := float64(pr.bytesRead) / float64(pr.contentLength) * 100
			pr.logger.Info("下载进度: %.1f%% (%d/%d 字节)", percentage, pr.bytesRead, pr.contentLength)
		} else {
			pr.logger.Info("已下载: %d 字节", pr.bytesRead)
		}
		pr.lastReport = now
	}

	return n, err
}

// checkSteamUpdate checks if SCUM server update is available using SteamCmd
func (c *Client) checkSteamUpdate(steamCmdPath string) (bool, error) {
	// 获取安装路径
	installPath := c.config.AutoInstall.InstallPath
	if installPath == "" {
		installPath = _const.DefaultInstallPath
	}

	// 构建SteamCmd命令来检查更新
	args := []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+app_info_update", "1",
		"+app_info_print", _const.SCUMServerAppID,
		"+quit",
	}

	c.logger.Info("Checking for updates with SteamCmd: %s %v", steamCmdPath, args)

	// 执行SteamCmd命令
	cmd := exec.Command(steamCmdPath, args...)
	steamCmdDir := filepath.Dir(steamCmdPath)
	cmd.Dir = steamCmdDir

	// 捕获输出
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 执行命令
	err := cmd.Run()
	if err != nil {
		c.logger.Error("SteamCmd update check failed: %v, stderr: %s", err, stderr.String())
		return false, fmt.Errorf("SteamCmd execution failed: %w", err)
	}

	// 分析输出以确定是否有更新
	output := stdout.String()
	c.logger.Debug("SteamCmd output: %s", output)

	// 检查输出中是否包含更新信息
	// SteamCmd会在有更新时输出特定的信息
	// 这里我们使用一个简单的方法：检查是否包含"update"相关的关键词
	hasUpdate := strings.Contains(strings.ToLower(output), "update") &&
		!strings.Contains(strings.ToLower(output), "no update")

	if hasUpdate {
		c.logger.Info("SteamCmd detected available update")
	} else {
		c.logger.Info("SteamCmd reports no update available")
	}

	return hasUpdate, nil
}

// selfBuiltServerDataPusher
// @description: 自建服务器数据推送定时器（每3秒推送一次在线用户、载具、队伍领地数据）
func (c *Client) selfBuiltServerDataPusher() {
	c.logger.Info("Self-built server data pusher started")
	defer c.wg.Done()
	c.wg.Add(1)

	for {
		select {
		case <-c.ctx.Done():
			c.logger.Info("Self-built server data pusher stopped")
			return
		case <-c.dataPushTicker.C:
			// 只有当服务器正在运行时才推送数据
			if !c.process.IsRunning() {
				continue
			}

			// 检查WebSocket连接是否正常
			if !c.wsClient.IsConnected() {
				continue
			}

			// 查询数据库获取所需数据
			usersData, vehiclesData, flagsData, groupsData := c.queryServerData()

			// 如果没有数据则跳过
			if usersData == "" && vehiclesData == "" && flagsData == "" && groupsData == "" {
				continue
			}

			// 构建推送消息
			pushMsg := request.WebSocketMessage{
				Type: MsgTypeSelfBuiltServerData,
				Data: map[string]interface{}{
					"server_id": c.serverID,
					"users":     usersData,
					"vehicles":  vehiclesData,
					"flags":     flagsData,
					"groups":    groupsData,
				},
			}

			// 发送消息
			if err := c.wsClient.SendMessage(pushMsg); err != nil {
				c.logger.Error("Failed to push self-built server data: %v", err)
			} else {
				c.logger.Debug("Successfully pushed self-built server data")
			}
		}
	}
}

// queryServerData
// @description: 查询服务器数据（用户列表、载具列表、队伍领地列表）
// @return: usersData, vehiclesData, flagsData, groupsData string
func (c *Client) queryServerData() (string, string, string, string) {
	var usersData, vehiclesData, flagsData, groupsData string

	// 查询在线玩家列表 - 使用SQL查询
	usersData = c.queryUsersData()

	// 查询载具列表 - 使用SQL查询
	vehiclesData = c.queryVehiclesData()

	// 查询队伍领地列表 - 使用SQL查询
	flagsData = c.queryFlagsData()

	// 查询所有队伍列表 - 使用SQL查询
	groupsData = c.queryGroupsData()

	return usersData, vehiclesData, flagsData, groupsData
}

// queryUsersData
// @description: 查询在线玩家列表数据并格式化为scum_robot期望的格式
// @return: string 格式化的玩家数据
func (c *Client) queryUsersData() string {
	// 计算查询时间窗口：当前时间减去时间窗口
	logTime := time.Now().Unix() - _const.OnlinePlayerTimeWindow

	// SQL查询：获取在线玩家信息（包括位置、声望、余额等）
	sqlQuery := `SELECT 
		up.id AS user_profile_id,
		up.name AS fake_name,
		COALESCE(up.fame_points, 0) AS fame_points,
		COALESCE(e.location_x, 0.0) AS location_x,
		COALESCE(e.location_y, 0.0) AS location_y,
		COALESCE(e.location_z, 0.0) AS location_z,
		COALESCE(SUM(CASE WHEN barc.currency_type = ? THEN barc.account_balance ELSE 0 END), 0) AS money_balance,
		COALESCE(SUM(CASE WHEN barc.currency_type = ? THEN barc.account_balance ELSE 0 END), 0) AS gold_balance
	FROM 
		user_profile up
	LEFT JOIN 
		prisoner p ON p.user_profile_id = up.id
	LEFT JOIN 
		prisoner_entity pe ON pe.prisoner_id = p.id
	LEFT JOIN 
		entity e ON e.id = pe.entity_id
	LEFT JOIN 
		bank_account_registry bar ON bar.account_owner_user_profile_id = up.id
	LEFT JOIN 
		bank_account_registry_currencies barc ON barc.bank_account_id = bar.id
	WHERE 
		up.type != ?
		AND p.last_save_time > ?
	GROUP BY 
		up.id, up.name, up.fame_points, e.location_x, e.location_y, e.location_z`

	results, err := c.db.Query(sqlQuery, _const.CurrencyTypeMoney, _const.CurrencyTypeGold, _const.UserTypeServer, logTime)
	if err != nil {
		c.logger.Error("Failed to query users data: %v", err)
		return ""
	}

	if len(results) == 0 {
		return ""
	}

	// 格式化为 scum_robot 期望的格式
	// 格式: Steam: (name) (steam_id) Fame: (fame) Account balance: (account) Gold balance: (gold) Location: X=(x) Y=(y) Z=(z)
	var builder strings.Builder
	for _, row := range results {
		userProfileID, _ := getInt64Value(row["user_profile_id"])
		fakeName, _ := row["fake_name"].(string)
		famePoints, _ := getFloat64Value(row["fame_points"])
		locationX, _ := getFloat64Value(row["location_x"])
		locationY, _ := getFloat64Value(row["location_y"])
		locationZ, _ := getFloat64Value(row["location_z"])
		moneyBalance, _ := getFloat64Value(row["money_balance"])
		goldBalance, _ := getFloat64Value(row["gold_balance"])

		// 格式化输出
		// 格式: Steam: (name) (steam_id) Fame: (fame) Account balance: (account) Gold balance: (gold) Location: X=(x) Y=(y) Z=(z)
		fmt.Fprintf(&builder, "Steam: %s (%d) Fame: %.0f Account balance: %.0f Gold balance: %.0f Location: X=%.2f Y=%.2f Z=%.2f \n",
			fakeName, userProfileID, famePoints, moneyBalance, goldBalance, locationX, locationY, locationZ)
	}

	return builder.String()
}

// queryVehiclesData
// @description: 查询载具列表数据并格式化为scum_robot期望的格式
// @return: string 格式化的载具数据
func (c *Client) queryVehiclesData() string {
	// SQL查询：获取载具列表
	// 载具类型名称是 trade_goods 表的 name + '_ES'，比如 name=RIS，那么 entity.class=RIS_ES
	// vehicle_spawner 表的 vehicle_entity_id 就是 entity 表的 id
	sqlQuery := `SELECT 
		vs.vehicle_entity_id AS vehicle_id,
		e.class AS entity_class,
		COALESCE(vs.vehicle_alias, '') AS vehicle_alias,
		COALESCE(e.location_x, 0.0) AS location_x,
		COALESCE(e.location_y, 0.0) AS location_y,
		COALESCE(e.location_z, 0.0) AS location_z
	FROM 
		vehicle_spawner vs
	INNER JOIN 
		entity e ON e.id = vs.vehicle_entity_id
	WHERE 
		e.location_x IS NOT NULL 
		AND e.location_y IS NOT NULL 
		AND e.location_z IS NOT NULL
		AND e.class LIKE ?`

	results, err := c.db.Query(sqlQuery, "%"+_const.VehicleClassSuffix+"%")
	if err != nil {
		c.logger.Error("Failed to query vehicles data: %v", err)
		return ""
	}

	if len(results) == 0 {
		return ""
	}

	// 格式化为 scum_robot 期望的格式
	// 格式: #(id): (vehicle_name) YYYY-MM-DDTHH:MM:SS.XXX X=(x) Y=(y) Z=(z)
	var builder strings.Builder
	currentTime := time.Now()
	timeStr := currentTime.Format(_const.VehicleTimeFormat)

	for _, row := range results {
		vehicleID, _ := getInt64Value(row["vehicle_id"])
		entityClass, _ := row["entity_class"].(string)
		vehicleAlias, _ := row["vehicle_alias"].(string)
		locationX, _ := getFloat64Value(row["location_x"])
		locationY, _ := getFloat64Value(row["location_y"])
		locationZ, _ := getFloat64Value(row["location_z"])

		// 确定载具名称：优先使用别名，其次使用 trade_goods 映射，最后从 entity.class 提取
		vehicleName := c.getVehicleName(entityClass, vehicleAlias)

		// 格式化输出
		fmt.Fprintf(&builder, "#%d: %s %s X=%.2f Y=%.2f Z=%.2f\n",
			vehicleID, vehicleName, timeStr, locationX, locationY, locationZ)
	}

	return builder.String()
}

// getVehicleName 获取载具名称
// @description: 根据 entity.class 和 vehicle_alias 确定载具名称
// @param: entityClass string, vehicleAlias string
// @return: string 载具名称
func (c *Client) getVehicleName(entityClass, vehicleAlias string) string {
	// 优先使用别名
	if vehicleAlias != "" {
		return vehicleAlias
	}

	// 其次使用 trade_goods 映射
	if c.vehicleGoodsMap != nil {
		if mappedName, ok := c.vehicleGoodsMap[entityClass]; ok {
			return mappedName
		}
	}

	// 最后从 entity.class 中提取（去掉后缀）
	if strings.HasSuffix(entityClass, _const.VehicleClassSuffix) {
		return entityClass[:len(entityClass)-len(_const.VehicleClassSuffix)]
	}

	return entityClass
}

// queryFlagsData
// @description: 查询队伍领地列表数据并格式化为scum_robot期望的格式
// @return: string 格式化的领地数据
func (c *Client) queryFlagsData() string {
	// SQL查询：获取队伍领地列表
	// base_element 表的 asset 字段包含 '%Flag%' 的是领地数据
	// 领地所有人是 owner_profile_id，对应 user_profile 表的 id
	// 该领地属于哪个队伍：通过队长的 user_profile 的 id 查询 squad_member.user_profile_id 且 rank=4，队伍id是 squad_id 对应 squad.id
	sqlQuery := `SELECT 
		be.element_id AS flag_id,
		be.owner_profile_id AS owner_profile_id,
		COALESCE(up.user_id, '') AS owner_steam_id,
		COALESCE(up.name, up.fake_name, '') AS owner_name,
		COALESCE(be.location_x, 0.0) AS location_x,
		COALESCE(be.location_y, 0.0) AS location_y,
		COALESCE(be.location_z, 0.0) AS location_z,
		COALESCE(s.id, 0) AS squad_id
	FROM 
		base_element be
	LEFT JOIN 
		user_profile up ON up.id = be.owner_profile_id
	LEFT JOIN 
		squad_member sm ON sm.user_profile_id = be.owner_profile_id AND sm.rank = ?
	LEFT JOIN 
		squad s ON s.id = sm.squad_id
	WHERE 
		be.asset LIKE ?`

	results, err := c.db.Query(sqlQuery, _const.SquadLeaderRank, _const.FlagAssetPattern)
	if err != nil {
		c.logger.Error("Failed to query flags data: %v", err)
		return ""
	}

	if len(results) == 0 {
		return ""
	}

	// 格式化为 scum_robot 期望的格式
	// 格式: Flag ID: (flag_id) | Owner: [(owner_id)] ... (name) (...) | Location: X=(x) Y=(y) Z=(z)
	var builder strings.Builder
	for _, row := range results {
		flagID, _ := getInt64Value(row["flag_id"])
		ownerID, _ := getInt64Value(row["owner_profile_id"])
		ownerSteamID, _ := row["owner_steam_id"].(string)
		ownerName, _ := row["owner_name"].(string)
		locationX, _ := getFloat64Value(row["location_x"])
		locationY, _ := getFloat64Value(row["location_y"])
		locationZ, _ := getFloat64Value(row["location_z"])

		// 格式化输出
		fmt.Fprintf(&builder, "Flag ID: %d | Owner: [%d] %s (%s) | Location: X=%.2f Y=%.2f Z=%.2f\n",
			flagID, ownerID, ownerName, ownerSteamID, locationX, locationY, locationZ)
	}

	return builder.String()
}

// queryGroupsData
// @description: 查询所有队伍列表数据并格式化为scum_robot期望的格式
// @return: string 格式化的队伍数据
func (c *Client) queryGroupsData() string {
	// SQL查询：获取所有队伍列表
	// 从 squad 表查询所有队伍，关联 squad_member 和 user_profile 获取成员信息
	sqlQuery := `SELECT 
		s.id AS squad_id,
		s.name AS squad_name,
		sm.user_profile_id AS member_user_profile_id,
		COALESCE(up.user_id, '') AS member_steam_id,
		COALESCE(up.name, '') AS member_steam_name,
		COALESCE(up.fake_name, '') AS member_character_name,
		COALESCE(sm.rank, 0) AS member_rank
	FROM 
		squad s
	LEFT JOIN 
		squad_member sm ON sm.squad_id = s.id
	LEFT JOIN 
		user_profile up ON up.id = sm.user_profile_id
	ORDER BY 
		s.id, sm.rank DESC, sm.id`

	results, err := c.db.Query(sqlQuery)
	if err != nil {
		c.logger.Error("Failed to query groups data: %v", err)
		return ""
	}

	if len(results) == 0 {
		return ""
	}

	// 格式化为 scum_robot 期望的格式
	// 格式: [SquadId: (id) SquadName: (name)]
	//       SteamId: (steam_id) SteamName: (steam_name) CharacterName: (char_name) MemberRank: (rank)
	//       ...
	//
	var builder strings.Builder
	var currentSquadID int64 = -1
	var currentSquadName string
	var memberList strings.Builder

	for _, row := range results {
		squadID, _ := getInt64Value(row["squad_id"])
		squadName, _ := row["squad_name"].(string)
		memberUserProfileID, _ := getInt64Value(row["member_user_profile_id"])
		memberSteamID, _ := row["member_steam_id"].(string)
		memberSteamName, _ := row["member_steam_name"].(string)
		memberCharacterName, _ := row["member_character_name"].(string)
		memberRank, _ := getInt64Value(row["member_rank"])

		// 如果切换到新的队伍，先输出上一个队伍的信息
		if currentSquadID != -1 && currentSquadID != squadID {
			fmt.Fprintf(&builder, "[SquadId: %d SquadName: %s]\n%s\n\n",
				currentSquadID, currentSquadName, memberList.String())
			memberList.Reset()
		}

		// 如果是新队伍，记录队伍ID和名称
		if currentSquadID != squadID {
			currentSquadID = squadID
			currentSquadName = squadName
		}

		// 如果有成员信息，添加到成员列表
		if memberUserProfileID > 0 {
			fmt.Fprintf(&memberList, "SteamId: %s SteamName: %s CharacterName: %s MemberRank: %d\n",
				memberSteamID, memberSteamName, memberCharacterName, memberRank)
		}
	}

	// 输出最后一个队伍的信息
	if currentSquadID != -1 {
		fmt.Fprintf(&builder, "[SquadId: %d SquadName: %s]\n%s\n\n",
			currentSquadID, currentSquadName, memberList.String())
	}

	return builder.String()
}

// getInt64Value 从interface{}中提取int64值
func getInt64Value(val interface{}) (int64, error) {
	if val == nil {
		return 0, nil
	}
	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		var i int64
		_, err := fmt.Sscanf(v, "%d", &i)
		return i, err
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", val)
	}
}

// getFloat64Value 从interface{}中提取float64值
func getFloat64Value(val interface{}) (float64, error) {
	if val == nil {
		return 0, nil
	}
	switch v := val.(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	case string:
		var f float64
		_, err := fmt.Sscanf(v, "%f", &f)
		return f, err
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", val)
	}
}
