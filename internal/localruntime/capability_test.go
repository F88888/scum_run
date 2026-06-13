package localruntime

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"scum_run/internal/logger"
	"scum_run/internal/process"
)

// TestLocalRuntimeExecuteProcessLifecycleCapabilities verifies the shared runtime preserves process start, status, restart, and stop behavior.
// t is the testing handle, and the function returns no values while failing the test when lifecycle capabilities drift from the legacy process manager behavior.
func TestLocalRuntimeExecuteProcessLifecycleCapabilities(t *testing.T) {
	steamDir, _, startLog := createLocalRuntimeTestLayout(t, "#!/bin/sh\n"+
		"echo started >> '__START_LOG__'\n"+
		"sleep 30\n")
	runtime := newTestLocalRuntime(t, LocalRuntimeOptions{SteamDir: steamDir})

	if _, err := runtime.ExecuteCapability(CapabilityProcessStart, nil); err != nil {
		t.Fatalf("start process capability: %v", err)
	}
	statusResult, err := runtime.ExecuteCapability(CapabilityProcessStatus, nil)
	if err != nil {
		t.Fatalf("status process capability: %v", err)
	}
	status, ok := statusResult.Data["status"].(process.Status)
	if !ok || !status.Running {
		t.Fatalf("expected running process status, got %#v", statusResult.Data["status"])
	}
	if _, err := runtime.ExecuteCapability(CapabilityProcessRestart, nil); err != nil {
		t.Fatalf("restart process capability: %v", err)
	}
	if _, err := runtime.ExecuteCapability(CapabilityProcessStop, nil); err != nil {
		t.Fatalf("stop process capability: %v", err)
	}
	content, err := os.ReadFile(startLog)
	if err != nil {
		t.Fatalf("read start log: %v", err)
	}
	if strings.Count(string(content), "started") < 2 {
		t.Fatalf("expected restart to relaunch process, got %q", string(content))
	}
}

