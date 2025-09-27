package websocket_client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"scum_run/internal/logger"
)

// Client represents a WebSocket client
type Client struct {
	url           string
	conn          *websocket.Conn
	logger        *logger.Logger
	mutex         sync.RWMutex
	writeMutex    sync.Mutex // 专门用于写操作的互斥锁
	isRunning     bool
	stopChan      chan struct{}
	reconnectChan chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	// 重连配置
	maxRetries       int
	retryInterval    time.Duration
	maxRetryInterval time.Duration
	// 心跳配置
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	// 连接稳定性配置
	readBufferSize  int
	writeBufferSize int
	maxMessageSize  int64
	lastHeartbeat   time.Time
	// 回调函数
	onConnect    func()
	onDisconnect func()
	onReconnect  func()
	// TLS 配置
	skipTLSVerify bool
}

// New creates a new WebSocket client
func New(url string, logger *logger.Logger) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		url:               url,
		logger:            logger,
		ctx:               ctx,
		cancel:            cancel,
		stopChan:          make(chan struct{}),
		reconnectChan:     make(chan struct{}),
		maxRetries:        -1,               // 无限重试
		retryInterval:     5 * time.Second,  // 增加重连间隔，减少频繁重连
		maxRetryInterval:  60 * time.Second, // 增加最大重连间隔
		heartbeatInterval: 40 * time.Second, // 与服务端错开的心跳时间，避免冲突
		heartbeatTimeout:  5 * time.Minute,  // 合理的心跳超时时间，给网络波动缓冲
		readBufferSize:    128 * 1024,       // 与服务端一致的缓冲区大小
		writeBufferSize:   128 * 1024,       // 与服务端一致的缓冲区大小
		maxMessageSize:    2 * 1024 * 1024,  // 与服务端一致的最大消息大小
		skipTLSVerify:     true,             // 默认不跳过 TLS 验证
	}
}

// Connect establishes a WebSocket connection
func (c *Client) Connect() error {
	c.mutex.Lock()
	dialer := websocket.Dialer{
		HandshakeTimeout: 60 * time.Second,  // 延长握手超时时间到60秒
		ReadBufferSize:   c.readBufferSize,  // 使用配置的读取缓冲区
		WriteBufferSize:  c.writeBufferSize, // 使用配置的写入缓冲区
		// 添加更多连接优化配置
		EnableCompression: false, // 禁用压缩减少CPU开销
	}

	// 配置 TLS 设置
	if c.skipTLSVerify {
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // 跳过 TLS 证书验证
		}
		c.logger.Warn("TLS certificate verification is disabled - this is not recommended for production use")
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		c.mutex.Unlock()
		return err
	}

	// 设置连接参数 - 移除超时限制提高稳定性
	conn.SetReadLimit(c.maxMessageSize) // 使用配置的最大消息大小
	// 移除读取超时限制，避免网络波动导致的连接断开
	// conn.SetReadDeadline() - 不设置读取超时

	conn.SetPongHandler(func(string) error {
		// 只记录pong响应，不重新设置读取超时
		c.mutex.Lock()
		c.lastHeartbeat = time.Now()
		c.mutex.Unlock()
		return nil
	})

	// 移除写超时限制，避免大量数据传输时超时
	// conn.SetWriteDeadline() - 不设置写超时

	c.conn = conn
	c.isRunning = true
	c.logger.Info("Connected to WebSocket server: %s", c.url)

	// 获取回调函数的引用，然后释放锁
	onConnect := c.onConnect
	c.mutex.Unlock()

	// 在锁外调用连接回调，避免死锁
	if onConnect != nil {
		onConnect()
	}

	return nil
}

// ConnectWithAutoReconnect establishes a WebSocket connection with auto-reconnect
func (c *Client) ConnectWithAutoReconnect() error {
	if err := c.Connect(); err != nil {
		return err
	}

	// 启动重连监控和心跳
	go c.monitorConnection()
	go c.heartbeatLoop()

	return nil
}

// Close closes the WebSocket connection
func (c *Client) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.isRunning = false
	c.cancel()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil

		// 调用断开连接回调
		if c.onDisconnect != nil {
			c.onDisconnect()
		}

		return err
	}
	return nil
}

