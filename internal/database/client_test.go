package database

import (
	"os"
	"path/filepath"
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