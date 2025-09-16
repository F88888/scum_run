package websocket_client

import (
	"context"
	"encoding/json"
	_const "scum_run/internal/const"
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
	lastHeartbeat     time.Time
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
		maxRetries:        -1, // 无限重试
		retryInterval:     5 * time.Second,
		maxRetryInterval:  60 * time.Second,
		heartbeatInterval: 30 * time.Second,
		heartbeatTimeout:  60 * time.Second,
	}
}

// Connect establishes a WebSocket connection
func (c *Client) Connect() error {
	c.mutex.Lock()
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   _const.WebSocketReadBufferSize,  // 读取缓冲区
		WriteBufferSize:  _const.WebSocketWriteBufferSize, // 写入缓冲区
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		c.mutex.Unlock()
		return err
	}

	// 设置连接参数
	conn.SetReadLimit(_const.WebSocketMaxMessageSize) // 最大消息大小
	conn.SetReadDeadline(time.Now().Add(time.Duration(_const.HeartbeatTimeout) * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(time.Duration(_const.HeartbeatTimeout) * time.Second))
		return nil
	})

	// 设置写超时，避免写操作阻塞
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

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

	// 设置写超时
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

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
	_, data, err := conn.ReadMessage()
	if err != nil {
		c.logger.Error("Failed to read message: %v", err)
		// 连接断开，触发重连
		c.handleDisconnection()
		return err
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
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次连接
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

				if time.Since(lastHeartbeat) > heartbeatTimeout {
					c.logger.Warn("Heartbeat timeout, attempting to reconnect...")
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
	c.isRunning = false
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mutex.Unlock()

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

// reconnect attempts to reconnect to the WebSocket server
func (c *Client) reconnect() {
	backoff := c.retryInterval
	retryCount := 0

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
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

				// 重新启动连接监控
				go c.monitorConnection()
				return
			}
		}
	}
}
