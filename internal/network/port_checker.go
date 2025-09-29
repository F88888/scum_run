package network

import (
	"fmt"
	"net"
	"time"

	_const "scum_run/internal/const"
	"scum_run/model"
)

// PortChecker 端口检查器
type PortChecker struct {
	timeout time.Duration
}

// NewPortChecker 创建新的端口检查器
func NewPortChecker(timeout time.Duration) *PortChecker {
	if timeout <= 0 {
		timeout = _const.DefaultWaitTime + _const.ShortWaitTime
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
func (pc *PortChecker) CheckPort(host string, port int) (*model.PortStatus, error) {
	address := fmt.Sprintf("%s:%d", host, port)

	// 尝试连接端口
	conn, err := net.DialTimeout("tcp", address, pc.timeout)
	if err != nil {
		// 连接失败，端口可能未被占用
		return &model.PortStatus{
			Host:  host,
			Port:  port,
			InUse: false,
			Error: err.Error(),
		}, nil
	}

	// 连接成功，端口被占用
	conn.Close()
	return &model.PortStatus{
		Host:  host,
		Port:  port,
		InUse: true,
		Error: "",
	}, nil
}
