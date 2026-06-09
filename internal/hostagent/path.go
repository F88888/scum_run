package hostagent

import "path/filepath"

// databasePathFromSteamDir builds the SCUM.db path from a Steam root directory.
// steamDir is the Steam installation directory, and the function returns the derived SCUM database file path.
func databasePathFromSteamDir(steamDir string) string {
	return filepath.Join(steamDir, "steamapps", "common", "scum server", "SCUM", "Saved", "SaveFiles", "SCUM.db")
}
