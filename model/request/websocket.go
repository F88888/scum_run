package request

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
	Type    string      `json:"type"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Success bool        `json:"success"`
}

// SystemMonitorData 系统监控数据结构
type SystemMonitorData struct {
	CPUUsage   float64 `json:"cpu_usage"`
	MemUsage   float64 `json:"mem_usage"`
	DiskUsage  float64 `json:"disk_usage"`
	NetIncome  float64 `json:"net_income"`  // KB/s
	NetOutcome float64 `json:"net_outcome"` // KB/s
	Timestamp  int64   `json:"timestamp"`
}
