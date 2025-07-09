package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSaveLoadState(t *testing.T) {
	dir := t.TempDir()

	c := &Container{
		Id:             "test123",
		InitProcessPiD: 42,
		CreatedAt:      time.Now().UTC().Round(time.Second),
		Status:         Running,
		Bundle:         "mybundle",
	}

	if err := SaveState(dir, c); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// ensure file exists
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("state.json not created: %v", err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if loaded.Id != c.Id || loaded.InitProcessPiD != c.InitProcessPiD ||
		!loaded.CreatedAt.Equal(c.CreatedAt) || loaded.Status != c.Status ||
		loaded.Bundle != c.Bundle {
		t.Fatalf("loaded state does not match saved state")
	}
}

func TestSaveStateCreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a", "b")

	c := &Container{
		Id:        "dirtest",
		CreatedAt: time.Now(),
		Status:    Created,
	}

	if err := SaveState(dir, c); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("state.json not created: %v", err)
	}
}

func TestLoadSpec(t *testing.T) {
	dir := t.TempDir()
	specJSON := `{"ociVersion":"1.0.2","root":{"path":"/tmp/rootfs"}}`
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(specJSON), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	spec, err := LoadSpec(cfg)
	if err != nil {
		t.Fatalf("LoadSpec failed: %v", err)
	}

	if spec.Root.Path != "/tmp/rootfs" {
		t.Fatalf("unexpected root path %s", spec.Root.Path)
	}
}

func TestStopContainer(t *testing.T) {
	baseStateDir = t.TempDir()
	id := "stoptest"

	// launch a dummy process to act as the container init
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start dummy process: %v", err)
	}

	stateDir, err := CreateStateDir(id)
	if err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	c := &Container{Id: id, InitProcessPiD: cmd.Process.Pid, CreatedAt: time.Now(), Status: Running}
	if err := SaveState(stateDir, c); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	if err := StopContainer(id); err != nil {
		t.Fatalf("StopContainer failed: %v", err)
	}

	// Wait for process to exit to avoid race in checking
	_ = cmd.Wait()

	// process should be gone
	err = cmd.Process.Signal(syscall.Signal(0))
	if err == nil {
		t.Fatalf("process still running after StopContainer")
	}

	loaded, err := LoadState(stateDir)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if loaded.Status != Stopped {
		t.Fatalf("expected status Stopped, got %v", loaded.Status)
	}
}
