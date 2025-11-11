package updater

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"scum_run/model"
)

// CreateUpdaterScript 创建独立的更新器脚本
func CreateUpdaterScript(config model.UpdaterConfig) error {
	var scriptContent string
	var scriptName string

	if runtime.GOOS == "windows" {
		scriptName = "scum_run_updater.bat"
		// 获取可执行文件所在目录
		exeDir := filepath.Dir(config.CurrentExePath)
		tempNewFile := filepath.Join(exeDir, "scum_run_new.exe")
		backupFile := config.CurrentExePath + ".backup"

		scriptContent = fmt.Sprintf(`@echo off
echo Starting SCUM Run updater...
echo Working directory: %%CD%%
echo Exe directory: %s

:: 切换到可执行文件所在目录
cd /d "%s"
if errorlevel 1 (
    echo Failed to change directory to %s
    pause
    exit /b 1
)

echo Changed to directory: %%CD%%

:: 等待主程序完全退出
echo Waiting for main program to exit...
timeout /t 3 /nobreak >nul

:: 下载新版本到可执行文件目录
echo Downloading update from %s...
echo Target file: %s

:: 设置PowerShell TLS 1.2支持并下载
powershell -Command "[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; try { Invoke-WebRequest -Uri '%s' -OutFile '%s' -UseBasicParsing -TimeoutSec 300; Write-Host 'Download successful' } catch { Write-Host ('Download error: ' + $_.Exception.Message); exit 1 }"

if errorlevel 1 (
    echo PowerShell download failed, trying alternative method...
    echo Trying curl...
    curl -L -o "%s" "%s"
    if errorlevel 1 (
        echo All download methods failed!
        pause
        exit /b 1
    )
)

if not exist "%s" (
    echo Download failed! File not found: %s
    dir "%s"
    pause
    exit /b 1
)

:: 验证下载的文件大小
for %%%%A in ("%s") do set filesize=%%%%~zA
echo Downloaded file size: %%filesize%% bytes
if %%filesize%% LSS 1000 (
    echo Downloaded file is too small, probably an error page
    type "%s"
    pause
    exit /b 1
)

echo Download completed successfully: %s

:: 备份当前版本
if exist "%s" (
    echo Backing up current version: %s
    copy "%s" "%s" >nul
    if errorlevel 1 (
        echo Backup failed!
        del "%s" >nul
        pause
        exit /b 1
    )
    echo Backup created: %s
)

:: 替换程序文件
echo Installing update...
echo Copying %s to %s
copy /Y "%s" "%s" >nul
if errorlevel 1 (
    echo Installation failed! Restoring backup...
    if exist "%s" (
        copy /Y "%s" "%s" >nul
    )
    del "%s" >nul
    pause
    exit /b 1
)

echo Installation completed successfully

:: 清理临时文件
echo Cleaning up temporary files...
del "%s" >nul
if exist "%s" (
    del "%s" >nul
)

:: 重启程序
echo Restarting SCUM Run client: %s
start "" "%s" %s

:: 删除更新器脚本自己
echo Deleting updater script...
(goto) 2>nul & del "%%%%~f0"
`,
			// 占位符参数列表
			exeDir, exeDir, exeDir, // 1-3: 工作目录相关
			config.UpdateURL, tempNewFile, // 4-5: 下载说明
			config.UpdateURL, tempNewFile, // 6-7: PowerShell 下载
			tempNewFile, config.UpdateURL, // 8-9: curl 备用下载
			tempNewFile, tempNewFile, exeDir, // 10-12: 检查文件是否存在
			tempNewFile, tempNewFile, tempNewFile, // 13-15: 验证文件大小
			config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, backupFile, // 16-19: 备份
			tempNewFile, backupFile, // 20-21: 备份失败清理
			tempNewFile, config.CurrentExePath, tempNewFile, config.CurrentExePath, // 22-25: 复制新文件
			backupFile, backupFile, config.CurrentExePath, tempNewFile, // 26-29: 恢复备份
			tempNewFile, backupFile, backupFile, // 30-32: 清理临时文件
			config.CurrentExePath, config.CurrentExePath, formatArgs(config.Args)) // 33-35: 重启程序
	} else {
		scriptName = "scum_run_updater.sh"
		// 获取可执行文件所在目录
		exeDir := filepath.Dir(config.CurrentExePath)
		tempNewFile := filepath.Join(exeDir, "scum_run_new")
		backupFile := config.CurrentExePath + ".backup"

		scriptContent = fmt.Sprintf(`#!/bin/bash
echo "Starting SCUM Run updater..."
echo "Working directory: $(pwd)"
echo "Exe directory: %s"

# 切换到可执行文件所在目录
cd "%s" || {
    echo "Failed to change directory to %s"
    exit 1
}

echo "Changed to directory: $(pwd)"

# 等待主程序完全退出
echo "Waiting for main program to exit..."
sleep 3

# 下载新版本到可执行文件目录
echo "Downloading update from %s..."
echo "Target file: %s"

# 尝试下载
if ! curl -L -o "%s" "%s" --connect-timeout 30 --max-time 300; then
    echo "curl download failed, trying wget..."
    if ! wget -O "%s" "%s" --timeout=300; then
        echo "All download methods failed!"
        exit 1
    fi
fi

if [ ! -f "%s" ]; then
    echo "Download failed! File not found: %s"
    ls -lh "%s"
    exit 1
fi

# 验证下载的文件大小
filesize=$(stat -f%%z "%s" 2>/dev/null || stat -c%%s "%s" 2>/dev/null)
echo "Downloaded file size: $filesize bytes"
if [ "$filesize" -lt 1000 ]; then
    echo "Downloaded file is too small, probably an error"
    cat "%s"
    exit 1
fi

echo "Download completed successfully: %s"

# 备份当前版本
if [ -f "%s" ]; then
    echo "Backing up current version: %s"
    if ! cp "%s" "%s"; then
        echo "Backup failed!"
        rm -f "%s"
        exit 1
    fi
    echo "Backup created: %s"
fi

# 替换程序文件
echo "Installing update..."
echo "Copying %s to %s"
if ! cp -f "%s" "%s"; then
    echo "Installation failed! Restoring backup..."
    if [ -f "%s" ]; then
        cp -f "%s" "%s"
    fi
    rm -f "%s"
    exit 1
fi

echo "Installation completed successfully"

# 设置执行权限
chmod +x "%s"

# 清理临时文件
echo "Cleaning up temporary files..."
rm -f "%s"
if [ -f "%s" ]; then
    rm -f "%s"
fi

# 重启程序
echo "Restarting SCUM Run client: %s"
nohup "%s" %s > /dev/null 2>&1 &

# 删除更新器脚本自己
echo "Deleting updater script..."
rm -f "$0"
`,
			// 占位符参数列表
			exeDir, exeDir, exeDir, // 1-3: 工作目录相关
			config.UpdateURL, tempNewFile, // 4-5: 下载说明
			tempNewFile, config.UpdateURL, // 6-7: curl 下载
			tempNewFile, config.UpdateURL, // 8-9: wget 备用下载
			tempNewFile, tempNewFile, exeDir, // 10-12: 检查文件是否存在
			tempNewFile, tempNewFile, tempNewFile, tempNewFile, // 13-16: 验证文件大小
			config.CurrentExePath, config.CurrentExePath, config.CurrentExePath, backupFile, // 17-20: 备份
			tempNewFile, backupFile, // 21-22: 备份失败清理
			tempNewFile, config.CurrentExePath, tempNewFile, config.CurrentExePath, // 23-26: 复制新文件
			backupFile, backupFile, config.CurrentExePath, tempNewFile, // 27-30: 恢复备份
			config.CurrentExePath,               // 31: chmod +x
			tempNewFile, backupFile, backupFile, // 32-34: 清理临时文件
			config.CurrentExePath, config.CurrentExePath, formatArgs(config.Args)) // 35-37: 重启程序
	}

	// 写入脚本文件 - 确保使用 Windows 风格的换行符
	windowsContent := strings.ReplaceAll(scriptContent, "\n", "\r\n")
	if err := os.WriteFile(scriptName, []byte(windowsContent), 0644); err != nil {
		return fmt.Errorf("failed to create updater script: %w", err)
	}

	return nil
}

