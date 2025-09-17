package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"

	"scum_run/internal/logger"
	"scum_run/model/request"
)

// SystemMonitor 系统监控器
type SystemMonitor struct {
	logger      *logger.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	interval    time.Duration
	callback    SystemMonitorCallback
	lastNetIO   *net.IOCountersStat
	lastNetTime time.Time
	mutex       sync.RWMutex
}

// SystemMonitorCallback 系统监控数据回调函数
type SystemMonitorCallback func(data *request.SystemMonitorData)

// New 创建新的系统监控器
func New(logger *logger.Logger, interval time.Duration) *SystemMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &SystemMonitor{
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		interval: interval,
	}
}

// SetCallback 设置监控数据回调函数
func (sm *SystemMonitor) SetCallback(callback SystemMonitorCallback) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.callback = callback
}

// Start 启动系统监控
func (sm *SystemMonitor) Start() error {
	sm.logger.Info("Starting system monitor with interval: %v", sm.interval)

	// 初始化网络IO统计
	if err := sm.initNetworkStats(); err != nil {
		sm.logger.Warn("Failed to initialize network stats: %v", err)
	}

	sm.wg.Add(1)
	go sm.monitorLoop()

	return nil
}

// Stop 停止系统监控
func (sm *SystemMonitor) Stop() {
	sm.logger.Info("Stopping system monitor")
	sm.cancel()
	sm.wg.Wait()
}

// initNetworkStats 初始化网络统计
func (sm *SystemMonitor) initNetworkStats() error {
	netIO, err := net.IOCounters(false)
	if err != nil {
		return fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(netIO) > 0 {
		sm.mutex.Lock()
		sm.lastNetIO = &netIO[0]
		sm.lastNetTime = time.Now()
		sm.mutex.Unlock()
	}

	return nil
}

// monitorLoop 监控循环
func (sm *SystemMonitor) monitorLoop() {
	defer sm.wg.Done()

	ticker := time.NewTicker(sm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			data, err := sm.collectSystemData()
			if err != nil {
				sm.logger.Error("Failed to collect system data: %v", err)
				continue
			}

			sm.mutex.RLock()
			callback := sm.callback
			sm.mutex.RUnlock()

			if callback != nil {
				callback(data)
			}
		}
	}
}

// collectSystemData 收集系统数据
func (sm *SystemMonitor) collectSystemData() (*request.SystemMonitorData, error) {
	data := &request.SystemMonitorData{
		Timestamp: time.Now().Unix(),
	}

	// 收集CPU使用率
	if err := sm.collectCPUUsage(data); err != nil {
		sm.logger.Warn("Failed to collect CPU usage: %v", err)
	}

	// 收集内存使用率
	if err := sm.collectMemoryUsage(data); err != nil {
		sm.logger.Warn("Failed to collect memory usage: %v", err)
	}

	// 收集磁盘使用率
	if err := sm.collectDiskUsage(data); err != nil {
		sm.logger.Warn("Failed to collect disk usage: %v", err)
	}

	// 收集网络流量
	if err := sm.collectNetworkUsage(data); err != nil {
		sm.logger.Warn("Failed to collect network usage: %v", err)
	}

	return data, nil
}

// collectCPUUsage 收集CPU使用率
func (sm *SystemMonitor) collectCPUUsage(data *request.SystemMonitorData) error {
	percentages, err := cpu.Percent(time.Second, false)
	if err != nil {
		return fmt.Errorf("failed to get CPU percentage: %w", err)
	}

	if len(percentages) > 0 {
		data.CPUUsage = percentages[0]
	}

	return nil
}

// collectMemoryUsage 收集内存使用率
func (sm *SystemMonitor) collectMemoryUsage(data *request.SystemMonitorData) error {
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return fmt.Errorf("failed to get memory info: %w", err)
	}

	data.MemUsage = memInfo.UsedPercent
	return nil
}

// collectDiskUsage 收集磁盘使用率
func (sm *SystemMonitor) collectDiskUsage(data *request.SystemMonitorData) error {
	// 获取系统根目录的磁盘使用情况
	diskUsage, err := disk.Usage("/")
	if err != nil {
		// 在Windows上尝试获取C盘使用情况
		diskUsage, err = disk.Usage("C:")
		if err != nil {
			return fmt.Errorf("failed to get disk usage: %w", err)
		}
	}

	data.DiskUsage = diskUsage.UsedPercent
	return nil
}

// collectNetworkUsage 收集网络流量
func (sm *SystemMonitor) collectNetworkUsage(data *request.SystemMonitorData) error {
	netIO, err := net.IOCounters(false)
	if err != nil {
		return fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(netIO) == 0 {
		return fmt.Errorf("no network interfaces found")
	}

	currentNetIO := &netIO[0]
	currentTime := time.Now()

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if sm.lastNetIO != nil && !sm.lastNetTime.IsZero() {
		// 计算时间差（秒）
		timeDiff := currentTime.Sub(sm.lastNetTime).Seconds()
		if timeDiff > 0 {
			// 计算流量差（字节）
			bytesInDiff := float64(currentNetIO.BytesRecv - sm.lastNetIO.BytesRecv)
			bytesOutDiff := float64(currentNetIO.BytesSent - sm.lastNetIO.BytesSent)

			// 转换为KB/s
			data.NetIncome = (bytesInDiff / 1024) / timeDiff
			data.NetOutcome = (bytesOutDiff / 1024) / timeDiff
		}
	}

	// 更新上次的网络统计
	sm.lastNetIO = currentNetIO
	sm.lastNetTime = currentTime

	return nil
}
