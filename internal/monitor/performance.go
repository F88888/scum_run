package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"scum_run/internal/logger"
	"scum_run/model"
)

// PerformanceMonitor 性能监控器
type PerformanceMonitor struct {
	logger      *logger.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	interval    time.Duration
	callback    PerformanceMonitorCallback
	lastNetIO   *net.IOCountersStat
	lastNetTime time.Time
	mutex       sync.RWMutex

	// 性能数据累积
	cpuReadings        []float64
	memReadings        []float64
	diskReadings       []float64
	networkInReadings  []int64
	networkOutReadings []int64
	diskReadSpeeds     []float64
	diskWriteSpeeds    []float64
	processCounts      []int
	loadAverages       [][]float64
}

// PerformanceMonitorCallback 性能监控数据回调函数
type PerformanceMonitorCallback func(data *model.PerformanceData)

// NewPerformanceMonitor 创建新的性能监控器
func NewPerformanceMonitor(logger *logger.Logger, interval time.Duration) *PerformanceMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &PerformanceMonitor{
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		interval: interval,
	}
}

// SetCallback 设置监控数据回调函数
func (pm *PerformanceMonitor) SetCallback(callback PerformanceMonitorCallback) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	pm.callback = callback
}

// Start 启动性能监控
func (pm *PerformanceMonitor) Start() error {
	pm.logger.Info("Starting performance monitor with interval: %v", pm.interval)

	// 初始化网络IO统计
	if err := pm.initNetworkStats(); err != nil {
		pm.logger.Warn("Failed to initialize network stats: %v", err)
	}

	pm.wg.Add(1)
	go pm.monitorLoop()

	return nil
}

// Stop 停止性能监控
func (pm *PerformanceMonitor) Stop() {
	pm.logger.Info("Stopping performance monitor")
	pm.cancel()
	pm.wg.Wait()
}

// GetAverageData 获取平均性能数据
func (pm *PerformanceMonitor) GetAverageData() *model.PerformanceData {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	if len(pm.cpuReadings) == 0 {
		return &model.PerformanceData{}
	}

	// 计算平均值
	avgCPU := pm.calculateAverage(pm.cpuReadings)
	avgMem := pm.calculateAverage(pm.memReadings)
	avgDisk := pm.calculateAverage(pm.diskReadings)

	avgNetworkIn := pm.calculateInt64Average(pm.networkInReadings)
	avgNetworkOut := pm.calculateInt64Average(pm.networkOutReadings)
	avgDiskRead := pm.calculateAverage(pm.diskReadSpeeds)
	avgDiskWrite := pm.calculateAverage(pm.diskWriteSpeeds)
	avgProcessCount := pm.calculateIntAverage(pm.processCounts)

	// 计算负载平均值
	var avgLoadAverage []float64
	if len(pm.loadAverages) > 0 {
		avgLoadAverage = make([]float64, len(pm.loadAverages[0]))
		for i := range avgLoadAverage {
			var sum float64
			for _, load := range pm.loadAverages {
				if i < len(load) {
					sum += load[i]
				}
			}
			avgLoadAverage[i] = sum / float64(len(pm.loadAverages))
		}
	}

	return &model.PerformanceData{
		CPUCores:        pm.getCPUCores(),
		TotalMemory:     pm.getTotalMemory(),
		AvailableMemory: pm.getAvailableMemory(),
		TotalDiskSpace:  pm.getTotalDiskSpace(),
		FreeDiskSpace:   pm.getFreeDiskSpace(),
		CPUUsage:        avgCPU,
		MemoryUsage:     avgMem,
		DiskUsage:       avgDisk,
		NetworkIn:       avgNetworkIn,
		NetworkOut:      avgNetworkOut,
		DiskReadSpeed:   avgDiskRead,
		DiskWriteSpeed:  avgDiskWrite,
		ProcessCount:    avgProcessCount,
		LoadAverage:     avgLoadAverage,
		Timestamp:       time.Now(),
	}
}

// ClearData 清空累积数据
func (pm *PerformanceMonitor) ClearData() {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.cpuReadings = pm.cpuReadings[:0]
	pm.memReadings = pm.memReadings[:0]
	pm.diskReadings = pm.diskReadings[:0]
	pm.networkInReadings = pm.networkInReadings[:0]
	pm.networkOutReadings = pm.networkOutReadings[:0]
	pm.diskReadSpeeds = pm.diskReadSpeeds[:0]
	pm.diskWriteSpeeds = pm.diskWriteSpeeds[:0]
	pm.processCounts = pm.processCounts[:0]
	pm.loadAverages = pm.loadAverages[:0]
}

