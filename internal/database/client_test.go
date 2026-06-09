package database

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scum_run/internal/logger"
)

func TestDatabaseInitialization(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "scum_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test database path
	dbPath := filepath.Join(tmpDir, "test.db")
	if _, err := os.Create(dbPath); err != nil {
		t.Fatalf("Failed to create test database file: %v", err)
	}

	// Create logger
	logger := logger.New()

	// Create database client
	client := New(dbPath, logger)

	// Test initialization
	err = client.Initialize()
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}

	// Verify database connection is active
	if client.db == nil {
		t.Fatal("Database connection is nil after initialization")
	}

	// Test connection by pinging
	err = client.db.Ping()
	if err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	// Test WAL mode query
	var journalMode string
	err = client.db.QueryRow("PRAGMA journal_mode;").Scan(&journalMode)
	if err != nil {
		t.Fatalf("Failed to query journal mode: %v", err)
	}

	if journalMode != "wal" {
		t.Fatalf("Expected journal mode to be 'wal', got '%s'", journalMode)
	}

	// Test double initialization (should not fail)
	err = client.Initialize()
	if err != nil {
		t.Fatalf("Failed to initialize database second time: %v", err)
	}

	// Test close
	err = client.Close()
	if err != nil {
		t.Fatalf("Failed to close database: %v", err)
	}

	// Verify connection is closed
	if client.db != nil {
		t.Fatal("Database connection should be nil after close")
	}
}

func TestDatabaseQuery(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "scum_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test database path
	dbPath := filepath.Join(tmpDir, "test.db")
	if _, err := os.Create(dbPath); err != nil {
		t.Fatalf("Failed to create test database file: %v", err)
	}

	// Create logger
	logger := logger.New()

	// Create database client
	client := New(dbPath, logger)

	// Initialize database
	err = client.Initialize()
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}
	defer client.Close()

	// Create test table
	_, err = client.Execute("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Insert test data
	_, err = client.Execute("INSERT INTO test (name) VALUES ('test1'), ('test2')")
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Query test data
	results, err := client.Query("SELECT id, name FROM test ORDER BY id")
	if err != nil {
		t.Fatalf("Failed to query test data: %v", err)
	}

	// Verify results
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0]["name"] != "test1" {
		t.Fatalf("Expected first name to be 'test1', got '%v'", results[0]["name"])
	}

	if results[1]["name"] != "test2" {
		t.Fatalf("Expected second name to be 'test2', got '%v'", results[1]["name"])
	}
}

func TestClassifySQLAllowsReadAndWrite(t *testing.T) {
	tests := map[string]string{
		"SELECT * FROM test":                         SQLActionRead,
		"WITH rows AS (SELECT 1) SELECT * FROM rows": SQLActionRead,
		"UPDATE users SET name = ? WHERE id = ?":     SQLActionWrite,
		"DELETE FROM users WHERE id = ?":             SQLActionWrite,
		"CREATE TABLE x (id INTEGER)":                SQLActionSchema,
	}
	for query, expected := range tests {
		action, _, err := ClassifySQL(query)
		if err != nil {
			t.Fatalf("classify %q failed: %v", query, err)
		}
		if action != expected {
			t.Fatalf("classify %q expected %s, got %s", query, expected, action)
		}
	}
}

func TestClassifySQLRejectsMultipleStatements(t *testing.T) {
	if _, _, err := ClassifySQL("SELECT 1; UPDATE users SET name = 'bad'"); err == nil {
		t.Fatal("expected multiple statements to be rejected")
	}
}

func TestExecuteCapabilitySupportsWriteAndParameterizedRead(t *testing.T) {
	client := newTestClient(t)
	defer client.Close()

	if _, err := client.ExecuteCapability("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)", QueryOptions{}); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	writeResult, err := client.ExecuteCapability("INSERT INTO users (name) VALUES (?)", QueryOptions{Args: []interface{}{"alice"}, QueryID: "write-1"})
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if writeResult.Action != SQLActionWrite || writeResult.RowsAffected != 1 || writeResult.QueryID != "write-1" {
		t.Fatalf("unexpected write result: %+v", writeResult)
	}
	readResult, err := client.ExecuteCapability("SELECT id, name FROM users WHERE name = ?", QueryOptions{Args: []interface{}{"alice"}, QueryID: "read-1"})
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	if readResult.Action != SQLActionRead || len(readResult.Rows) != 1 || readResult.Rows[0]["name"] != "alice" {
		t.Fatalf("unexpected read result: %+v", readResult)
	}
}

