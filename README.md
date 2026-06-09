# SCUM Run Client

`scum_run` 当前同时保留两种运行模式：

1. 旧兼容模式：连接旧 `scum_robot` WebSocket。（已放弃）
2. 新 host-agent 模式：连接 `scum_server`，通过 Host Agent 注册、心跳和数据库操作轮询执行受控能力。

## Features

- **Automatic Steam Detection**: Supports Windows, Linux, and macOS
- **Process Management**: Safe start/stop/restart of SCUM server
- **Real-time Log Monitoring**: Watches log files and sends updates immediately
- **Database Queries**: Execute SQL queries on the SCUM database
- **Token Authentication**: Secure authentication with the server
- **Heartbeat Monitoring**: Maintains connection health

## Host-Agent Mode

新平台下推荐使用 host-agent 模式，它不再连接旧 `scum_robot` WebSocket，而是直接对接 `scum_server`：

```bash
export SCUM_RUN_MODE=host-agent
export SCUM_HOST_AGENT_SERVER_URL=http://127.0.0.1:18080
export SCUM_HOST_AGENT_REGISTRATION_TOKEN=<token>
export SCUM_HOST_AGENT_ID=scum-run-dev
export SCUM_HOST_AGENT_DISPLAY_NAME="SCUM Run Dev"
export SCUM_HOST_AGENT_VERSION=dev
export SCUM_HOST_AGENT_ADDRESS=127.0.0.1
export SCUM_RUN_DATABASE_PATH=/path/to/SCUM.db

./scum_run
```

如果没有显式设置 `SCUM_RUN_DATABASE_PATH`，也可以提供 `SCUM_RUN_STEAM_DIR` 让 `scum_run` 推导 `SCUM.db` 路径。

当前 host-agent 模式已经闭合以下链路：

- `POST /api/v1/host-agents/hello`
- `POST /api/v1/host-agents/heartbeat`
- `GET /api/v1/host-agents/database-operations/next`
- `POST /api/v1/host-agents/database-operations/{id}/result`

插件来源的 SCUM 数据库请求会通过只读模式执行，并拒绝写入、多语句、事务、schema 变更和其他危险 SQL。

## Installation

1. Clone or copy the `scum_run` directory to your local machine
2. Navigate to the directory:
   ```bash
   cd scum_run
   ```
3. Install dependencies:
   ```bash
   go mod tidy
   ```
4. Build the application:
   ```bash
   go build -o scum_run main.go
   ```

## Legacy WebSocket Configuration

1. Copy the example configuration file:
   ```bash
   cp config.example.json config.json
   ```
2. Edit `config.json` with your settings:
   ```json
   {
     "token": "your_server_token_here",
     "server_addr": "ws://your-server:8080/ws",
     "log_level": "info"
   }
   ```

## Legacy WebSocket Usage

### Basic Usage

```bash
./scum_run
```

### Command Line Options

```bash
./scum_run -token="your_token" -server="ws://localhost:8080/ws" -config="custom_config.json"
```

### Command Line Arguments

- `-token`: Authentication token for the server
- `-server`: WebSocket server address
- `-config`: Path to configuration file (default: config.json)

## Supported WebSocket Commands

The client responds to the following WebSocket message types:

### Server Control
- `server_start`: Start the SCUM server
- `server_stop`: Stop the SCUM server  
- `server_restart`: Restart the SCUM server
- `server_status`: Get server status (running/stopped, PID)

### Database Operations
- `db_query`: Execute a single SQL statement on the SCUM database. Both reads and authorized writes are supported because operator workflows may need to repair user data.
  ```json
  {
    "type": "db_query",
    "data": {
      "query_id": "repair-001",
      "query": "UPDATE users SET name = ? WHERE id = ?",
      "args": ["new-name", 123],
      "timeout_ms": 10000,
      "max_rows": 500,
      "max_bytes": 1048576
    }
  }
  ```

  Read responses include `action`, `columns`, `result`, `truncated`, `truncated_by`, and `duration_ms`.
  Write responses include `action`, `rows_affected`, and `duration_ms`.
  Multi-statement payloads are rejected by default; send one SQL statement per request.

### Log Monitoring
- The client automatically sends `log_update` messages when new log lines are detected:
  ```json
  {
    "type": "log_update",
    "data": {
      "filename": "server.log",
      "lines": ["[2024-01-01 12:00:00] Server started"],
      "timestamp": 1704110400
    }
  }
  ```

## File Paths

The client automatically detects the following paths:

- **SCUM Server Executable**: `{SteamDir}/steamapps/common/SCUM Server/Binaries/Win64/SCUMServer.exe`
- **SCUM Database**: `{SteamDir}/steamapps/common/scum server/scum/saved/SaveFiles/SCUM.db`
- **SCUM Logs Directory**: `{SteamDir}/steamapps/common/scum server/scum/saved/SaveFiles/Logs`

## Requirements

- Go 1.21 or later
- Steam installed with SCUM Server
- SQLite3 support (CGO enabled)
- Network access to the target control plane (`scum_robot` legacy mode or `scum_server` host-agent mode)

## Troubleshooting

### Steam Directory Not Found
If the client cannot detect your Steam directory, you can:
1. Check if Steam is installed in a non-standard location
2. Ensure the Steam directory contains the required files
3. Check the logs for more details

### Database Access Issues
- Ensure the SCUM database file exists
- Check file permissions
- Verify the database is not locked by another process

### WebSocket Connection Issues
- Verify the server address and port
- Check the authentication token
- Ensure the scum_robot server is running
- Check firewall settings

## Logs

The client logs all activities to stdout with different log levels:
- `[DEBUG]`: Detailed debugging information
- `[INFO]`: General information
- `[WARN]`: Warnings that don't prevent operation
- `[ERROR]`: Errors that may affect functionality

## Security

- Use strong authentication tokens
- Ensure secure WebSocket connections (WSS) in production
- Limit database query permissions as needed
- Monitor log output for sensitive information 
