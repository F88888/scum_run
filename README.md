# SCUM Run Client

The SCUM Run client is a Go application that connects to the scum_robot server and provides the following functionality:

1. **Steam Directory Detection**: Automatically detects the Steam installation directory
2. **SCUM Server Management**: Starts, stops, and restarts the SCUM server executable
3. **Database Access**: Reads from the SCUM SQLite database (SCUM.db)
4. **Log Monitoring**: Monitors SCUM server log files and pushes new lines to the server
5. **WebSocket Communication**: Maintains a connection with the scum_robot server for remote control

## Features

- **Automatic Steam Detection**: Supports Windows, Linux, and macOS
- **Process Management**: Safe start/stop/restart of SCUM server
- **Real-time Log Monitoring**: Watches log files and sends updates immediately
- **Database Queries**: Execute SQL queries on the SCUM database
- **Token Authentication**: Secure authentication with the server
- **Heartbeat Monitoring**: Maintains connection health

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

## Configuration

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

## Usage

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
- `db_query`: Execute SQL queries on the SCUM database
  ```json
  {
    "type": "db_query",
    "data": {
      "query": "SELECT * FROM ScumUser LIMIT 10"
    }
  }
  ```

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
- Network access to the scum_robot server

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