func TestExecuteReadOnlyCapabilityRejectsWrite(t *testing.T) {
	client := newTestClient(t)
	defer client.Close()

	if _, err := client.ExecuteCapability("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)", QueryOptions{}); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	if _, err := client.ExecuteReadOnlyCapability("INSERT INTO users (name) VALUES ('alice')", QueryOptions{}); err == nil {
		t.Fatal("expected read-only capability to reject write SQL")
	}
}

func TestExecuteReadOnlyCapabilityRejectsUnsafeSQL(t *testing.T) {
	client := newTestClient(t)
	defer client.Close()

	for _, query := range []string{
		"SELECT 1; SELECT 2",
		"PRAGMA writable_schema = 1",
		"ATTACH DATABASE '/tmp/SCUM.db' AS leaked",
	} {
		if _, err := client.ExecuteReadOnlyCapability(query, QueryOptions{}); err == nil {
			t.Fatalf("expected read-only capability to reject %q", query)
		}
	}
}

func TestExecuteReadOnlyCapabilityReturnsRows(t *testing.T) {
	client := newTestClient(t)
	defer client.Close()

	if _, err := client.ExecuteCapability("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)", QueryOptions{}); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	if _, err := client.ExecuteCapability("INSERT INTO users (name) VALUES ('alice')", QueryOptions{}); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	result, err := client.ExecuteReadOnlyCapability("SELECT id, name FROM users", QueryOptions{QueryID: "read-only-1"})
	if err != nil {
		t.Fatalf("read-only query failed: %v", err)
	}
	if result.Action != SQLActionRead || result.QueryID != "read-only-1" || len(result.Rows) != 1 {
		t.Fatalf("unexpected read-only result: %+v", result)
	}
}

func TestExecuteReadOnlyCapabilityShapesLockedError(t *testing.T) {
	message := SanitizeError(errors.New("database is locked at /tmp/scum/SCUM.db password=abc"))
	if message == "" || strings.Contains(message, "/tmp/scum") || strings.Contains(message, "SCUM.db") || strings.Contains(message, "abc") {
		t.Fatalf("expected sanitized locked error, got %q", message)
	}
}

func TestExecuteCapabilityTruncatesRows(t *testing.T) {
	client := newTestClient(t)
	defer client.Close()

	if _, err := client.ExecuteCapability("CREATE TABLE rows_test (id INTEGER PRIMARY KEY)", QueryOptions{}); err != nil {
		t.Fatalf("create table failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := client.ExecuteCapability("INSERT INTO rows_test DEFAULT VALUES", QueryOptions{}); err != nil {
			t.Fatalf("insert row %d failed: %v", i, err)
		}
	}
	result, err := client.ExecuteCapability("SELECT id FROM rows_test ORDER BY id", QueryOptions{MaxRows: 2})
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	if !result.Truncated || result.TruncatedBy != "rows" || len(result.Rows) != 2 {
		t.Fatalf("expected row truncation, got %+v", result)
	}
}

func TestSanitizeErrorRedactsSecretsAndPaths(t *testing.T) {
	message := SanitizeError(errors.New("failed password=abc at /tmp/SCUM.db"))
	if message == "" || message == "failed password=abc at /tmp/SCUM.db" {
		t.Fatalf("expected sanitized error, got %q", message)
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "scum_test")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	client := New(filepath.Join(tmpDir, "test.db"), logger.New())
	if _, err := os.Create(client.dbPath); err != nil {
		t.Fatalf("create test db file: %v", err)
	}
	if err := client.Initialize(); err != nil {
		t.Fatalf("initialize test db: %v", err)
	}
	return client
}
