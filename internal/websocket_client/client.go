package websocket_client

import (
	"context"
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
		heartbeatInterval: 30 * time.Second, // 与服务端错开的心跳时间
		heartbeatTimeout:  30 * time.Minute, // 大幅延长心跳超时到30分钟，避免误判断开
		readBufferSize:    128 * 1024,       // 与服务端一致的缓冲区大小
		writeBufferSize:   128 * 1024,       // 与服务端一致的缓冲区大小
		maxMessageSize:    2 * 1024 * 1024,  // 与服务端一致的最大消息大小
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
	// 启动消息监听循环，确保能及时处理ping/pong消息
	go c.messageLoop()

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

	// 处理Pong消息
	if messageType == websocket.PongMessage {
		c.mutex.Lock()
		c.lastHeartbeat = time.Now()
		c.mutex.Unlock()
		c.logger.Debug("Received pong message")
		return nil
	}

	// 只处理文本消息
	if messageType != websocket.TextMessage {
		c.logger.Debug("Received non-text message, ignoring")
		return nil
	}

	c.logger.Debug("Received raw message: %s", string(data))
	err = json.Unmarshal(data, message)
	if err != nil {
		c.logger.Error("Failed to unmarshal message: %v", err)
		return err
	}

	c.logger.Debug("Message unmarshaled successfully: %+v", message)
	return nil
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

		// 触发重连
		select {
		case c.reconnectChan <- struct{}{}:
		default:
		}
	}
}

// messageLoop 持续监听WebSocket消息，确保能及时处理ping/pong消息
func (c *Client) messageLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			if !c.IsConnected() {
				// 连接断开时等待一段时间再检查
				time.Sleep(1 * time.Second)
				continue
			}

			c.mutex.RLock()
			conn := c.conn
			c.mutex.RUnlock()

			if conn == nil {
				time.Sleep(1 * time.Second)
				continue
			}

			// 设置较短的读取超时，避免阻塞过久
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			messageType, data, err := conn.ReadMessage()
			_ = conn.SetReadDeadline(time.Time{}) // 清除超时

			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					c.logger.Error("Unexpected WebSocket close in message loop: %v", err)
					c.handleDisconnection()
				} else if strings.Contains(err.Error(), "unexpected EOF") {
					// 客户端异常关闭，通常是网络问题
					c.logger.Warn("Connection closed abnormally (unexpected EOF): %v", err)
					c.handleDisconnection()
				} else if strings.Contains(err.Error(), "close 1006") {
					// WebSocket 1006错误码表示异常关闭
					c.logger.Warn("Connection closed with abnormal closure (1006): %v", err)
					c.handleDisconnection()
				} else if strings.Contains(err.Error(), "i/o timeout") {
					// 读取超时，继续循环
					c.logger.Debug("Read timeout in message loop, continuing...")
					continue
				} else {
					// 其他错误继续循环
					c.logger.Debug("Read error in message loop: %v", err)
					continue
				}
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

			case websocket.PongMessage:
				// 更新心跳时间
				c.mutex.Lock()
				c.lastHeartbeat = time.Now()
				c.mutex.Unlock()
				c.logger.Debug("Received pong message")

			case websocket.TextMessage:
				// 文本消息暂时忽略，因为业务逻辑在其他地方处理
				c.logger.Debug("Received text message in background loop: %s", string(data))

			case websocket.BinaryMessage:
				// 二进制消息暂时忽略
				c.logger.Debug("Received binary message in background loop")
			}
		}
	}
}

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
				c.logger.Info("Waiting before first reconnection attempt...")
				time.Sleep(2 * time.Second)
				isFirstAttempt = false
			}

			c.logger.Info("Attempting to reconnect... (attempt %d)", retryCount+1)

			if err := c.Connect(); err != nil {
				c.logger.Error("Reconnection failed: %v", err)
				retryCount++

				// 检查是否达到最大重试次数
				if c.maxRetries > 0 && retryCount >= c.maxRetries {
					c.logger.Error("Max retry attempts reached, giving up")
					return
				}

				// 指数退避
				backoff *= 2
				if backoff > c.maxRetryInterval {
					backoff = c.maxRetryInterval
				}
			} else {
				c.logger.Info("Reconnected successfully")

				// 调用重连回调
				if c.onReconnect != nil {
					c.onReconnect()
				}

				// 重新启动连接监控和消息循环
				go c.monitorConnection()
				go c.messageLoop()
				return
			}
		}
	}
}
