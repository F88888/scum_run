package model

import "time"

// PerformanceData 性能数据结构
type PerformanceData struct {
	// 基础系统信息
	CPUCores        int   `json:"cpu_cores"`
	TotalMemory     int64 `json:"total_memory_bytes"`
	AvailableMemory int64 `json:"available_memory_bytes"`
	TotalDiskSpace  int64 `json:"total_disk_space_bytes"`
	FreeDiskSpace   int64 `json:"free_disk_space_bytes"`

	// 实时性能数据
	CPUUsage       float64   `json:"cpu_usage"`
	MemoryUsage    float64   `json:"memory_usage"`
	DiskUsage      float64   `json:"disk_usage"`
	NetworkIn      int64     `json:"network_in_bytes"`
	NetworkOut     int64     `json:"network_out_bytes"`
	DiskReadSpeed  float64   `json:"disk_read_speed"`
	DiskWriteSpeed float64   `json:"disk_write_speed"`
	ProcessCount   int       `json:"process_count"`
	LoadAverage    []float64 `json:"load_average"`

	// 性能指标
	FilesPerSecond float64 `json:"files_per_second"`
	DataThroughput float64 `json:"data_throughput"`

	// 时间戳
	Timestamp time.Time `json:"timestamp"`
}
