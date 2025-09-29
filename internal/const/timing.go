package _const

import "time"

// 时间相关常量
const (
	// 等待时间常量
	DefaultWaitTime = 2 * time.Second // 默认等待时间
	LongWaitTime    = 5 * time.Second // 长时间等待
	ShortWaitTime   = 1 * time.Second // 短时间等待
	CleanupWaitTime = 5 * time.Second // 清理等待时间

	// 重试相关常量（避免与 update.go 中的 UpdateRetryCount 重复）
	ClientRetryCount = 5  // 客户端重试次数
	MaxRetryCount    = 10 // 最大重试次数

	// 连接相关常量
	ConnectionTimeout = 60 * time.Second // 连接超时时间
	HeartbeatInterval = 40 * time.Second // 心跳间隔
	HeartbeatTimeout  = 5 * time.Minute  // 心跳超时
	RetryInterval     = 5 * time.Second  // 重试间隔
	MaxRetryInterval  = 60 * time.Second // 最大重试间隔

	// 缓冲区大小常量
	ReadBufferSize  = 128 * 1024      // 读取缓冲区大小
	WriteBufferSize = 128 * 1024      // 写入缓冲区大小
	MaxMessageSize  = 2 * 1024 * 1024 // 最大消息大小
)
