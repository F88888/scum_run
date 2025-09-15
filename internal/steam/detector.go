package steam

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"scum_run/internal/logger"
)

type Detector struct {
	logger *logger.Logger
}

func NewDetector(logger *logger.Logger) *Detector {
	return &Detector{
		logger: logger,
	}
}

// DetectSteamDirectory attempts to find the Steam installation directory
func (d *Detector) DetectSteamDirectory() string {
	d.logger.Info("Detecting Steam installation directory...")

	var commonPaths []string

	switch runtime.GOOS {
	case "windows":
		commonPaths = []string{
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Steam"),
			filepath.Join(os.Getenv("PROGRAMFILES"), "Steam"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Steam"),
			`C:\Program Files (x86)\Steam`,
			`C:\Program Files\Steam`,
			`D:\Steam`,
			`E:\Steam`,
		}
	case "linux":
		homeDir, _ := os.UserHomeDir()
		commonPaths = []string{
			filepath.Join(homeDir, ".steam", "steam"),
			filepath.Join(homeDir, ".local", "share", "Steam"),
			"/usr/games/steam",
			"/opt/steam",
		}
	case "darwin":
		homeDir, _ := os.UserHomeDir()
		commonPaths = []string{
			filepath.Join(homeDir, "Library", "Application Support", "Steam"),
			"/Applications/Steam.app/Contents/MacOS",
		}
	}

	for _, path := range commonPaths {
		if d.isValidSteamDirectory(path) {
			d.logger.Info("Steam directory found: %s", path)
			return path
		}
	}

	d.logger.Warn("Steam directory not found in common locations")
	return ""
}

// isValidSteamDirectory checks if the given path is a valid Steam directory
func (d *Detector) isValidSteamDirectory(path string) bool {
	// Check if the directory exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}

	// Check for Steam executable
	var steamExe string
	switch runtime.GOOS {
	case "windows":
		steamExe = "steam.exe"
	default:
		steamExe = "steam"
	}

	steamExePath := filepath.Join(path, steamExe)
	if _, err := os.Stat(steamExePath); os.IsNotExist(err) {
		return false
	}

	// Check for steamapps directory
	steamappsPath := filepath.Join(path, "steamapps")
	if _, err := os.Stat(steamappsPath); os.IsNotExist(err) {
		return false
	}

	return true
}

// GetSCUMServerPath returns the path to the SCUM Server executable
func (d *Detector) GetSCUMServerPath(steamDir string) string {
	// Try direct installation path first (for SteamCmd installations)
	directPath := filepath.Join(steamDir, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	if _, err := os.Stat(directPath); err == nil {
		return directPath
	}

	// Fall back to standard Steam installation path
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Binaries", "Win64", "SCUMServer.exe")
}

// GetSCUMDatabasePath returns the path to the SCUM database
func (d *Detector) GetSCUMDatabasePath(steamDir string) string {
	// Try direct installation path first (for SteamCmd installations)
	directPath := filepath.Join(steamDir, "SCUM", "Saved", "SaveFiles", "SCUM.db")
	if _, err := os.Stat(filepath.Dir(directPath)); err == nil {
		return directPath
	}

	// Fall back to standard Steam installation path
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Saved", "SaveFiles", "SCUM.db")
}

// GetSCUMLogsPath returns the path to the SCUM logs directory
func (d *Detector) GetSCUMLogsPath(steamDir string) string {
	// Try direct installation path first (for SteamCmd installations)
	directPath := filepath.Join(steamDir, "SCUM", "Saved", "Logs")
	if _, err := os.Stat(directPath); err == nil {
		return directPath
	}

	// Fall back to standard Steam installation path
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Saved", "Logs")
}

// IsSCUMServerInstalled checks if SCUM Dedicated Server is installed
func (d *Detector) IsSCUMServerInstalled(steamDir string) bool {
	serverPath := d.GetSCUMServerPath(steamDir)
	if _, err := os.Stat(serverPath); os.IsNotExist(err) {
		d.logger.Debug("SCUM Server executable not found: %s", serverPath)
		return false
	}

	// Determine the server directory based on installation path
	var serverDir string
	if strings.Contains(serverPath, "steamapps") {
		// Standard Steam installation
		serverDir = filepath.Dir(filepath.Dir(filepath.Dir(serverPath))) // Go up to SCUM Dedicated Server directory
	} else {
		// Direct SteamCmd installation
		serverDir = filepath.Dir(filepath.Dir(filepath.Dir(serverPath))) // Go up to steamDir/SCUM directory
	}

	if _, err := os.Stat(serverDir); os.IsNotExist(err) {
		d.logger.Debug("SCUM server directory not found: %s", serverDir)
		return false
	}

	d.logger.Debug("SCUM Dedicated Server is installed at: %s", serverDir)
	return true
}

// IsSCUMDatabaseAvailable checks if SCUM database file exists
func (d *Detector) IsSCUMDatabaseAvailable(steamDir string) bool {
	dbPath := d.GetSCUMDatabasePath(steamDir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		d.logger.Debug("SCUM database not found: %s", dbPath)
		return false
	}

	d.logger.Debug("SCUM database found: %s", dbPath)
	return true
}

// IsSCUMLogsDirectoryAvailable checks if SCUM logs directory exists
func (d *Detector) IsSCUMLogsDirectoryAvailable(steamDir string) bool {
	logsPath := d.GetSCUMLogsPath(steamDir)
	if _, err := os.Stat(logsPath); os.IsNotExist(err) {
		d.logger.Debug("SCUM logs directory not found: %s", logsPath)
		return false
	}

	d.logger.Debug("SCUM logs directory found: %s", logsPath)
	return true
}
