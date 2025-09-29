package model

import "fmt"

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
