package _const

const (
	// 更新相关常量
	UpdateCheckURL      = "https://api.github.com/repos/your-org/scum_run/releases/latest" // GitHub API获取最新版本
	UpdateDownloadURL   = "https://github.com/your-org/scum_run/releases/download"         // GitHub下载基础URL
	UpdateTempDir       = "temp_update"                                                    // 临时更新目录
	UpdateBackupSuffix  = ".backup"                                                        // 备份文件后缀
	UpdateRetryCount    = 3                                                                // 更新重试次数
	UpdateTimeoutSecond = 300                                                              // 更新超时时间（秒）

	// 更新状态
	UpdateStatusChecking    = "checking"    // 检查更新中
	UpdateStatusDownloading = "downloading" // 下载中
	UpdateStatusInstalling  = "installing"  // 安装中
	UpdateStatusCompleted   = "completed"   // 更新完成
	UpdateStatusFailed      = "failed"      // 更新失败
	UpdateStatusNoUpdate    = "no_update"   // 无需更新
)
