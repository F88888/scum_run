package hostagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"scum_run/internal/localruntime"
	"scum_run/internal/logger"
	"scum_run/internal/process"
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

// TestAgentHelloDeclaresManagedProcessLifecycleCapabilities verifies managed executors advertise process lifecycle capability and startup behavior metadata during hello.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentHelloDeclaresManagedProcessLifecycleCapabilities(t *testing.T) {
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())
	steamDir, _, _ := createManagedExecutorTestLayout(t, "#!/bin/sh\nsleep 30\n")
	helloArrived := make(chan helloRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/hello":
			var request helloRequest
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode hello request: %v", err)
			}
			writeJSONResponse(t, w, helloResponse{SessionToken: "session-1"})
			helloArrived <- request
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/host-agents/capabilities":
			writeJSONResponse(t, w, map[string]any{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/host-agents/database-operations/next":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/heartbeat":
			writeJSONResponse(t, w, map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := newTestAgent(t, Config{
		ServerURL:              server.URL,
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorWait,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		SteamDir:               steamDir,
		HeartbeatInterval:      time.Second,
		PollInterval:           20 * time.Millisecond,
		RequestTimeout:         time.Second,
	})
	go func() {
		if err := agent.register(context.Background()); err != nil {
			t.Errorf("register agent: %v", err)
		}
	}()
	var request helloRequest
	select {
	case request = <-helloArrived:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for host-agent hello")
	}
	if request.JobProtocolVersion != "execution-agent-job-protocol.v1" {
		t.Fatalf("expected job protocol version, got %+v", request)
	}
	if request.CapabilitySchemaVersion != defaultRuntimeContract || !request.LifecycleCommandsSupported {
		t.Fatalf("expected runtime contract metadata, got %+v", request)
	}
	processCapability := findCapability(request.Capabilities, hostAgentProcessStartCapability)
	if processCapability.Capability == "" {
		t.Fatalf("expected %s capability in hello payload", hostAgentProcessStartCapability)
	}
	if processCapability.Status != hostAgentCapabilityStatus {
		t.Fatalf("expected active process capability, got %+v", processCapability)
	}
	if processCapability.Metadata["startupBehavior"] != startupBehaviorWait || processCapability.Metadata["runtimeContractVersion"] != defaultRuntimeContract {
		t.Fatalf("expected startup metadata, got %+v", processCapability.Metadata)
	}
	fileReadCapability := findCapability(request.Capabilities, hostAgentFileReadCapability)
	if fileReadCapability.Capability == "" || fileReadCapability.Status != hostAgentCapabilityStatus {
		t.Fatalf("expected active %s capability, got %+v", hostAgentFileReadCapability, fileReadCapability)
	}
	fileListCapability := findCapability(request.Capabilities, hostAgentFileListCapability)
	if fileListCapability.Capability == "" || fileListCapability.Status != hostAgentCapabilityStatus {
		t.Fatalf("expected active %s capability, got %+v", hostAgentFileListCapability, fileListCapability)
	}
}

// TestAgentPollsFileReadOperation verifies that host-agent mode can claim, execute, and report one bounded file read operation.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentPollsFileReadOperation(t *testing.T) {
	steamDir, _, _ := createManagedExecutorTestLayout(t, "#!/bin/sh\nsleep 30\n")
	targetPath := filepath.Join(steamDir, "SCUM", "Saved", "Config", "WindowsServer", "ServerSettings.ini")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create settings dir: %v", err)
	}
	content := strings.Repeat("header-line\n", 8) + "ServerName=Test Server\nMaxPlayers=64\n"
	if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write settings file: %v", err)
	}
	var (
		report        fileOperationResultRequest
		reportArrived = make(chan struct{}, 1)
		fileServed    bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/hello":
			writeJSONResponse(t, w, helloResponse{SessionToken: "session-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/heartbeat":
			writeJSONResponse(t, w, map[string]any{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/host-agents/database-operations/next":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/host-agents/file-operations/next":
			if fileServed {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			fileServed = true
			writeJSONResponse(t, w, fileOperation{
				ID:            "fop-1",
				OperationType: "read",
				RelativePath:  "SCUM/Saved/Config/WindowsServer/ServerSettings.ini",
				ContentMode:   "text",
				Result:        map[string]any{"limits": map[string]any{"readByteLimit": 64}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/file-operations/fop-1/result":
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Fatalf("decode file report: %v", err)
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
		ServerURL:              server.URL,
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorWait,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		ScopeRoot:              steamDir,
		SteamDir:               steamDir,
		HeartbeatInterval:      5 * time.Second,
		PollInterval:           20 * time.Millisecond,
		RequestTimeout:         time.Second,
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
		t.Fatal("timed out waiting for file report")
	}

	if err := <-done; err != nil {
		t.Fatalf("agent returned error: %v", err)
	}
	if report.Status != "succeeded" {
		t.Fatalf("expected succeeded file report, got %+v", report)
	}
	if report.Metadata["content"] == "" {
		t.Fatalf("expected bounded file content, got %+v", report.Metadata)
	}
	if report.BeforeChecksum == "" || report.AfterChecksum == "" {
		t.Fatalf("expected file checksums, got %+v", report)
	}
}

// TestAgentExecuteFileListOperation verifies the host agent can enumerate one scoped directory with bounded metadata.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentExecuteFileListOperation(t *testing.T) {
	steamDir, _, _ := createManagedExecutorTestLayout(t, "#!/bin/sh\nsleep 30\n")
	logsDir := filepath.Join(steamDir, "SCUM", "Saved", "SaveFiles", "Logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("create logs dir: %v", err)
	}
	for _, name := range []string{"admin.log", "gameplay.log", "login.log"} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write log file: %v", err)
		}
	}
	agent := newTestAgent(t, Config{
		ServerURL:              "http://127.0.0.1",
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorWait,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		ScopeRoot:              steamDir,
		SteamDir:               steamDir,
		HeartbeatInterval:      time.Second,
		PollInterval:           time.Second,
		RequestTimeout:         time.Second,
	})

	result := agent.executeFileOperation(fileOperation{
		ID:            "fop-list-1",
		OperationType: "list",
		RelativePath:  "SCUM/Saved/SaveFiles/Logs",
		Result:        map[string]any{"limits": map[string]any{"listEntryLimit": 2}},
	})
	if result.Status != "succeeded" {
		t.Fatalf("expected succeeded list result, got %+v", result)
	}
	entries, ok := result.Metadata["entries"].([]fileListEntry)
	if !ok {
		t.Fatalf("expected typed file list entries, got %+v", result.Metadata["entries"])
	}
	if len(entries) != 2 {
		t.Fatalf("expected bounded entry list, got %+v", entries)
	}
}

// TestAgentManagedCapabilityReturnsBlockedResult verifies host-agent mode returns a structured blocked error when process runtime readiness is missing.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentManagedCapabilityReturnsBlockedResult(t *testing.T) {
	agent := newTestAgent(t, Config{
		ServerURL:         "http://127.0.0.1",
		RegistrationToken: "token",
		AgentID:           "agent-1",
		DisplayName:       "agent-1",
		Version:           "test",
		Address:           "127.0.0.1",
		DatabasePath:      createTestDatabase(t),
		HeartbeatInterval: time.Second,
		PollInterval:      time.Second,
		RequestTimeout:    time.Second,
	})

	_, err := agent.executeManagedCapability(hostAgentProcessStartCapability, nil)
	var capabilityErr *localruntime.CapabilityExecutionError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("expected structured capability error, got %v", err)
	}
	if capabilityErr.ReasonCode != localruntime.ProcessReasonPathUnresolved {
		t.Fatalf("expected process path unresolved reason, got %+v", capabilityErr)
	}
	if capabilityErr.Data == nil {
		t.Fatalf("expected blocked error data, got %+v", capabilityErr)
	}
}

// TestAgentManagedCapabilityStartsProcess verifies host-agent process dispatch uses the shared runtime for successful local lifecycle work.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentManagedCapabilityStartsProcess(t *testing.T) {
	steamDir, _, _ := createManagedExecutorTestLayout(t, "#!/bin/sh\nsleep 30\n")
	agent := newTestAgent(t, Config{
		ServerURL:              "http://127.0.0.1",
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorWait,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		SteamDir:               steamDir,
		HeartbeatInterval:      time.Second,
		PollInterval:           time.Second,
		RequestTimeout:         time.Second,
	})

	startResult, err := agent.executeManagedCapability(hostAgentProcessStartCapability, nil)
	if err != nil {
		t.Fatalf("start managed capability: %v", err)
	}
	status, ok := startResult.Data["status"].(process.Status)
	if !ok || !status.Running {
		t.Fatalf("expected running process status, got %#v", startResult.Data["status"])
	}
	if _, err := agent.executeManagedCapability(hostAgentProcessStopCapability, nil); err != nil {
		t.Fatalf("stop managed capability: %v", err)
	}
}

// TestAgentBootstrapStartOnceStartsProcessOnlyOnce verifies bootstrap_start_once starts the local process once and records an idempotency marker.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentBootstrapStartOnceStartsProcessOnlyOnce(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", stateDir)
	startLog := filepath.Join(t.TempDir(), "bootstrap.log")
	script := "#!/bin/sh\n" +
		"echo started >> '" + startLog + "'\n" +
		"sleep 30\n"
	steamDir, _, _ := createManagedExecutorTestLayout(t, script)

	agent := newTestAgent(t, Config{
		ServerURL:              "http://127.0.0.1",
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorBootstrap,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		SteamDir:               steamDir,
		HeartbeatInterval:      time.Second,
		PollInterval:           time.Second,
		RequestTimeout:         time.Second,
	})
	if err := agent.maybeBootstrapStartOnce(); err != nil {
		t.Fatalf("bootstrap start once: %v", err)
	}
	if err := agent.maybeBootstrapStartOnce(); err != nil {
		t.Fatalf("second bootstrap start once should be ignored, got %v", err)
	}
	if !agent.runtime.Process().GetStatus().Running {
		t.Fatalf("expected managed executor bootstrap to start process, got %+v", agent.runtime.Process().GetStatus())
	}
	content, err := os.ReadFile(startLog)
	if err != nil {
		t.Fatalf("read bootstrap log: %v", err)
	}
	if strings.Count(string(content), "started") != 1 {
		t.Fatalf("expected bootstrap start to run once, got %q", string(content))
	}
	if err := agent.runtime.Process().Stop(); err != nil {
		t.Fatalf("stop bootstrap process: %v", err)
	}
}

