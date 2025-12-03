# SCUM Run - Steam++ 集成功能

## 功能概述

scum_run 现已集成 Steam++ (Watt Toolkit) 网络加速功能，可以自动为 SCUM 服务器和 steamCMD 提供网络加速服务，解决国内访问 Steam 服务时的连接超时和速度慢的问题。

## 什么是 Steam++？

Steam++ (现名 Watt Toolkit) 是一个开源的多功能 Steam 工具箱，主要功能包括：

- **网络加速**：通过本地反代技术，让国内用户能够正常访问 Steam 商店、社区、CDN 等服务
- **多平台支持**：支持 Windows、Linux、macOS 等多个平台
- **开源免费**：基于 GPL-3.0 许可证的开源项目

官方项目：[BeyondDimension/SteamTools](https://github.com/BeyondDimension/SteamTools)

## 集成优势

### 1. 自动化管理
- **无需手动启动**：scum_run 会自动检测并启动 Steam++
- **生命周期管理**：与 scum_run 同时启动和停止
- **状态监控**：实时监控 Steam++ 运行状态

### 2. 智能检测
- **自动路径检测**：自动检测 Steam++ 安装路径
- **多版本支持**：支持新旧版本的 Steam++
- **配置灵活**：支持手动指定路径和参数

### 3. 容错设计
- **启动失败不影响主程序**：Steam++ 启动失败不会导致 scum_run 退出
- **优雅降级**：网络加速不可用时程序正常运行
- **错误恢复**：支持重试和恢复机制

## 配置说明

### 配置文件结构

```json
{
  "steam_tools": {
    "enabled": true,                    // 是否启用 Steam++ 集成
    "executable_path": "",              // Steam++ 可执行文件路径（留空自动检测）
    "auto_start": true,                 // 是否自动启动 Steam++
    "auto_accelerate": true,            // 是否自动启用网络加速
    "wait_timeout": 30,                 // 等待 Steam++ 启动的超时时间（秒）
    "accelerate_items": [               // 要加速的项目列表
      "Steam",
      "SteamCommunity",
      "SteamStore",
      "SteamCDN"
    ]
  }
}
```

### 配置项详解

#### `enabled` (布尔值)
- **作用**：控制是否启用 Steam++ 集成功能
- **默认值**：`true`
- **说明**：设置为 `false` 可完全禁用 Steam++ 功能

#### `executable_path` (字符串)
- **作用**：指定 Steam++ 可执行文件的完整路径
- **默认值**：`""` (空字符串，自动检测)
- **说明**：
  - 留空时程序会自动检测常见安装路径
  - 可手动指定路径，如：`"C:\\Steam++\\Steam++.exe"`

#### `auto_start` (布尔值)
- **作用**：控制是否随 scum_run 自动启动 Steam++
- **默认值**：`true`
- **说明**：设置为 `false` 需要手动启动 Steam++

#### `auto_accelerate` (布尔值)
- **作用**：控制是否自动启用网络加速
- **默认值**：`true`
- **说明**：由于 Steam++ 限制，目前需要手动在 GUI 中启用加速

#### `wait_timeout` (整数)
- **作用**：等待 Steam++ 启动完成的超时时间
- **默认值**：`30` (秒)
- **说明**：如果 Steam++ 启动较慢，可以适当增加此值

#### `accelerate_items` (字符串数组)
- **作用**：指定要加速的 Steam 服务项目
- **默认值**：`["Steam", "SteamCommunity", "SteamStore", "SteamCDN"]`
- **可选项目**：
  - `Steam`: Steam 客户端基础服务
  - `SteamCommunity`: Steam 社区服务
  - `SteamStore`: Steam 商店服务
  - `SteamCDN`: Steam 内容分发网络

#### `auto_download` (布尔值) **新功能**
- **作用**：控制是否在未找到 Steam++ 时自动下载安装
- **默认值**：`true`
- **说明**：启用后，程序会自动从指定URL下载并安装 Steam++

#### `download_url` (字符串) **新功能**
- **作用**：指定 Steam++ 的下载地址
- **默认值**：`"https://www.npc0.com/Steam++_v3.0.0-rc.16_win_x64.exe"`
- **说明**：确保URL指向可信任的 Steam++ 官方版本

#### `install_path` (字符串) **新功能**
- **作用**：指定 Steam++ 的安装路径
- **默认值**：`""` (空字符串，使用默认路径)
- **说明**：留空时使用 `%LOCALAPPDATA%\Steam++` 作为安装目录

#### `verify_checksum` (布尔值) **新功能**
- **作用**：控制是否验证下载文件的校验和
- **默认值**：`true`
- **说明**：强烈建议启用以确保文件完整性和安全性

#### `expected_checksum` (字符串) **新功能**
- **作用**：期望的文件SHA256校验和
- **默认值**：`"C3754C0913F69AD89C04292E3388A72C40967D4F7123AC3A58512FF65FFD26C0"`
- **说明**：用于验证下载文件的完整性，防止文件损坏或被篡改

## 自动检测路径

程序会按以下顺序检测 Steam++ 安装路径：

### Windows 系统
1. `%LOCALAPPDATA%\Steam++\Steam++.exe`
2. `%PROGRAMFILES%\Steam++\Steam++.exe`
3. `%PROGRAMFILES(X86)%\Steam++\Steam++.exe`
4. `%LOCALAPPDATA%\SteamTools\Steam++.exe` (旧版本)
5. `%PROGRAMFILES%\SteamTools\Steam++.exe` (旧版本)
6. `%PROGRAMFILES(X86)%\SteamTools\Steam++.exe` (旧版本)
7. `./Steam++.exe` (相对路径)
8. `../Steam++/Steam++.exe` (相对路径)

### Linux 系统
- 相关路径的 Linux 版本可执行文件

### macOS 系统
- 相关路径的 macOS 版本可执行文件

## 使用方法

### 方法一：自动下载安装 (推荐) **新功能**

如果您的系统中没有安装 Steam++，程序会自动为您下载并安装：

1. **启用自动下载**（默认已启用）
   ```json
   {
     "steam_tools": {
       "enabled": true,
       "auto_download": true,
       "download_url": "https://scum.npc0.com/Steam++_v3.0.0-rc.16_win_x64.exe"
     }
   }
   ```

2. **运行 scum_run**
   ```bash
   ./scum_run.exe
   ```

3. **自动下载过程**
   - 程序检测到未安装 Steam++
   - 自动从配置的URL下载最新版本
   - 验证文件完整性（SHA256校验）
   - 安装到 `%LOCALAPPDATA%\Steam++` 目录
   - 自动启动并配置加速服务

4. **查看下载进度**
   ```
   [INFO] 尝试自动下载并安装 Steam++...
   [INFO] 开始下载 Steam++ 从: https://scum.npc0.com/Steam++_v3.0.0-rc.16_win_x64.exe
   [INFO] 下载进度: 25.3% (2048000/8192000 字节)
   [INFO] 下载进度: 50.1% (4096000/8192000 字节)
   [INFO] 正在验证文件校验和...
   [INFO] 文件校验通过
   [INFO] Steam++ 安装完成: C:\Users\用户名\AppData\Local\Steam++\Steam++.exe
   ```

### 方法二：手动安装

如果您希望手动安装 Steam++：

1. **下载 Steam++**
   - 访问：[Steam++ Releases](https://github.com/BeyondDimension/SteamTools/releases)
   - 下载最新稳定版本
   - 安装到默认位置

2. **配置 scum_run**
   ```json
   {
     "steam_tools": {
       "enabled": true,
       "auto_download": false,
       "executable_path": "C:\\Steam++\\Steam++.exe"
     }
   }
   ```

3. **启动程序**
   - 运行 scum_run
   - 程序会自动检测并启动 Steam++

## 状态监控

### WebSocket 消息

scum_run 支持通过 WebSocket 查询 Steam++ 状态：

#### 请求消息
```json
{
  "type": "steamtools_status"
}
```

#### 响应消息
```json
{
  "type": "steamtools_status",
  "data": {
    "enabled": true,
    "running": true,
    "auto_start": true,
    "auto_accelerate": true,
    "pid": 12345,
    "executable_path": "C:\\Steam++\\Steam++.exe"
  },
  "success": true
}
```

### 心跳消息

scum_run 会在心跳消息中包含 Steam++ 状态信息：

```json
{
  "type": "heartbeat",
  "data": {
    "timestamp": 1640995200,
    "steamtools_status": {
      "enabled": true,
      "running": true
    }
  },
  "success": true
}
```

## 日志信息

### 启动日志
```
[INFO] 正在启动 Steam++ 网络加速...
[INFO] 检测到 Steam++ 安装路径: C:\Steam++\Steam++.exe
[INFO] Steam++ 已启动，PID: 12345
[INFO] Steam++ 启动成功，网络加速已启用
```

### 错误日志
```
[WARN] Steam++ 启动失败，继续运行但可能影响 Steam 服务访问: 错误详情
[WARN] 未能自动检测到 Steam++ 安装路径
[ERROR] 启动 Steam++ 失败: 错误详情
```

## 故障排除

### 1. Steam++ 检测失败

**问题**：程序提示"未找到 Steam++ 可执行文件"

**解决方案**：
- 确认已正确安装 Steam++
- 启用自动下载功能：`"auto_download": true`
- 在配置中手动指定 `executable_path`
- 检查文件权限

### 2. 自动下载失败 **新功能**

**问题**：自动下载 Steam++ 失败

**解决方案**：
- 检查网络连接是否正常
- 确认下载URL是否可访问
- 检查磁盘空间是否足够
- 查看详细错误日志
- 尝试手动下载：
  ```bash
  curl -o Steam++.exe https://scum.npc0.com/Steam++_v3.0.0-rc.16_win_x64.exe
  ```

### 3. 文件校验失败 **新功能**

**问题**：下载的文件校验和不匹配

**解决方案**：
- 重新下载文件
- 检查网络连接稳定性
- 暂时禁用校验：`"verify_checksum": false`
- 更新预期校验和值
- 确认下载源的可信性

### 4. 安装权限问题 **新功能**

**问题**：无权限创建安装目录或文件

**解决方案**：
- 以管理员权限运行 scum_run
- 指定有权限的安装路径：
  ```json
  {
    "steam_tools": {
      "install_path": "D:\\MyApps\\Steam++"
    }
  }
  ```
- 检查目标目录的权限设置

### 5. Steam++ 启动失败

**问题**：Steam++ 进程无法启动

**解决方案**：
- 检查 Steam++ 是否已在运行
- 确认有足够的系统权限
- 查看详细错误日志
- 手动运行 Steam++ 测试

### 6. 网络加速无效

**问题**：Steam++ 已启动但网络访问仍然有问题

**解决方案**：
- 手动在 Steam++ GUI 中启用网络加速
- 检查代理设置是否正确
- 确认防火墙未阻止 Steam++
- 重启网络加速服务

### 7. 进程残留

**问题**：程序退出后 Steam++ 进程仍在运行

**解决方案**：
- 检查日志中的停止信息
- 手动结束 Steam++ 进程：
  ```cmd
  taskkill /F /IM Steam++.exe
  ```
- 检查是否有其他程序在使用 Steam++

## 最佳实践

### 1. 配置建议

```json
{
  "steam_tools": {
    "enabled": true,
    "auto_start": true,
    "auto_accelerate": false,  // 建议手动启用以确保正确配置
    "wait_timeout": 45        // 对于性能较低的机器，适当增加等待时间
  }
}
```

### 2. 使用建议

- **首次使用**：建议先手动运行 Steam++ 确认功能正常
- **配置检查**：定期检查 Steam++ 的加速配置是否正确
- **日志监控**：关注 scum_run 日志中的 Steam++ 相关信息
- **版本更新**：定期更新 Steam++ 到最新版本

### 3. 性能优化

- **内存占用**：Steam++ 会占用一定内存，低配置机器需要注意
- **网络监控**：监控网络连接数，避免过多并发连接
- **防病毒软件**：将 Steam++ 添加到杀毒软件白名单

## 技术实现

### 核心组件

1. **steamtools.Manager**：Steam++ 生命周期管理
2. **自动检测**：多路径检测算法
3. **进程管理**：安全的进程启动和停止
4. **状态监控**：实时状态查询和上报

### 架构设计

```
scum_run
├── config (配置管理)
├── internal/steamtools (Steam++ 管理器)
├── internal/client (客户端集成)
└── WebSocket API (状态查询接口)
```

### 依赖关系

- **Windows API**：进程检测和管理
- **文件系统**：路径检测和验证
- **网络通信**：状态上报和心跳

## 更新日志

### v1.0.0
- 初始版本，支持基础 Steam++ 集成
- 自动检测和启动功能
- WebSocket 状态查询API

### 未来规划

- **远程控制**：支持远程启停 Steam++ 加速
- **配置同步**：自动同步加速配置
- **性能监控**：网络加速效果监控
- **多实例支持**：支持多个 Steam++ 实例管理

## 许可证

本集成功能遵循 scum_run 项目的许可证。Steam++ 本身遵循 GPL-3.0 许可证。

## 支持与反馈

如果您在使用 Steam++ 集成功能时遇到问题，请：

1. 查看日志文件获取详细错误信息
2. 确认 Steam++ 版本兼容性
3. 检查网络环境和防火墙设置
4. 提交 Issue 并附上相关日志 