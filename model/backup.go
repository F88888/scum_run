package model

import (
	"time"
)

// BackupTask 备份任务
type BackupTask struct {
	TaskID           string        `json:"task_id"`
	ServerID         uint          `json:"server_id"`
	BackupType       string        `json:"backup_type"` // full, incremental, differential
	BackupPaths      []string      `json:"backup_paths"`
	ExcludePaths     []string      `json:"exclude_paths"`
	TargetPath       string        `json:"target_path"`
	Compression      bool          `json:"compression"`
	CompressionLevel int           `json:"compression_level"`
	Encryption       bool          `json:"encryption"`
	Deduplication    bool          `json:"deduplication"`
	MaxSize          int64         `json:"max_size_bytes"`
	CreatedAt        time.Time     `json:"created_at"`
	StartedAt        *time.Time    `json:"started_at,omitempty"`
	CompletedAt      *time.Time    `json:"completed_at,omitempty"`
	Status           string        `json:"status"` // pending, running, completed, failed
	Progress         float64       `json:"progress"`
	ErrorMessage     string        `json:"error_message,omitempty"`
	Result           *BackupResult `json:"result,omitempty"`
}

// BackupResult 备份结果
type BackupResult struct {
	BackupID         string  `json:"backup_id"`
	BackupSize       int64   `json:"backup_size_bytes"`
	FileCount        int     `json:"file_count"`
	Duration         int     `json:"duration_seconds"`
	CompressionRatio float64 `json:"compression_ratio"`
	BackupPath       string  `json:"backup_path"`
	Checksum         string  `json:"checksum"`

	// 性能监控数据
	CPUUsage       float64   `json:"cpu_usage"`         // 备份期间平均CPU使用率
	MemoryUsage    float64   `json:"memory_usage"`      // 备份期间平均内存使用率
	DiskUsage      float64   `json:"disk_usage"`        // 备份期间磁盘使用率
	NetworkIn      int64     `json:"network_in_bytes"`  // 网络输入流量
	NetworkOut     int64     `json:"network_out_bytes"` // 网络输出流量
	DiskReadSpeed  float64   `json:"disk_read_speed"`   // 磁盘读取速度 MB/s
	DiskWriteSpeed float64   `json:"disk_write_speed"`  // 磁盘写入速度 MB/s
	ProcessCount   int       `json:"process_count"`     // 备份时进程数量
	LoadAverage    []float64 `json:"load_average"`      // 系统负载平均值

	// 系统资源信息
	CPUCores        int   `json:"cpu_cores"`              // CPU核心数
	TotalMemory     int64 `json:"total_memory_bytes"`     // 总内存大小
	AvailableMemory int64 `json:"available_memory_bytes"` // 可用内存
	TotalDiskSpace  int64 `json:"total_disk_space_bytes"` // 总磁盘空间
	FreeDiskSpace   int64 `json:"free_disk_space_bytes"`  // 可用磁盘空间

	// 备份性能指标
	FilesPerSecond  float64 `json:"files_per_second"`         // 每秒处理文件数
	DataThroughput  float64 `json:"data_throughput"`          // 数据吞吐量 MB/s
	CompressionTime int     `json:"compression_time_seconds"` // 压缩耗时
	EncryptionTime  int     `json:"encryption_time_seconds"`  // 加密耗时

	CreatedAt time.Time `json:"created_at"`
}

// SystemMetrics 系统指标
type SystemMetrics struct {
	ServerID     uint      `json:"server_id"`
	Timestamp    time.Time `json:"timestamp"`
	CPUUsage     float64   `json:"cpu_usage"`
	MemoryUsage  float64   `json:"memory_usage"`
	DiskUsage    float64   `json:"disk_usage"`
	NetworkIn    int64     `json:"network_in_bytes"`
	NetworkOut   int64     `json:"network_out_bytes"`
	ProcessCount int       `json:"process_count"`
	Uptime       int64     `json:"uptime_seconds"`
	LoadAverage  []float64 `json:"load_average"`
}

// FileSystemInfo 文件系统信息
type FileSystemInfo struct {
	ServerID             uint             `json:"server_id"`
	Timestamp            time.Time        `json:"timestamp"`
	TotalFiles           int64            `json:"total_files"`
	TotalSize            int64            `json:"total_size_bytes"`
	FileTypeDistribution map[string]int64 `json:"file_type_distribution"`
	DirectoryStructure   map[string]int64 `json:"directory_structure"`
	LargestFiles         []FileInfo       `json:"largest_files"`
	DuplicateFiles       []DuplicateFile  `json:"duplicate_files"`
	LastModified         time.Time        `json:"last_modified"`
}

// FileInfo 文件信息
type FileInfo struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size_bytes"`
	Modified time.Time `json:"modified"`
	Type     string    `json:"type"`
	Hash     string    `json:"hash,omitempty"`
}

// DuplicateFile 重复文件
type DuplicateFile struct {
	Hash  string   `json:"hash"`
	Size  int64    `json:"size_bytes"`
	Files []string `json:"files"`
	Count int      `json:"count"`
}

// BackupConfig 备份配置
type BackupConfig struct {
	ServerID         uint      `json:"server_id"`
	Enabled          bool      `json:"enabled"`
	BackupFrequency  string    `json:"backup_frequency"`
	RetentionDays    int       `json:"retention_days"`
	CompressionLevel int       `json:"compression_level"`
	Deduplication    bool      `json:"deduplication"`
	Encryption       bool      `json:"encryption"`
	BackupPaths      []string  `json:"backup_paths"`
	ExcludePaths     []string  `json:"exclude_paths"`
	MaxBackupSize    int64     `json:"max_backup_size_bytes"`
	LastConfigUpdate time.Time `json:"last_config_update"`
}

// BackupStatus 备份状态
type BackupStatus struct {
	ServerID          uint        `json:"server_id"`
	LastBackupTime    *time.Time  `json:"last_backup_time,omitempty"`
	NextBackupTime    *time.Time  `json:"next_backup_time,omitempty"`
	CurrentTask       *BackupTask `json:"current_task,omitempty"`
	TotalBackups      int         `json:"total_backups"`
	SuccessfulBackups int         `json:"successful_backups"`
	FailedBackups     int         `json:"failed_backups"`
	TotalBackupSize   int64       `json:"total_backup_size_bytes"`
	AverageBackupSize int64       `json:"average_backup_size_bytes"`
	AverageDuration   int         `json:"average_duration_seconds"`
	SuccessRate       float64     `json:"success_rate"`
}
