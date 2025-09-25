package network

import (
	"fmt"
	"net"
	"time"
)

// PortChecker 端口检查器
type PortChecker struct {
	timeout time.Duration
}

// NewPortChecker 创建新的端口检查器
func NewPortChecker(timeout time.Duration) *PortChecker {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &PortChecker{
		timeout: timeout,
	}
}

// IsPortInUse 检查指定端口是否被占用
func (pc *PortChecker) IsPortInUse(host string, port int) (bool, error) {
	address := fmt.Sprintf("%s:%d", host, port)

	// 尝试连接端口
	conn, err := net.DialTimeout("tcp", address, pc.timeout)
	if err != nil {
		// 如果连接失败，可能是端口未被占用
		return false, nil
	}

	// 如果连接成功，说明端口被占用
	conn.Close()
	return true, nil
}

// CheckPort 检查端口状态并返回详细信息
func (pc *PortChecker) CheckPort(host string, port int) (*PortStatus, error) {
	address := fmt.Sprintf("%s:%d", host, port)

	// 尝试连接端口
	conn, err := net.DialTimeout("tcp", address, pc.timeout)
	if err != nil {
		// 连接失败，端口可能未被占用
		return &PortStatus{
			Host:  host,
			Port:  port,
			InUse: false,
			Error: err.Error(),
		}, nil
	}

	// 连接成功，端口被占用
	conn.Close()
	return &PortStatus{
		Host:  host,
		Port:  port,
		InUse: true,
		Error: "",
	}, nil
}

// PortStatus 端口状态信息
type PortStatus struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	InUse bool   `json:"in_use"`
	Error string `json:"error,omitempty"`
}

// IsAvailable 返回端口是否可用
func (ps *PortStatus) IsAvailable() bool {
	return !ps.InUse
}

// String 返回端口状态的字符串表示
func (ps *PortStatus) String() string {
	if ps.InUse {
		return fmt.Sprintf("Port %d on %s is in use", ps.Port, ps.Host)
	}
	if ps.Error != "" {
		return fmt.Sprintf("Port %d on %s is available (error: %s)", ps.Port, ps.Host, ps.Error)
	}
	return fmt.Sprintf("Port %d on %s is available", ps.Port, ps.Host)
}