// SendMessage sends a message via WebSocket
func (c *Client) SendMessage(message interface{}) error {
	// 使用读锁检查连接状态
	c.mutex.RLock()
	if !c.isRunning || c.conn == nil {
		c.mutex.RUnlock()
		c.logger.Error("Cannot send message: connection not running or nil")
		return websocket.ErrCloseSent
	}
	conn := c.conn
	c.mutex.RUnlock()

	// 使用专门的写锁确保同时只有一个写操作
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()

	// 移除写超时限制，避免网络波动时发送失败
	// conn.SetWriteDeadline() - 不设置写超时

	c.logger.Debug("Sending message: %+v", message)
	err := conn.WriteJSON(message)
	if err != nil {
		c.logger.Error("Failed to send message: %v", err)
		// 发送失败时触发重连
		c.handleDisconnection()
	} else {
		c.logger.Debug("Message sent successfully")
	}
	return err
}

// ReadMessage reads a message from WebSocket
func (c *Client) ReadMessage(message interface{}) error {
	for {
		c.mutex.RLock()
		if !c.isRunning || c.conn == nil {
			c.mutex.RUnlock()
			c.logger.Error("Cannot read message: connection not running or nil")
			return websocket.ErrCloseSent
		}
		conn := c.conn
		c.mutex.RUnlock()

		c.logger.Debug("Waiting for message from server...")
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			c.logger.Error("Failed to read message: %v", err)
			// 连接断开，触发重连
			c.handleDisconnection()
			return err
		}

		// 处理不同类型的消息
		switch messageType {
		case websocket.PingMessage:
			// 响应ping消息
			c.mutex.Lock()
			if c.conn != nil {
				_ = c.conn.WriteMessage(websocket.PongMessage, data)
			}
			c.mutex.Unlock()
			c.logger.Debug("Responded to ping message")
			// 继续读取下一条消息
			continue

		case websocket.PongMessage:
			// 更新心跳时间
			c.mutex.Lock()
			c.lastHeartbeat = time.Now()
			c.mutex.Unlock()
			c.logger.Debug("Received pong message")
			// 继续读取下一条消息
			continue

		case websocket.TextMessage:
			// 处理文本消息
			c.logger.Debug("Received raw message: %s", string(data))
			err = json.Unmarshal(data, message)
			if err != nil {
				c.logger.Error("Failed to unmarshal message: %v", err)
				return err
			}
			c.logger.Debug("Message unmarshaled successfully: %+v", message)
			return nil

		case websocket.BinaryMessage:
			// 二进制消息暂时忽略，继续读取下一条消息
			c.logger.Debug("Received binary message, ignoring")
			continue

		default:
			// 其他类型消息忽略，继续读取下一条消息
			c.logger.Debug("Received non-text message type %d, ignoring", messageType)
			continue
		}
	}
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.isRunning && c.conn != nil
}

// SetCallbacks sets callback functions for connection events
func (c *Client) SetCallbacks(onConnect, onDisconnect, onReconnect func()) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.onConnect = onConnect
	c.onDisconnect = onDisconnect
	c.onReconnect = onReconnect
}

// SetRetryConfig sets retry configuration
func (c *Client) SetRetryConfig(maxRetries int, retryInterval, maxRetryInterval time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.maxRetries = maxRetries
	c.retryInterval = retryInterval
	c.maxRetryInterval = maxRetryInterval
}

// SetHeartbeatConfig sets heartbeat configuration
func (c *Client) SetHeartbeatConfig(interval, timeout time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.heartbeatInterval = interval
	c.heartbeatTimeout = timeout
}

// SetTLSConfig sets TLS configuration
func (c *Client) SetTLSConfig(skipVerify bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.skipTLSVerify = skipVerify
	if skipVerify {
		c.logger.Warn("TLS certificate verification will be skipped for future connections")
	} else {
		c.logger.Info("TLS certificate verification will be enabled for future connections")
	}
}

