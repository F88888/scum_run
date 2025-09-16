package websocket_client

import (
	"context"
	"encoding/json"
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
	defer c.mutex.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return err
	}

	c.conn = conn
	c.isRunning = true
	c.logger.Info("Connected to WebSocket server: %s", c.url)

	// 调用连接回调
	if c.onConnect != nil {
		c.onConnect()
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
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if !c.isRunning || c.conn == nil {
		return websocket.ErrCloseSent
	}

	return c.conn.WriteJSON(message)
}

// ReadMessage reads a message from WebSocket
func (c *Client) ReadMessage(message interface{}) error {
	c.mutex.RLock()
	if !c.isRunning || c.conn == nil {
		c.mutex.RUnlock()
		return websocket.ErrCloseSent
	}
	conn := c.conn
	c.mutex.RUnlock()

	_, data, err := conn.ReadMessage()
	if err != nil {
		// 连接断开，触发重连
		c.handleDisconnection()
		return err
	}

	return json.Unmarshal(data, message)
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
