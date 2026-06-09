package hostagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"scum_run/internal/logger"
)

// TestAgentPollsReadOnlyDatabaseOperation verifies that host-agent mode can claim, execute, and report one read-only database operation.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentPollsReadOnlyDatabaseOperation(t *testing.T) {
	databasePath := createTestDatabase(t)
	var (
		report        databaseOperationResultRequest
		helloCalled   bool
		reportArrived = make(chan struct{}, 1)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/hello":
			helloCalled = true
			writeJSONResponse(t, w, helloResponse{SessionToken: "session-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/heartbeat":
			writeJSONResponse(t, w, map[string]any{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/host-agents/database-operations/next":
			if r.Header.Get("Authorization") != "Bearer session-1" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			writeJSONResponse(t, w, databaseOperation{
				ID:         "dbop-1",
				QueryID:    "query-1",
				SQLText:    "SELECT id, name FROM players ORDER BY id ASC",
				ReadOnly:   true,
				TimeoutMS:  1000,
				MaxRows:    10,
				MaxBytes:   4096,
				SQLSummary: "SELECT players",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/database-operations/dbop-1/result":
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Fatalf("decode report: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			select {
			case reportArrived <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := newTestAgent(t, Config{
		ServerURL:         server.URL,
		RegistrationToken: "token",
		AgentID:           "agent-1",
		DisplayName:       "agent-1",
		Version:           "test",
		Address:           "127.0.0.1",
		DatabasePath:      databasePath,
		HeartbeatInterval: 5 * time.Second,
		PollInterval:      20 * time.Millisecond,
		RequestTimeout:    time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx)
	}()

	select {
	case <-reportArrived:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for database report")
	}

	if err := <-done; err != nil {
		t.Fatalf("agent returned error: %v", err)
	}
	if !helloCalled {
		t.Fatal("expected host agent hello to be called")
	}
	if report.Status != "succeeded" {
		t.Fatalf("expected succeeded report status, got %q", report.Status)
	}
	if report.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %d", report.RowCount)
	}
	if report.Rows[0]["name"] != "Alice" {
		t.Fatalf("expected first row to be Alice, got %#v", report.Rows[0])
	}
}

// TestAgentRejectsMutatingDatabaseOperation verifies that write-like SQL is rejected through the read-only execution path.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentRejectsMutatingDatabaseOperation(t *testing.T) {
	databasePath := createTestDatabase(t)
	var report databaseOperationResultRequest
	reportArrived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/hello":
			writeJSONResponse(t, w, helloResponse{SessionToken: "session-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/heartbeat":
			writeJSONResponse(t, w, map[string]any{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/host-agents/database-operations/next":
			writeJSONResponse(t, w, databaseOperation{
				ID:         "dbop-2",
				QueryID:    "query-2",
				SQLText:    "UPDATE players SET name = 'secret-token-value' WHERE id = 1",
				ReadOnly:   true,
				TimeoutMS:  1000,
				MaxRows:    10,
				MaxBytes:   4096,
				SQLSummary: "UPDATE players",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/database-operations/dbop-2/result":
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Fatalf("decode report: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			select {
			case reportArrived <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := newTestAgent(t, Config{
		ServerURL:         server.URL,
		RegistrationToken: "token",
		AgentID:           "agent-1",
		DisplayName:       "agent-1",
		Version:           "test",
		Address:           "127.0.0.1",
		DatabasePath:      databasePath,
		HeartbeatInterval: 5 * time.Second,
		PollInterval:      20 * time.Millisecond,
		RequestTimeout:    time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx)
	}()

	select {
	case <-reportArrived:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for database rejection report")
	}

	if err := <-done; err != nil {
		t.Fatalf("agent returned error: %v", err)
	}
	if report.Status != "failed" {
		t.Fatalf("expected failed report status, got %q", report.Status)
	}
	if report.ErrorCode != "readonly_rejected" {
		t.Fatalf("expected readonly_rejected, got %q", report.ErrorCode)
	}
	if report.ErrorMessage == "" {
		t.Fatal("expected sanitized error message")
	}
	if contains(report.ErrorMessage, databasePath) {
		t.Fatalf("expected redacted error, got %q", report.ErrorMessage)
	}
}

// newTestAgent builds one host-agent instance for tests.
// t is the testing handle and cfg contains the runtime configuration, and the function returns a ready Agent or fails the test when construction fails.
func newTestAgent(t *testing.T, cfg Config) *Agent {
	t.Helper()
	agent, err := New(cfg, logger.New())
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	return agent
}

// createTestDatabase creates a temporary SQLite database with sample player rows.
// t is the testing handle, and the function returns the database file path or fails the test when setup cannot complete.
func createTestDatabase(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	databasePath := filepath.Join(tempDir, "SCUM.db")
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
	return databasePath
}

// writeJSONResponse writes one JSON response for the test server.
// t is the testing handle, w receives the response, body is the JSON payload, and the function returns no values.
func writeJSONResponse(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// contains reports whether one string contains another.
// text is the haystack, fragment is the substring, and the function returns true when fragment appears in text.
func contains(text string, fragment string) bool {
	return strings.Contains(text, fragment)
}