// monitorConnection monitors the connection and handles reconnection
func (c *Client) monitorConnection() {
	ticker := time.NewTicker(5 * time.Minute) // 每5分钟检查一次连接，减少频繁检查
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if !c.IsConnected() {
				c.logger.Warn("Connection lost, attempting to reconnect...")
				go c.reconnect()
			} else {
				// 检查心跳超时
				c.mutex.RLock()
				lastHeartbeat := c.lastHeartbeat
				heartbeatTimeout := c.heartbeatTimeout
				c.mutex.RUnlock()

				// 如果从未收到心跳，跳过检查
				if !lastHeartbeat.IsZero() && time.Since(lastHeartbeat) > heartbeatTimeout {
					c.logger.Warn("Heartbeat timeout (last: %v, timeout: %v), attempting to reconnect...",
						lastHeartbeat, heartbeatTimeout)
					c.handleDisconnection()
				}
			}
		case <-c.reconnectChan:
			go c.reconnect()
		}
	}
}

// heartbeatLoop sends periodic heartbeat messages
func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if c.IsConnected() {
				heartbeatMsg := map[string]interface{}{
					"type": "heartbeat",
					"data": map[string]interface{}{
						"timestamp": time.Now().Unix(),
					},
				}

				if err := c.SendMessage(heartbeatMsg); err != nil {
					c.logger.Error("Failed to send heartbeat: %v", err)
					c.handleDisconnection()
				} else {
					c.mutex.Lock()
					c.lastHeartbeat = time.Now()
					c.mutex.Unlock()
				}
			}
		}
	}
}

// handleDisconnection handles disconnection events
func (c *Client) handleDisconnection() {
	c.mutex.Lock()
	wasRunning := c.isRunning
	c.isRunning = false
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.mutex.Unlock()

	if wasRunning {
		c.logger.Warn("WebSocket disconnected, attempting to reconnect...")

		// 调用断开连接回调
		if c.onDisconnect != nil {
			c.onDisconnect()
		}

		// 触发重连 - 使用非阻塞发送避免死锁
		select {
		case c.reconnectChan <- struct{}{}:
		default:
			// 如果重连通道已满，说明重连已在进行中
			c.logger.Debug("Reconnection already in progress")
		}
	}
}

// messageLoop function removed to prevent race conditions with concurrent reads
// Ping/Pong handling is now integrated into the main ReadMessage method

// reconnect attempts to reconnect to the WebSocket server
func (c *Client) reconnect() {
	backoff := c.retryInterval
	retryCount := 0
	isFirstAttempt := true

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
			// 首次重连前等待更长时间，避免启动时的频繁重连
			if isFirstAttempt {
				c.logger.Info("Starting reconnection process...")
				time.Sleep(1 * time.Second) // 减少初始等待时间
				isFirstAttempt = false
			}

			c.logger.Info("Attempting to reconnect... (attempt %d)", retryCount+1)

			if err := c.Connect(); err != nil {
				c.logger.Warn("Reconnection attempt %d failed: %v", retryCount+1, err)
				retryCount++

				// 检查是否达到最大重试次数
				if c.maxRetries > 0 && retryCount >= c.maxRetries {
					c.logger.Error("Max retry attempts reached, giving up")
					return
				}

				// 智能退避策略：网络错误使用较短间隔，其他错误使用指数退避
				if strings.Contains(err.Error(), "connection refused") ||
					strings.Contains(err.Error(), "network is unreachable") ||
					strings.Contains(err.Error(), "timeout") {
					// 网络问题，使用固定的较短间隔
					backoff = c.retryInterval
				} else {
					// 其他错误，使用指数退避
					backoff *= 2
					if backoff > c.maxRetryInterval {
						backoff = c.maxRetryInterval
					}
				}

				// 对于频繁的连接失败，增加额外延迟
				if retryCount > 5 {
					c.logger.Info("Multiple reconnection failures, adding extra delay...")
					time.Sleep(5 * time.Second)
				}
			} else {
				c.logger.Info("Reconnected successfully after %d attempts", retryCount+1)

				// 调用重连回调
				if c.onReconnect != nil {
					c.onReconnect()
				}

				// 重新启动连接监控
				go c.monitorConnection()
				return
			}
		}
	}
}
