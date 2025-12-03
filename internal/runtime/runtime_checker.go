package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	_const "scum_run/internal/const"
	"scum_run/internal/logger"
	"strings"
	"time"
)

// Checker 运行时依赖检查器
type Checker struct {
	logger *logger.Logger
}

// NewChecker 创建新的运行时检查器
func NewChecker(logger *logger.Logger) *Checker {
	return &Checker{logger: logger}
}

// CheckAndInstallRuntimes 检查并安装必要的运行时依赖
func (c *Checker) CheckAndInstallRuntimes() error {
	if runtime.GOOS != "windows" {
		c.logger.Info("运行时检查仅支持 Windows 系统")
		return nil
	}

	c.logger.Info("🔍 检查必要的运行时依赖...")

	// 检查 Visual C++ Redistributables
	if err := c.checkAndInstallVCRedist(); err != nil {
		return fmt.Errorf("Visual C++ Redistributables 检查/安装失败: %w", err)
	}

	// 检查 DirectX
	if err := c.checkAndInstallDirectX(); err != nil {
		return fmt.Errorf("DirectX 检查/安装失败: %w", err)
	}

	c.logger.Info("✅ 所有运行时依赖检查完成")
	return nil
}

// checkAndInstallVCRedist 检查并安装 Visual C++ Redistributables v14 (适用于 Visual Studio 2017-2026)
func (c *Checker) checkAndInstallVCRedist() error {
	c.logger.Info("检查 Visual C++ Redistributables v14 (适用于 Visual Studio 2017-2026)...")

	// Visual C++ Redistributable v14 的注册表路径
	registryPaths := []string{
		_const.VCRedistRegistryPath1,
		_const.VCRedistRegistryPath2,
	}

	// 检查是否已安装
	installed := false
	for _, regPath := range registryPaths {
		if ok, _ := c.checkVCRedistInstalled(regPath); ok {
			installed = true
			break
		}
	}

	if !installed {
		c.logger.Warn("❌ Visual C++ Redistributables v14 未安装")

		// 下载并安装
		name := "Visual C++ Redistributables v14 (适用于 Visual Studio 2017-2026)"
		if err := c.downloadAndInstallVCRedist(_const.DefaultVisualCURL, _const.VCRedistFilename, name); err != nil {
			c.logger.Error("安装 %s 失败: %v", name, err)
			return err
		}
	} else {
		c.logger.Info("✅ Visual C++ Redistributables v14 已安装")
	}

	return nil
}

// checkVCRedistInstalled 检查 Visual C++ Redistributable 是否已安装
func (c *Checker) checkVCRedistInstalled(registryPath string) (bool, error) {
	// 使用 reg query 命令检查注册表
	cmd := exec.Command("reg", "query", fmt.Sprintf("%s%s", _const.RegistryHKLMPrefix, registryPath), _const.RegistryQueryV, _const.RegistryVersionKey)
	output, err := cmd.Output()
	if err != nil {
		// 如果命令失败，可能表示未安装
		return false, nil
	}

	// 检查输出中是否包含 Version
	return strings.Contains(strings.ToLower(string(output)), strings.ToLower(_const.RegistryVersionKey)), nil
}

// downloadAndInstallVCRedist 下载并安装 Visual C++ Redistributable
func (c *Checker) downloadAndInstallVCRedist(url, filename, name string) error {
	c.logger.Info("📥 开始下载 %s...", name)

	// 创建临时目录
	tempDir := filepath.Join(os.TempDir(), _const.RuntimeTempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}

	// 下载文件
	filePath := filepath.Join(tempDir, filename)
	if err := c.downloadFile(url, filePath); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}

	c.logger.Info("📦 开始安装 %s...", name)

	// Visual C++ Redistributable v14 使用 /install /quiet /norestart 参数
	installArgs := []string{_const.VCInstallArgInstall, _const.VCInstallArgQuiet, _const.VCInstallArgNoRestart}
	cmd := exec.Command(filePath, installArgs...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("安装失败: %w", err)
	}

	c.logger.Info("✅ %s 安装完成", name)

	// 等待安装完成
	time.Sleep(time.Duration(_const.VCRedistWaitTime) * time.Second)

	// 清理临时文件
	os.Remove(filePath)

	return nil
}

// checkAndInstallDirectX 检查并安装 DirectX
func (c *Checker) checkAndInstallDirectX() error {
	c.logger.Info("检查 DirectX End-User Runtimes...")

	// 检查 d3dx9_43.dll 是否存在（DirectX 9 的典型文件）
	directX9Path := filepath.Join(os.Getenv("WINDIR"), _const.WindowsSystem32, _const.DirectXCheckDll)
	if _, err := os.Stat(directX9Path); err == nil {
		c.logger.Info("✅ DirectX 已安装")
		return nil
	}

	c.logger.Warn("❌ DirectX 未安装，开始下载安装...")

	// 下载并安装
	if err := c.downloadAndInstallDirectX(_const.DefaultDirectxURL, _const.DirectXRedistFile); err != nil {
		return err
	}

	return nil
}

// downloadAndInstallDirectX 下载并安装 DirectX
func (c *Checker) downloadAndInstallDirectX(url, filename string) error {
	c.logger.Info("📥 开始下载 DirectX End-User Runtimes...")

	// 创建临时目录
	tempDir := filepath.Join(os.TempDir(), _const.RuntimeTempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}

	// 下载文件
	filePath := filepath.Join(tempDir, filename)
	if err := c.downloadFile(url, filePath); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}

	c.logger.Info("📦 开始安装 DirectX...")

	// 解压并安装 DirectX（DirectX 安装程序需要先解压）
	extractDir := filepath.Join(tempDir, _const.DirectXExtractDir)
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return fmt.Errorf("创建解压目录失败: %w", err)
	}

	// DirectX 安装程序需要 /Q 参数进行静默安装
	cmd := exec.Command(filePath, _const.DirectXExtractArgQ, _const.DirectXExtractArgT+extractDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}

	// 运行解压后的安装程序
	installerPath := filepath.Join(extractDir, _const.DirectXSetupExe)
	installCmd := exec.Command(installerPath, _const.DirectXInstallArgSilent)
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("安装失败: %w", err)
	}

	c.logger.Info("✅ DirectX 安装完成")

	// 等待安装完成
	time.Sleep(time.Duration(_const.DirectXWaitTime) * time.Second)

	// 清理临时文件
	os.RemoveAll(tempDir)

	return nil
}

// downloadFile 下载文件
func (c *Checker) downloadFile(url, filePath string) error {
	// 使用 PowerShell 下载文件（Windows 内置）
	psScript := fmt.Sprintf(`
		$ProgressPreference = 'SilentlyContinue'
		Invoke-WebRequest -Uri "%s" -OutFile "%s"
	`, url, filePath)

	cmd := exec.Command("powershell", "-Command", psScript)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("PowerShell 下载失败: %w", err)
	}

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("下载的文件不存在: %s", filePath)
	}

	return nil
}