// initNetworkStats 初始化网络统计
func (pm *PerformanceMonitor) initNetworkStats() error {
	netIO, err := net.IOCounters(false)
	if err != nil {
		return fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(netIO) > 0 {
		pm.mutex.Lock()
		pm.lastNetIO = &netIO[0]
		pm.lastNetTime = time.Now()
		pm.mutex.Unlock()
	}

	return nil
}

// monitorLoop 监控循环
func (pm *PerformanceMonitor) monitorLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(pm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			data, err := pm.collectPerformanceData()
			if err != nil {
				pm.logger.Error("Failed to collect performance data: %v", err)
				continue
			}

			// 累积数据
			pm.accumulateData(data)

			pm.mutex.RLock()
			callback := pm.callback
			pm.mutex.RUnlock()

			if callback != nil {
				callback(data)
			}
		}
	}
}

// collectPerformanceData 收集性能数据
func (pm *PerformanceMonitor) collectPerformanceData() (*model.PerformanceData, error) {
	data := &model.PerformanceData{
		Timestamp: time.Now(),
	}

	// 收集系统基础信息
	data.CPUCores = pm.getCPUCores()
	data.TotalMemory = pm.getTotalMemory()
	data.AvailableMemory = pm.getAvailableMemory()
	data.TotalDiskSpace = pm.getTotalDiskSpace()
	data.FreeDiskSpace = pm.getFreeDiskSpace()

	// 收集CPU使用率
	if err := pm.collectCPUUsage(data); err != nil {
		pm.logger.Warn("Failed to collect CPU usage: %v", err)
	}

	// 收集内存使用率
	if err := pm.collectMemoryUsage(data); err != nil {
		pm.logger.Warn("Failed to collect memory usage: %v", err)
	}

	// 收集磁盘使用率
	if err := pm.collectDiskUsage(data); err != nil {
		pm.logger.Warn("Failed to collect disk usage: %v", err)
	}

	// 收集网络流量
	if err := pm.collectNetworkUsage(data); err != nil {
		pm.logger.Warn("Failed to collect network usage: %v", err)
	}

	// 收集磁盘IO速度
	if err := pm.collectDiskIOSpeed(data); err != nil {
		pm.logger.Warn("Failed to collect disk IO speed: %v", err)
	}

	// 收集进程数量
	if err := pm.collectProcessCount(data); err != nil {
		pm.logger.Warn("Failed to collect process count: %v", err)
	}

	// 收集系统负载
	if err := pm.collectLoadAverage(data); err != nil {
		pm.logger.Warn("Failed to collect load average: %v", err)
	}

	return data, nil
}

// accumulateData 累积性能数据
func (pm *PerformanceMonitor) accumulateData(data *model.PerformanceData) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.cpuReadings = append(pm.cpuReadings, data.CPUUsage)
	pm.memReadings = append(pm.memReadings, data.MemoryUsage)
	pm.diskReadings = append(pm.diskReadings, data.DiskUsage)
	pm.networkInReadings = append(pm.networkInReadings, data.NetworkIn)
	pm.networkOutReadings = append(pm.networkOutReadings, data.NetworkOut)
	pm.diskReadSpeeds = append(pm.diskReadSpeeds, data.DiskReadSpeed)
	pm.diskWriteSpeeds = append(pm.diskWriteSpeeds, data.DiskWriteSpeed)
	pm.processCounts = append(pm.processCounts, data.ProcessCount)
	pm.loadAverages = append(pm.loadAverages, data.LoadAverage)

	// 限制数据量，只保留最近1000个读数
	maxReadings := 1000
	if len(pm.cpuReadings) > maxReadings {
		pm.cpuReadings = pm.cpuReadings[len(pm.cpuReadings)-maxReadings:]
		pm.memReadings = pm.memReadings[len(pm.memReadings)-maxReadings:]
		pm.diskReadings = pm.diskReadings[len(pm.diskReadings)-maxReadings:]
		pm.networkInReadings = pm.networkInReadings[len(pm.networkInReadings)-maxReadings:]
		pm.networkOutReadings = pm.networkOutReadings[len(pm.networkOutReadings)-maxReadings:]
		pm.diskReadSpeeds = pm.diskReadSpeeds[len(pm.diskReadSpeeds)-maxReadings:]
		pm.diskWriteSpeeds = pm.diskWriteSpeeds[len(pm.diskWriteSpeeds)-maxReadings:]
		pm.processCounts = pm.processCounts[len(pm.processCounts)-maxReadings:]
		pm.loadAverages = pm.loadAverages[len(pm.loadAverages)-maxReadings:]
	}
}

// collectCPUUsage 收集CPU使用率
func (pm *PerformanceMonitor) collectCPUUsage(data *model.PerformanceData) error {
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
func (pm *PerformanceMonitor) collectMemoryUsage(data *model.PerformanceData) error {
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return fmt.Errorf("failed to get memory info: %w", err)
	}

	data.MemoryUsage = memInfo.UsedPercent
	return nil
}