// TestLocalRuntimeExecuteDatabaseCapabilities verifies the shared runtime preserves bounded database read and write execution behavior.
// t is the testing handle, and the function returns no values while failing the test when database capability execution no longer matches the local database client behavior.
func TestLocalRuntimeExecuteDatabaseCapabilities(t *testing.T) {
	steamDir, _, _ := createLocalRuntimeTestLayout(t, "#!/bin/sh\nsleep 30\n")
	runtime := newTestLocalRuntime(t, LocalRuntimeOptions{SteamDir: steamDir})

	readResult, err := runtime.ExecuteCapability(CapabilityDatabaseQueryReadOnly, map[string]any{
		"query": "SELECT name FROM players ORDER BY id ASC",
	})
	if err != nil {
		t.Fatalf("readonly database capability: %v", err)
	}
	rows, ok := readResult.Data["rows"].([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %#v", readResult.Data["rows"])
	}
	if rows[0]["name"] != "Alice" {
		t.Fatalf("expected first row Alice, got %#v", rows[0])
	}

	writeResult, err := runtime.ExecuteCapability(CapabilityDatabaseQuery, map[string]any{
		"query": "UPDATE players SET name = 'Carol' WHERE id = 2",
	})
	if err != nil {
		t.Fatalf("write database capability: %v", err)
	}
	if rowsAffected, _ := writeResult.Data["rowsAffected"].(int64); rowsAffected != 1 {
		t.Fatalf("expected one updated row, got %#v", writeResult.Data["rowsAffected"])
	}
	verifyResult, err := runtime.ExecuteCapability(CapabilityDatabaseQueryReadOnly, map[string]any{
		"query": "SELECT name FROM players WHERE id = 2",
	})
	if err != nil {
		t.Fatalf("verify database capability: %v", err)
	}
	verifyRows, ok := verifyResult.Data["rows"].([]map[string]any)
	if !ok || len(verifyRows) != 1 || verifyRows[0]["name"] != "Carol" {
		t.Fatalf("expected updated player row, got %#v", verifyResult.Data["rows"])
	}
}

// TestLocalRuntimeResolvesStandardSteamRoot verifies Steam-root layouts resolve under steamapps/common/SCUM Server.
// t is the testing handle, and the function returns no values while failing the test when standard Steam installs are mapped to direct-install paths.
func TestLocalRuntimeResolvesStandardSteamRoot(t *testing.T) {
	steamRoot, databasePath, _ := createStandardSteamRootTestLayout(t, "#!/bin/sh\nsleep 30\n")
	runtime := newTestLocalRuntime(t, LocalRuntimeOptions{SteamDir: steamRoot})

	if runtime.DatabasePath() != databasePath {
		t.Fatalf("expected database path %q, got %q", databasePath, runtime.DatabasePath())
	}
	if readiness := runtime.ProcessReadiness(); !readiness.Ready {
		t.Fatalf("expected process readiness for standard Steam root, got reason=%s summary=%s", readiness.ReasonCode, readiness.Summary)
	}
}

// TestLocalRuntimePrefersExplicitScopeRoot verifies managed executors use the configured server root instead of Steam detection.
// t is the testing handle, and the function returns no values while failing the test when explicit scope roots are ignored.
func TestLocalRuntimePrefersExplicitScopeRoot(t *testing.T) {
	serverRoot, databasePath, _ := createLocalRuntimeTestLayout(t, "#!/bin/sh\nsleep 30\n")
	runtime := newTestLocalRuntime(t, LocalRuntimeOptions{ScopeRoot: serverRoot})

	if runtime.DatabasePath() != databasePath {
		t.Fatalf("expected database path %q, got %q", databasePath, runtime.DatabasePath())
	}
	if readiness := runtime.ProcessReadiness(); !readiness.Ready {
		t.Fatalf("expected process readiness for explicit scope root, got reason=%s summary=%s", readiness.ReasonCode, readiness.Summary)
	}
}

// TestLocalRuntimeRejectsMalformedWindowsScopeRoot verifies invalid logical refs fail before SQLite initialization.
// t is the testing handle, and the function returns no values while failing when malformed scope roots are accepted.
func TestLocalRuntimeRejectsMalformedWindowsScopeRoot(t *testing.T) {
	_, err := New(LocalRuntimeOptions{ScopeRoot: `manual:\scum-run-manual`}, logger.New())
	if err == nil || !strings.Contains(err.Error(), "SCUM_RUN_SCOPE_ROOT") {
		t.Fatalf("expected malformed scope root error, got %v", err)
	}
}

// newTestLocalRuntime constructs one shared runtime for tests.
// t is the testing handle and options describes the local Steam/database layout, and the function returns a ready runtime or fails the test when initialization fails.
func newTestLocalRuntime(t *testing.T, options LocalRuntimeOptions) *LocalRuntime {
	t.Helper()
	runtime, err := New(options, logger.New())
	if err != nil {
		t.Fatalf("new local runtime: %v", err)
	}
	return runtime
}

// createLocalRuntimeTestLayout creates a fake Steam layout with a SQLite database and executable script.
// t is the testing handle and script is written as the fake SCUMServer executable, and the function returns the Steam root, database path, and script log path.
func createLocalRuntimeTestLayout(t *testing.T, script string) (string, string, string) {
	t.Helper()
	steamDir := t.TempDir()
	databasePath := filepath.Join(steamDir, "SCUM", "Saved", "SaveFiles", "SCUM.db")
	serverPath := filepath.Join(steamDir, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	startLog := filepath.Join(filepath.Dir(serverPath), "process-start.log")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatalf("create database dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(serverPath), 0o755); err != nil {
		t.Fatalf("create server dir: %v", err)
	}
	db, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE players (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`,
		`INSERT INTO players (name) VALUES ('Alice');`,
		`INSERT INTO players (name) VALUES ('Bob');`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed sqlite db: %v", err)
		}
	}
	if err := os.WriteFile(serverPath, []byte(strings.ReplaceAll(script, "__START_LOG__", filepath.ToSlash(startLog))), 0o755); err != nil {
		t.Fatalf("write fake server executable: %v", err)
	}
	return steamDir, databasePath, startLog
}

// createStandardSteamRootTestLayout creates a fake Steam root with SCUM Server installed under steamapps/common.
// t is the testing handle and script is written as the fake SCUMServer executable, and the function returns the Steam root, database path, and script log path.
func createStandardSteamRootTestLayout(t *testing.T, script string) (string, string, string) {
	t.Helper()
	steamRoot := t.TempDir()
	serverRoot := filepath.Join(steamRoot, "steamapps", "common", "SCUM Server")
	databasePath := filepath.Join(serverRoot, "SCUM", "Saved", "SaveFiles", "SCUM.db")
	serverPath := filepath.Join(serverRoot, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
	startLog := filepath.Join(filepath.Dir(serverPath), "process-start.log")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatalf("create database dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(serverPath), 0o755); err != nil {
		t.Fatalf("create server dir: %v", err)
	}
	db, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE players (id INTEGER PRIMARY KEY, name TEXT NOT NULL);`); err != nil {
		t.Fatalf("seed sqlite db: %v", err)
	}
	if err := os.WriteFile(serverPath, []byte(strings.ReplaceAll(script, "__START_LOG__", filepath.ToSlash(startLog))), 0o755); err != nil {
		t.Fatalf("write fake server executable: %v", err)
	}
	return steamRoot, databasePath, startLog
}
