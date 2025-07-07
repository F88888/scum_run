package steam

import (
	"os"
	"path/filepath"
	"runtime"

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
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Binaries", "Win64", "SCUMServer.exe")
}

// GetSCUMDatabasePath returns the path to the SCUM database
func (d *Detector) GetSCUMDatabasePath(steamDir string) string {
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Saved", "SaveGames", "SCUM.db")
}

// GetSCUMLogsPath returns the path to the SCUM logs directory
func (d *Detector) GetSCUMLogsPath(steamDir string) string {
	return filepath.Join(steamDir, "steamapps", "common", "SCUM Dedicated Server", "SCUM", "Saved", "Logs")
}
