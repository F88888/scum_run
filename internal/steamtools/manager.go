package steamtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"scum_run/config"
	"scum_run/internal/logger"
)

// Manager manages Steam++ lifecycle
type Manager struct {
	config               *config.SteamToolsConfig
	logger               *logger.Logger
	cmd                  *exec.Cmd
	ctx                  context.Context
	cancel               context.CancelFunc
	accelerationNotified bool      // 是否已经显示过加速提示
	cachedExecutablePath string    // 缓存检测到的可执行文件路径
	lastDetectionTime    time.Time // 上次检测时间
}

// New creates a new Steam++ manager
func New(cfg *config.SteamToolsConfig, logger *logger.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		config: cfg,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}
}

// DetectSteamTools tries to detect Steam++ installation
func (m *Manager) DetectSteamTools() string {
	m.logger.Info("正在检测 Steam++ 安装路径...")

	// If path is specified in config, use it
	if m.config.ExecutablePath != "" {
		if m.isValidSteamToolsPath(m.config.ExecutablePath) {
			m.logger.Info("使用配置指定的 Steam++ 路径: %s", m.config.ExecutablePath)
			return m.config.ExecutablePath
		}
		m.logger.Warn("配置指定的 Steam++ 路径无效: %s", m.config.ExecutablePath)
	}

	// Common installation paths for Steam++ / Watt Toolkit
	commonPaths := []string{
		// Steam++ 新版本 (Watt Toolkit)
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Steam++", "Steam++.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES"), "Steam++", "Steam++.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Steam++", "Steam++.exe"),

		// Steam++ 旧版本
		filepath.Join(os.Getenv("LOCALAPPDATA"), "SteamTools", "Steam++.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES"), "SteamTools", "Steam++.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "SteamTools", "Steam++.exe"),

		// 相对路径检测 (用户可能把 Steam++ 放在同一目录)
		"./Steam++.exe",
		"../Steam++/Steam++.exe",
		"./SteamTools/Steam++.exe",
		"../SteamTools/Steam++.exe",
	}

	for _, path := range commonPaths {
		if m.isValidSteamToolsPath(path) {
			m.logger.Info("检测到 Steam++ 安装路径: %s", path)
			return path
		}
	}

	m.logger.Warn("未能自动检测到 Steam++ 安装路径")

	// 尝试自动下载安装
	if m.config.AutoDownload && m.config.DownloadUrl != "" {
		m.logger.Info("尝试自动下载并安装 Steam++...")
		if downloadedPath, err := m.downloadAndInstallSteamTools(); err != nil {
			m.logger.Error("自动下载安装 Steam++ 失败: %v", err)
		} else {
			m.logger.Info("Steam++ 自动下载安装成功: %s", downloadedPath)
			return downloadedPath
		}
	}

	return ""
}

// downloadAndInstallSteamTools automatically downloads and installs Steam++
func (m *Manager) downloadAndInstallSteamTools() (string, error) {
	// 确定安装路径
	installDir := m.config.InstallPath
	if installDir == "" {
		// 使用默认安装路径
		installDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Steam++")
	}

	// 创建安装目录
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("创建安装目录失败: %w", err)
	}

	// 确定下载的文件名和最终的可执行文件路径
	downloadFileName := "Steam++_downloaded.exe"
	downloadPath := filepath.Join(installDir, downloadFileName)
	finalExePath := filepath.Join(installDir, "Steam++.exe")

	m.logger.Info("开始下载 Steam++ 从: %s", m.config.DownloadUrl)
	m.logger.Info("下载到: %s", downloadPath)

	// 下载文件
	if err := m.downloadFile(m.config.DownloadUrl, downloadPath); err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}

	// 验证校验和
	if m.config.VerifyChecksum && m.config.ExpectedChecksum != "" {
		m.logger.Info("正在验证文件校验和...")
		if err := m.verifyFileChecksum(downloadPath, m.config.ExpectedChecksum); err != nil {
			// 删除无效文件
			os.Remove(downloadPath)
			return "", fmt.Errorf("文件校验失败: %w", err)
		}
		m.logger.Info("文件校验通过")
	}

	// 移动文件到最终位置
	if err := os.Rename(downloadPath, finalExePath); err != nil {
		return "", fmt.Errorf("移动文件失败: %w", err)
	}

	// 设置可执行权限 (Windows上通常不需要，但为了跨平台兼容性)
	if err := os.Chmod(finalExePath, 0755); err != nil {
		m.logger.Warn("设置可执行权限失败: %v", err)
	}

	m.logger.Info("Steam++ 安装完成: %s", finalExePath)
	return finalExePath, nil
}

