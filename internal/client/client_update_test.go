package client

import (
	"runtime"
	"testing"
	"time"

	"scum_run/internal/logger"
	"scum_run/internal/process"
	"scum_run/model"
)

// TestPrepareRunningServerForSelfUpdateLeavesServerRunning verifies self-update keeps SCUM alive unless stop_server was explicitly requested.
// t provides the test context, and the test starts one detached sleep command to simulate the managed server.
// It returns no values; the test fails when the helper stops the server even though the caller did not request it.
func TestPrepareRunningServerForSelfUpdateLeavesServerRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command shape is Unix-specific")
	}
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())

	manager := process.NewWithConfig(&model.ServerConfig{
		ServiceName:  "client-update-leave-running",
		GamePort:     31011,
		ServerIP:     "127.0.0.1",
		StartCommand: "sleep 30",
	}, logger.New())
	if err := manager.Start(); err != nil {
		t.Fatalf("start detached command: %v", err)
	}
	defer func() {
		_ = manager.ForceStop()
	}()

	client := &Client{
		logger:  logger.New(),
		process: manager,
	}
	if err := client.prepareRunningServerForSelfUpdate(false); err != nil {
		t.Fatalf("prepare self-update without stop request: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !manager.IsRunning() {
		t.Fatal("expected server to keep running when stop_server is false")
	}
}

// TestPrepareRunningServerForSelfUpdateStopsServer verifies self-update only stops SCUM when stop_server was explicitly requested.
// t provides the test context, and the test starts one detached sleep command to simulate the managed server.
// It returns no values; the test fails when the helper leaves the server running after an explicit stop request or returns a stop error.
func TestPrepareRunningServerForSelfUpdateStopsServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command shape is Unix-specific")
	}
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())

	manager := process.NewWithConfig(&model.ServerConfig{
		ServiceName:  "client-update-stop-running",
		GamePort:     31012,
		ServerIP:     "127.0.0.1",
		StartCommand: "sleep 30",
	}, logger.New())
	if err := manager.Start(); err != nil {
		t.Fatalf("start detached command: %v", err)
	}

	client := &Client{
		logger:  logger.New(),
		process: manager,
	}
	if err := client.prepareRunningServerForSelfUpdate(true); err != nil {
		t.Fatalf("prepare self-update with stop request: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if manager.IsRunning() {
		t.Fatal("expected server to stop when stop_server is true")
	}
}
