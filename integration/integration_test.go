package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"containish/container"
)

func TestContainerEcho(t *testing.T) {
	if os.Getenv("IN_VM") != "1" {
		t.Skip("integration test only runs inside the VM")
	}

	// Build the containish binary from the repository root
	build := exec.Command("go", "build", "-o", "containish")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build containish: %v\n%s", err, string(out))
	}

	// Run the container and capture output
	runCmd := exec.Command("bash", "-c", "echo 'echo hello && exit' | sudo ./containish run test_container")
	runCmd.Dir = ".."
	output, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run container: %v\n%s", err, string(output))
	}
	if !strings.Contains(string(output), "hello") {
		t.Fatalf("expected output to contain 'hello', got:\n%s", string(output))
	}
}

func TestContainerStateFile(t *testing.T) {
	if os.Getenv("IN_VM") != "1" {
		t.Skip("integration test only runs inside the VM")
	}

	// Build the binary
	build := exec.Command("go", "build", "-o", "containish")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build containish: %v\n%s", err, string(out))
	}

	id := "container_id"
	stateDir := filepath.Join("/run/miniruntime", id)

	// Ensure no leftover state
	_ = exec.Command("sudo", "rm", "-rf", stateDir).Run()

	runCmd := exec.Command("bash", "-c", "echo 'echo hi && exit' | sudo ./containish run "+id)
	runCmd.Dir = ".."
	if out, err := runCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to run container: %v\n%s", err, string(out))
	}

	stateBytes, err := exec.Command("sudo", "cat", filepath.Join(stateDir, "state.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read state.json: %v\n%s", err, string(stateBytes))
	}

	var c container.Container
	if err := json.Unmarshal(stateBytes, &c); err != nil {
		t.Fatalf("failed to decode state.json: %v", err)
	}

	if c.Id != id {
		t.Fatalf("expected id %s, got %s", id, c.Id)
	}
	if c.Status != container.Stopped {
		t.Fatalf("expected status %v, got %v", container.Stopped, c.Status)
	}
	if c.InitProcessPiD == 0 {
		t.Fatalf("expected non-zero PID")
	}
}

func TestDetachAndStop(t *testing.T) {
	if os.Getenv("IN_VM") != "1" {
		t.Skip("integration test only runs inside the VM")
	}

	build := exec.Command("go", "build", "-o", "containish")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build containish: %v\n%s", err, string(out))
	}

	id := "detachstop"
	stateDir := filepath.Join("/run/miniruntime", id)
	_ = exec.Command("sudo", "rm", "-rf", stateDir).Run()

	runCmd := exec.Command("bash", "-c", "echo 'sleep 30' | sudo ./containish run -d "+id)
	runCmd.Dir = ".."
	if out, err := runCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to run container: %v\n%s", err, string(out))
	}

	stateBytes, err := exec.Command("sudo", "cat", filepath.Join(stateDir, "state.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read state.json: %v\n%s", err, string(stateBytes))
	}
	var c container.Container
	if err := json.Unmarshal(stateBytes, &c); err != nil {
		t.Fatalf("failed to decode state.json: %v", err)
	}
	if c.Status != container.Running {
		t.Fatalf("expected status Running, got %v", c.Status)
	}
	if err := exec.Command("sudo", "kill", "-0", strconv.Itoa(c.InitProcessPiD)).Run(); err != nil {
		t.Fatalf("init process not running: %v", err)
	}

	// Verify the init process is now reparented to PID 1
	ppidBytes, err := exec.Command("bash", "-c", fmt.Sprintf("ps -o ppid= -p %d", c.InitProcessPiD)).Output()
	if err != nil {
		t.Fatalf("failed to get parent pid: %v", err)
	}
	if strings.TrimSpace(string(ppidBytes)) != "1" {
		t.Fatalf("expected init parent pid 1, got %s", strings.TrimSpace(string(ppidBytes)))
	}

	stopCmd := exec.Command("sudo", "./containish", "stop", id)
	stopCmd.Dir = ".."
	if out, err := stopCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to stop container: %v\n%s", err, string(out))
	}

	if err := exec.Command("sudo", "kill", "-0", strconv.Itoa(c.InitProcessPiD)).Run(); err == nil {
		t.Fatalf("process still running after stop")
	}

	stateBytes, err = exec.Command("sudo", "cat", filepath.Join(stateDir, "state.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read state.json after stop: %v\n%s", err, string(stateBytes))
	}
	if err := json.Unmarshal(stateBytes, &c); err != nil {
		t.Fatalf("failed to decode state.json after stop: %v", err)
	}
	if c.Status != container.Stopped {
		t.Fatalf("expected status Stopped, got %v", c.Status)
	}
}
