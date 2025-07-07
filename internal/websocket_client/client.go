package websocket_client

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"scum_run/internal/logger"
)

// Client represents a WebSocket client
type Client struct {
	url    string
	conn   *websocket.Conn
	logger *logger.Logger
	mutex  sync.Mutex
}

// New creates a new WebSocket client
func New(url string, logger *logger.Logger) *Client {
	return &Client{
		url:    url,
		logger: logger,
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
	c.logger.Info("Connected to WebSocket server: %s", c.url)
	return nil
}

// Close closes the WebSocket connection
func (c *Client) Close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// SendMessage sends a message via WebSocket
func (c *Client) SendMessage(message interface{}) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.conn == nil {
		return websocket.ErrCloseSent
	}

	return c.conn.WriteJSON(message)
}

// ReadMessage reads a message from WebSocket
func (c *Client) ReadMessage(message interface{}) error {
	if c.conn == nil {
		return websocket.ErrCloseSent
	}

	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return err
	}

	return json.Unmarshal(data, message)
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.conn != nil
} 