// downloadFile downloads a file from URL to the specified path with progress
func (m *Manager) downloadFile(url, filepath string) error {
	// 创建 HTTP 请求
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute) // 10分钟超时
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置 User-Agent
	req.Header.Set("User-Agent", "SCUM-Run-Client/1.0")

	// 发送请求
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，HTTP状态码: %d", resp.StatusCode)
	}

	// 创建目标文件
	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer out.Close()

	// 获取文件大小用于显示进度
	contentLength := resp.ContentLength

	// 创建进度报告器
	progressReader := &progressReader{
		Reader:        resp.Body,
		contentLength: contentLength,
		logger:        m.logger,
	}

	// 复制文件内容
	_, err = io.Copy(out, progressReader)
	if err != nil {
		return fmt.Errorf("下载文件内容失败: %w", err)
	}

	m.logger.Info("文件下载完成")
	return nil
}

// progressReader 实现下载进度显示
type progressReader struct {
	io.Reader
	contentLength int64
	bytesRead     int64
	logger        *logger.Logger
	lastReport    time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.bytesRead += int64(n)

	// 每5秒报告一次进度
	now := time.Now()
	if now.Sub(pr.lastReport) >= 5*time.Second {
		if pr.contentLength > 0 {
			percentage := float64(pr.bytesRead) / float64(pr.contentLength) * 100
			pr.logger.Info("下载进度: %.1f%% (%d/%d 字节)", percentage, pr.bytesRead, pr.contentLength)
		} else {
			pr.logger.Info("已下载: %d 字节", pr.bytesRead)
		}
		pr.lastReport = now
	}

	return n, err
}

// verifyFileChecksum verifies the downloaded file's SHA256 checksum
func (m *Manager) verifyFileChecksum(filepath, expectedChecksum string) error {
	file, err := os.Open(filepath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("计算校验和失败: %w", err)
	}

	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	expectedChecksum = strings.ToLower(strings.TrimSpace(expectedChecksum))
	actualChecksum = strings.ToLower(actualChecksum)

	if actualChecksum != expectedChecksum {
		return fmt.Errorf("校验和不匹配，期望: %s, 实际: %s", expectedChecksum, actualChecksum)
	}

	return nil
}

// isValidSteamToolsPath checks if the given path is a valid Steam++ executable
func (m *Manager) isValidSteamToolsPath(path string) bool {
	if path == "" {
		return false
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}

	// For Windows, ensure it's an .exe file
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(path), ".exe") {
		return false
	}

	return true
}

// Start starts Steam++ with network acceleration
func (m *Manager) Start() error {
	if !m.config.Enabled {
		m.logger.Info("Steam++ 集成已禁用")
		return nil
	}

	// Detect Steam++ path
	execPath := m.DetectSteamTools()
	if execPath == "" {
		return fmt.Errorf("未找到 Steam++ 可执行文件，请检查安装路径或启用自动下载功能")
	}

	// Check if Steam++ is already running
	if m.isSteamToolsRunning() {
		m.logger.Info("Steam++ 已在运行，跳过启动")
		if m.config.AutoAccelerate {
			return m.enableAcceleration()
		}
		return nil
	}

	m.logger.Info("正在启动 Steam++: %s", execPath)

	// Prepare command arguments
	args := []string{}

	// Add minimized startup if auto start
	if m.config.AutoStart {
		args = append(args, "-m") // Minimized start
	}

	// Create command
	m.cmd = exec.CommandContext(m.ctx, execPath, args...)

	// Start Steam++
	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("启动 Steam++ 失败: %w", err)
	}

	m.logger.Info("Steam++ 已启动，PID: %d", m.cmd.Process.Pid)

	// Wait for Steam++ to initialize
	if m.config.AutoAccelerate {
		m.logger.Info("等待 Steam++ 初始化完成...")
		time.Sleep(time.Second)

		// Enable acceleration
		return m.enableAcceleration()
	}

	return nil
}

