package model

// UpdaterConfig 更新器配置
type UpdaterConfig struct {
	CurrentExePath string   // 当前程序路径
	UpdateURL      string   // 更新下载URL
	UpdaterExeName string   // 更新器程序名
	Args           []string // 程序启动参数
}
