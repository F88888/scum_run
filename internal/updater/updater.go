package updater

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// UpdaterConfig 更新器配置
type UpdaterConfig struct {
	CurrentExePath string   // 当前程序路径
	UpdateURL      string   // 更新下载URL
	UpdaterExeName string   // 更新器程序名
	Args           []string // 程序启动参数
}

// CreateUpdaterScript 创建独立的更新器脚本
func CreateUpdaterScript(config UpdaterConfig) error {
	var scriptContent string
	var scriptName string

	if runtime.GOOS == "windows" {
		scriptName = "scum_run_updater.bat"
		scriptContent = fmt.Sprintf(`@echo off
echo Starting SCUM Run updater...

:: 等待主程序完全退出
timeout /t 3 /nobreak >nul

:: 下载新版本
echo Downloading update from %s...
powershell -Command "Invoke-WebRequest -Uri '%s' -OutFile 'scum_run_new.exe'"

if not exist "scum_run_new.exe" (
    echo Download failed!
    pause
    exit /b 1
)

:: 备份当前版本
if exist "%s" (
    echo Backing up current version...
    copy "%s" "%s.backup" >nul
    if errorlevel 1 (
        echo Backup failed!
        del "scum_run_new.exe" >nul
        pause
        exit /b 1
    )
)

:: 替换程序文件
echo Installing update...
copy "scum_run_new.exe" "%s" >nul
if errorlevel 1 (
    echo Installation failed! Restoring backup...
    copy "%s.backup" "%s" >nul
    del "scum_run_new.exe" >nul
    pause
    exit /b 1
)

:: 清理临时文件
del "scum_run_new.exe" >nul
del "%s.backup" >nul

:: 重启程序
echo Restarting SCUM Run client...
start "" "%s" %s

:: 删除更新器脚本自己
del "%%~f0"
`, config.UpdateURL, config.UpdateURL, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, formatArgs(config.Args))
	} else {
		scriptName = "scum_run_updater.sh"
		scriptContent = fmt.Sprintf(`#!/bin/bash
echo "Starting SCUM Run updater..."

# 等待主程序完全退出
sleep 3

# 下载新版本
echo "Downloading update from %s..."
if ! curl -L -o "scum_run_new" "%s"; then
    echo "Download failed!"
    exit 1
fi

# 备份当前版本
if [ -f "%s" ]; then
    echo "Backing up current version..."
    if ! cp "%s" "%s.backup"; then
        echo "Backup failed!"
        rm -f "scum_run_new"
        exit 1
    fi
fi

# 替换程序文件
echo "Installing update..."
if ! cp "scum_run_new" "%s"; then
    echo "Installation failed! Restoring backup..."
    cp "%s.backup" "%s" 2>/dev/null
    rm -f "scum_run_new"
    exit 1
fi

# 设置执行权限
chmod +x "%s"

# 清理临时文件
rm -f "scum_run_new"
rm -f "%s.backup"

# 重启程序
echo "Restarting SCUM Run client..."
nohup "%s" %s > /dev/null 2>&1 &

# 删除更新器脚本自己
rm -f "$0"
`, config.UpdateURL, config.UpdateURL, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, formatArgs(config.Args))
	}

	// 写入脚本文件
	if err := os.WriteFile(scriptName, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("failed to create updater script: %w", err)
	}

	return nil
}

// ExecuteUpdate 执行更新流程
func ExecuteUpdate(config UpdaterConfig) error {
	// 1. 创建更新器脚本
	if err := CreateUpdaterScript(config); err != nil {
		return fmt.Errorf("failed to create updater script: %w", err)
	}

	// 2. 启动更新器脚本
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "scum_run_updater.bat")
	} else {
		cmd = exec.Command("bash", "scum_run_updater.sh")
	}

	// 分离进程，让更新器独立运行
	cmd.SysProcAttr = getSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start updater: %w", err)
	}

	return nil
}

// DownloadAndPrepareUpdate 下载并准备更新文件
func DownloadAndPrepareUpdate(downloadURL, targetPath string) error {
	// 创建临时目录
	tempDir := "temp_update"
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// 下载文件
	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// 保存到临时文件
	tempFile := filepath.Join(tempDir, "scum_run_update")
	if runtime.GOOS == "windows" {
		tempFile += ".exe"
	}

	out, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save update file: %w", err)
	}

	// 移动到最终位置
	if err := os.Rename(tempFile, targetPath); err != nil {
		return fmt.Errorf("failed to move update file: %w", err)
	}

	return nil
}

// formatArgs 格式化命令行参数
func formatArgs(args []string) string {
	result := ""
	for _, arg := range args {
		if result != "" {
			result += " "
		}
		// 如果参数包含空格，需要加引号
		if containsSpace(arg) {
			result += `"` + arg + `"`
		} else {
			result += arg
		}
	}
	return result
}

// containsSpace 检查字符串是否包含空格
func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' {
			return true
		}
	}
	return false
}