// Stop stops Steam++
func (m *Manager) Stop() error {
	m.logger.Info("正在停止 Steam++...")

	m.cancel()

	if m.cmd != nil && m.cmd.Process != nil {
		// Try graceful shutdown first
		if err := m.cmd.Process.Signal(os.Interrupt); err != nil {
			m.logger.Warn("发送中断信号失败，强制终止: %v", err)
			if err := m.cmd.Process.Kill(); err != nil {
				return fmt.Errorf("强制终止 Steam++ 失败: %w", err)
			}
		}

		// Wait for process to exit
		if err := m.cmd.Wait(); err != nil {
			m.logger.Warn("Steam++ 进程退出异常: %v", err)
		}

		m.cmd = nil
		m.logger.Info("Steam++ 已停止")
	}

	return nil
}

// enableAcceleration enables network acceleration for specified items
func (m *Manager) enableAcceleration() error {
	m.logger.Info("正在启用网络加速...")

	// Note: Steam++ doesn't provide a direct command line interface for enabling acceleration
	// This is a limitation of the current Steam++ design
	// Users need to manually enable acceleration in the GUI or use automation tools

	// 只在第一次时显示详细提示
	if !m.accelerationNotified {
		if len(m.config.AccelerateItems) > 0 {
			m.logger.Info("请手动在 Steam++ 界面中启用网络加速功能")
			m.logger.Info("建议加速项目: %v", m.config.AccelerateItems)
			m.logger.Info("操作步骤:")
			m.logger.Info("1. 打开 Steam++ 主界面")
			m.logger.Info("2. 点击左侧的 '网络加速' 选项")
			m.logger.Info("3. 选择需要加速的服务并点击 '一键加速'")
			m.logger.Info("4. 确认加速状态显示为 '已启用'")
		} else {
			m.logger.Info("请手动在 Steam++ 界面中启用网络加速功能")
			m.logger.Info("建议加速项目: Steam 商店、Steam 社区、Steam API、Github")
		}
		m.accelerationNotified = true
	} else {
		m.logger.Debug("Steam++ 网络加速需要手动启用（已提示过）")
	}

	return nil
}

// isSteamToolsRunning checks if Steam++ is currently running
func (m *Manager) isSteamToolsRunning() bool {
	if runtime.GOOS != "windows" {
		return false
	}

	// Use tasklist to check for Steam++ processes
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq Steam++.exe")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.Contains(string(output), "Steam++.exe")
}

// GetStatus returns the current status of Steam++
func (m *Manager) GetStatus() map[string]interface{} {
	status := map[string]interface{}{
		"enabled":         m.config.Enabled,
		"running":         m.isSteamToolsRunning(),
		"auto_start":      m.config.AutoStart,
		"auto_accelerate": m.config.AutoAccelerate,
		"auto_download":   m.config.AutoDownload,
		"download_url":    m.config.DownloadUrl,
	}

	if m.cmd != nil && m.cmd.Process != nil {
		status["pid"] = m.cmd.Process.Pid
	}

	// 使用缓存的路径，避免重复检测
	execPath := m.getCachedExecutablePath()
	if execPath != "" {
		status["executable_path"] = execPath
	}

	return status
}

// getCachedExecutablePath returns cached executable path or detects it if cache is expired
func (m *Manager) getCachedExecutablePath() string {
	// 如果缓存有效（5分钟内），直接返回缓存的路径
	if m.cachedExecutablePath != "" && time.Since(m.lastDetectionTime) < 5*time.Minute {
		return m.cachedExecutablePath
	}

	// 缓存过期或为空，重新检测
	execPath := m.DetectSteamTools()
	if execPath != "" {
		m.cachedExecutablePath = execPath
		m.lastDetectionTime = time.Now()
	}

	return execPath
}