// TestAgentRegisterAppliesBootstrapLaunchProfile verifies hello bootstrap launch profiles update the shared runtime before process start.
// t is the testing handle used for assertions, and the function returns no values.
func TestAgentRegisterAppliesBootstrapLaunchProfile(t *testing.T) {
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())
	steamDir, _, _ := createManagedExecutorTestLayout(t, "#!/bin/sh\nsleep 30\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/host-agents/hello":
			writeJSONResponse(t, w, helloResponse{
				SessionToken: "session-1",
				BootstrapLaunchProfile: &bootstrapLaunchProfile{
					ServiceName:       "scum-main",
					Ports:             []bootstrapLaunchDeclaredPort{{Name: "game", Port: 7777}},
					WorkDir:           ".",
					LaunchMode:        "argv",
					Executable:        "SCUM/Binaries/Win64/SCUMServer.exe",
					Args:              []string{"-log", "-port=7777"},
					DesiredGeneration: 4,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := newTestAgent(t, Config{
		ServerURL:              server.URL,
		RegistrationToken:      "token",
		AgentID:                "agent-1",
		DisplayName:            "agent-1",
		Version:                "test",
		StartupBehavior:        startupBehaviorWait,
		RuntimeContractVersion: defaultRuntimeContract,
		Address:                "127.0.0.1",
		SteamDir:               steamDir,
		HeartbeatInterval:      time.Second,
		PollInterval:           time.Second,
		RequestTimeout:         time.Second,
	})

	if err := agent.register(context.Background()); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	config := agent.runtime.Process().GetConfig()
	if config.LaunchProfile == nil {
		t.Fatal("expected bootstrap launch profile to be applied to process config")
	}
	if len(config.LaunchProfile.Args) != 2 || config.LaunchProfile.Args[0] != "-log" {
		t.Fatalf("expected argv args to be applied, got %+v", config.LaunchProfile)
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

// createManagedExecutorTestLayout builds one fake SCUM layout with a database and executable for managed-executor host-agent tests.
// t is the testing handle, script is written as the fake SCUMServer executable, and the function returns the Steam root, database path and server executable path.
func createManagedExecutorTestLayout(t *testing.T, script string) (string, string, string) {
	t.Helper()
	steamDir := t.TempDir()
	databasePath := filepath.Join(steamDir, "SCUM", "Saved", "SaveFiles", "SCUM.db")
	serverPath := filepath.Join(steamDir, "SCUM", "Binaries", "Win64", "SCUMServer.exe")
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
		t.Fatalf("create sqlite table: %v", err)
	}
	if err := os.WriteFile(serverPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake server executable: %v", err)
	}
	return steamDir, databasePath, serverPath
}

// findCapability returns the first declared capability with the requested key.
// capabilities is the hello payload capability list, key identifies the desired capability, and the function returns the matching capability or a zero-value struct when absent.
func findCapability(capabilities []hostAgentCapability, key string) hostAgentCapability {
	for _, capability := range capabilities {
		if capability.Capability == key {
			return capability
		}
	}
	return hostAgentCapability{}
}