// ExecuteUpdate 执行更新流程
func ExecuteUpdate(config model.UpdaterConfig) error {
	fmt.Printf("🔄 开始执行更新流程...\n")
	fmt.Printf("📥 下载URL: %s\n", config.UpdateURL)
	fmt.Printf("📁 当前程序路径: %s\n", config.CurrentExePath)
	fmt.Printf("⚙️ 启动参数: %v\n", config.Args)

	// 1. 创建更新器脚本
	fmt.Printf("📝 创建更新器脚本...\n")
	if err := CreateUpdaterScript(config); err != nil {
		fmt.Printf("❌ 创建更新器脚本失败: %v\n", err)
		return fmt.Errorf("failed to create updater script: %w", err)
	}
	fmt.Printf("✅ 更新器脚本创建成功\n")

	// 2. 启动更新器脚本
	var cmd *exec.Cmd
	var scriptName string
	if runtime.GOOS == "windows" {
		scriptName = "scum_run_updater.bat"
		// 使用 start 命令启动，完全分离进程
		cmd = exec.Command("cmd", "/C", "start", "/B", "", scriptName)
		fmt.Printf("🪟 启动Windows更新器脚本: %s\n", scriptName)
	} else {
		scriptName = "scum_run_updater.sh"
		// 使用 nohup 启动，完全分离进程
		cmd = exec.Command("nohup", "bash", scriptName, "&")
		fmt.Printf("🐧 启动Linux更新器脚本: %s\n", scriptName)
	}

	// 分离进程，让更新器独立运行
	cmd.SysProcAttr = getSysProcAttr()

	fmt.Printf("🚀 启动更新器进程...\n")
	if err := cmd.Start(); err != nil {
		fmt.Printf("❌ 启动更新器失败: %v\n", err)
		return fmt.Errorf("failed to start updater: %w", err)
	}

	// 立即释放进程资源，不等待子进程结束
	cmd.Process.Release()

	fmt.Printf("✅ 更新器进程已启动，PID: %d\n", cmd.Process.Pid)
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