// collectDiskUsage 收集磁盘使用率
func (pm *PerformanceMonitor) collectDiskUsage(data *model.PerformanceData) error {
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
func (pm *PerformanceMonitor) collectNetworkUsage(data *model.PerformanceData) error {
	netIO, err := net.IOCounters(false)
	if err != nil {
		return fmt.Errorf("failed to get network IO counters: %w", err)
	}

	if len(netIO) == 0 {
		return fmt.Errorf("no network interfaces found")
	}

	currentNetIO := &netIO[0]
	currentTime := time.Now()

	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	if pm.lastNetIO != nil && !pm.lastNetTime.IsZero() {
		// 计算时间差（秒）
		timeDiff := currentTime.Sub(pm.lastNetTime).Seconds()
		if timeDiff > 0 {
			// 计算流量差（字节）
			bytesInDiff := currentNetIO.BytesRecv - pm.lastNetIO.BytesRecv
			bytesOutDiff := currentNetIO.BytesSent - pm.lastNetIO.BytesSent

			data.NetworkIn = int64(bytesInDiff)
			data.NetworkOut = int64(bytesOutDiff)
		}
	}

	// 更新上次的网络统计
	pm.lastNetIO = currentNetIO
	pm.lastNetTime = currentTime

	return nil
}

// collectDiskIOSpeed 收集磁盘IO速度
func (pm *PerformanceMonitor) collectDiskIOSpeed(data *model.PerformanceData) error {
	diskIO, err := disk.IOCounters()
	if err != nil {
		return fmt.Errorf("failed to get disk IO counters: %w", err)
	}

	var totalReadBytes, totalWriteBytes uint64
	for _, io := range diskIO {
		totalReadBytes += io.ReadBytes
		totalWriteBytes += io.WriteBytes
	}

	// 这里简化处理，实际应该计算速度
	// 由于需要时间间隔来计算速度，这里先返回0
	data.DiskReadSpeed = 0
	data.DiskWriteSpeed = 0

	return nil
}

// collectProcessCount 收集进程数量
func (pm *PerformanceMonitor) collectProcessCount(data *model.PerformanceData) error {
	processes, err := process.Processes()
	if err != nil {
		return fmt.Errorf("failed to get processes: %w", err)
	}

	data.ProcessCount = len(processes)
	return nil
}

// collectLoadAverage 收集系统负载
func (pm *PerformanceMonitor) collectLoadAverage(data *model.PerformanceData) error {
	loadAvg, err := load.Avg()
	if err != nil {
		return fmt.Errorf("failed to get load average: %w", err)
	}

	data.LoadAverage = []float64{loadAvg.Load1, loadAvg.Load5, loadAvg.Load15}
	return nil
}

// getCPUCores 获取CPU核心数
func (pm *PerformanceMonitor) getCPUCores() int {
	cores, err := cpu.Counts(true)
	if err != nil {
		pm.logger.Warn("Failed to get CPU cores: %v", err)
		return 0
	}
	return cores
}

// getTotalMemory 获取总内存
func (pm *PerformanceMonitor) getTotalMemory() int64 {
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		pm.logger.Warn("Failed to get total memory: %v", err)
		return 0
	}
	return int64(memInfo.Total)
}

// getAvailableMemory 获取可用内存
func (pm *PerformanceMonitor) getAvailableMemory() int64 {
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		pm.logger.Warn("Failed to get available memory: %v", err)
		return 0
	}
	return int64(memInfo.Available)
}

// getTotalDiskSpace 获取总磁盘空间
func (pm *PerformanceMonitor) getTotalDiskSpace() int64 {
	diskUsage, err := disk.Usage("/")
	if err != nil {
		diskUsage, err = disk.Usage("C:")
		if err != nil {
			pm.logger.Warn("Failed to get total disk space: %v", err)
			return 0
		}
	}
	return int64(diskUsage.Total)
}

// getFreeDiskSpace 获取可用磁盘空间
func (pm *PerformanceMonitor) getFreeDiskSpace() int64 {
	diskUsage, err := disk.Usage("/")
	if err != nil {
		diskUsage, err = disk.Usage("C:")
		if err != nil {
			pm.logger.Warn("Failed to get free disk space: %v", err)
			return 0
		}
	}
	return int64(diskUsage.Free)
}

// calculateAverage 计算float64数组的平均值
func (pm *PerformanceMonitor) calculateAverage(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// calculateInt64Average 计算int64数组的平均值
func (pm *PerformanceMonitor) calculateInt64Average(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}

	var sum int64
	for _, v := range values {
		sum += v
	}
	return sum / int64(len(values))
}

// calculateIntAverage 计算int数组的平均值
func (pm *PerformanceMonitor) calculateIntAverage(values []int) int {
	if len(values) == 0 {
		return 0
	}

	var sum int
	for _, v := range values {
		sum += v
	}
	return sum / len(values)
}
