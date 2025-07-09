package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
