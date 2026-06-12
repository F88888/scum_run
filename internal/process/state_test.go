package process

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"scum_run/internal/logger"
	"scum_run/model"
)

// TestStatePathUsesServiceAndPort verifies that local process state is scoped per service identity.
// t is the test handle, and the test fails when different service/port combinations collide.
func TestStatePathUsesServiceAndPort(t *testing.T) {
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())
	log := logger.New()
	first := NewWithConfig(&model.ServerConfig{ServiceName: "alpha", GamePort: 31001}, log)
	second := NewWithConfig(&model.ServerConfig{ServiceName: "alpha", GamePort: 31002}, log)
	third := NewWithConfig(&model.ServerConfig{ServiceName: "beta", GamePort: 31001}, log)

	paths := map[string]bool{
		first.statePath():  true,
		second.statePath(): true,
		third.statePath():  true,
	}
	if len(paths) != 3 {
		t.Fatalf("expected service and port to produce distinct state paths, got %#v", paths)
	}
}

// TestStatePathUsesLaunchGeneration verifies launch profile generations get distinct runtime state files.
// t is the test handle, and the test fails when two launch generations collide.
func TestStatePathUsesLaunchGeneration(t *testing.T) {
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())
	log := logger.New()
	first := NewWithConfig(&model.ServerConfig{LaunchProfile: &model.LaunchProfile{ServiceName: "mc", Ports: []model.LaunchDeclaredPort{{Name: "game", Port: 25565}}, LaunchGeneration: 1}}, log)
	second := NewWithConfig(&model.ServerConfig{LaunchProfile: &model.LaunchProfile{ServiceName: "mc", Ports: []model.LaunchDeclaredPort{{Name: "game", Port: 25565}}, LaunchGeneration: 2}}, log)

	if first.statePath() == second.statePath() {
		t.Fatalf("expected launch generations to produce distinct state paths: %s", first.statePath())
	}
}

// TestBuildLaunchProfileCommandResolvesScopedExecutable verifies argv profiles resolve under the instance scope.
// t is the test handle, and the test fails when the command does not use the scoped work directory or executable.
func TestBuildLaunchProfileCommandResolvesScopedExecutable(t *testing.T) {
	scope := t.TempDir()
	workDir := filepath.Join(scope, "servers", "main")
	if err := os.MkdirAll(filepath.Join(workDir, "bin"), 0755); err != nil {
		t.Fatalf("create work dir: %v", err)
	}
	executable := filepath.Join(workDir, "bin", "start.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	manager := NewWithConfig(&model.ServerConfig{
		ExecPath: scope,
		LaunchProfile: &model.LaunchProfile{
			ServerInstanceID: "si-1",
			ServiceName:      "mc",
			Ports:            []model.LaunchDeclaredPort{{Name: "game", Port: 25565}},
			LaunchGeneration: 1,
			WorkDir:          "servers/main",
			LaunchMode:       "argv",
			Executable:       "bin/start.sh",
			Args:             []string{"--no-color"},
		},
	}, logger.New())

	cmd, err := manager.buildCommand()
	if err != nil {
		t.Fatalf("build launch profile command: %v", err)
	}
	expectedExecutable, _ := filepath.EvalSymlinks(executable)
	expectedWorkDir, _ := filepath.EvalSymlinks(workDir)
	if cmd.Path != expectedExecutable || cmd.Dir != expectedWorkDir || len(cmd.Args) != 2 || cmd.Args[1] != "--no-color" {
		t.Fatalf("unexpected scoped command: path=%s dir=%s args=%v", cmd.Path, cmd.Dir, cmd.Args)
	}
}

// TestBuildLaunchProfileCommandRejectsTraversal verifies profile paths cannot escape the instance scope.
// t is the test handle, and the test fails when traversal is accepted.
func TestBuildLaunchProfileCommandRejectsTraversal(t *testing.T) {
	scope := t.TempDir()
	manager := NewWithConfig(&model.ServerConfig{
		ExecPath: scope,
		LaunchProfile: &model.LaunchProfile{
			ServiceName:      "bad",
			Ports:            []model.LaunchDeclaredPort{{Name: "game", Port: 25565}},
			LaunchGeneration: 1,
			WorkDir:          "../outside",
			LaunchMode:       "argv",
			Executable:       "start.sh",
		},
	}, logger.New())
	if _, err := manager.buildCommand(); err == nil {
		t.Fatal("expected traversal launch profile to be rejected")
	}
}

// TestBuildLaunchProfileCommandRejectsSymlinkEscape verifies executable symlinks cannot leave the instance scope.
// t is the test handle, and the test skips when the platform does not allow symlink creation.
func TestBuildLaunchProfileCommandRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation often requires privileges on Windows")
	}
	scope := t.TempDir()
	outside := t.TempDir()
	workDir := filepath.Join(scope, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("create work dir: %v", err)
	}
	outsideExecutable := filepath.Join(outside, "start.sh")
	if err := os.WriteFile(outsideExecutable, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write outside executable: %v", err)
	}
	if err := os.Symlink(outsideExecutable, filepath.Join(workDir, "start.sh")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manager := NewWithConfig(&model.ServerConfig{
		ExecPath: scope,
		LaunchProfile: &model.LaunchProfile{
			ServiceName:      "bad",
			Ports:            []model.LaunchDeclaredPort{{Name: "game", Port: 25565}},
			LaunchGeneration: 1,
			WorkDir:          "work",
			LaunchMode:       "argv",
			Executable:       "start.sh",
		},
	}, logger.New())
	if _, err := manager.buildCommand(); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

// TestCleanupOnExitDetachesProcess verifies executor cleanup does not kill a started server.
// t is the test handle, and the test fails when CleanupOnExit terminates the managed process.
func TestCleanupOnExitDetachesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command shape is Unix-specific")
	}
	t.Setenv("SCUM_RUN_PROCESS_STATE_DIR", t.TempDir())
	manager := NewWithConfig(&model.ServerConfig{
		ServiceName:  "detach-test",
		GamePort:     31003,
		ServerIP:     "127.0.0.1",
		StartCommand: "sleep 30",
	}, logger.New())
	if err := manager.Start(); err != nil {
		t.Fatalf("start detached command: %v", err)
	}
	pid := manager.GetPID()
	if pid <= 0 {
		t.Fatalf("expected running process pid")
	}
	defer func() {
		_ = manager.ForceStop()
	}()

	manager.CleanupOnExit()
	time.Sleep(100 * time.Millisecond)
	if !testProcessAlive(pid) {
		t.Fatalf("expected process %d to survive CleanupOnExit", pid)
	}
	if err := manager.Stop(); err != nil {
		t.Fatalf("stop detached command: %v", err)
	}
}

// testProcessAlive checks whether a process is still alive during tests.
// pid identifies the process, and the function returns true when signal zero succeeds.
func testProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
