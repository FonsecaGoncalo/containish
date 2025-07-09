package container

import (
	"os"
	"path/filepath"
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
