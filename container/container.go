package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const ParentStage = "PARENT_STAGE"
const ChildStage = "CHILD_STAGE"

type Status int

const (
	Created Status = iota
	Running
	Stopped
)

type Container struct {
	Id             string    `json:"id"`
	InitProcessPiD int       `json:"initProcessPiD"`
	CreatedAt      time.Time `json:"createdAt"`
	Status         Status    `json:"status"`
	Bundle         string    `json:"bundle"`
}

func SaveState(stateDir string, s *Container) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("failed to create state dir: %w", err)
	}

	f, err := os.Create(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return fmt.Errorf("failed to create state.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("failed to enconde container state: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync state.json: %w", err)
	}

	return nil
}

func LoadState(stateDir string) (*Container, error) {
	f, err := os.Open(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to open state.json: %w", err)
	}
	defer f.Close()

	var s Container
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("failed to decode state.json: %w", err)
	}
	return &s, nil
}

// LoadSpec loads a runtime-spec config from the given path.
func LoadSpec(configPath string) (*specs.Spec, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open config file: %w", err)
	}
	defer f.Close()

	var spec specs.Spec
	if err := json.NewDecoder(f).Decode(&spec); err != nil {
		return nil, fmt.Errorf("cannot decode config: %w", err)
	}
	return &spec, nil
}

func CreateStateDir(containerId string) (string, error) {
	baseDir := "/run/miniruntime"
	stateDir := filepath.Join(baseDir, containerId)
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create container state dir: %w", err)
	}
	return stateDir, nil
}

// RunContainer prepares, forks, and executes the container process.
func RunContainer(containerId string) error {
	// Copy the local Alpine root filesystem into /alpine.
	if err := cpAlpineFS(); err != nil {
		return fmt.Errorf("failed to copy alpine FS: %w", err)
	}

	stateDir, err := CreateStateDir(containerId)
	if err != nil {
		return err
	}

	container := &Container{
		Id:             containerId,
		InitProcessPiD: 0,
		Status:         Created,
		CreatedAt:      time.Now(),
		Bundle:         "",
	}

	if err := SaveState(stateDir, container); err != nil {
		return err
	}

	// Create a socket pair used for simple one-byte notifications
	// between the parent and child processes.
	parent, child, err := initSocketPair("init")
	if err != nil {
		return fmt.Errorf("failed to create socket pair: %w", err)
	}
	defer parent.Close()

	// Prepare the parent-stage command—essentially re-invoking our own binary with "init PARENT_STAGE".
	cmd := exec.Command("/proc/self/exe", "init", ParentStage)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// The child side of the socket pair becomes the ExtraFile with FD=3 (or next available).
	cmd.ExtraFiles = append(cmd.ExtraFiles, child)
	// We inform the child process which FD to use via the environment.
	cmd.Env = append(cmd.Env, "INIT_PIPE="+strconv.Itoa(3+len(cmd.ExtraFiles)-1))

	fmt.Println("PARENT: Forking /proc/self/exe with PARENT_STAGE")
	if err := cmd.Start(); err != nil {
		_ = child.Close() // best effort
		return fmt.Errorf("failed to start parent-stage process: %w", err)
	}
	_ = child.Close() // Close child side in parent

	// Wait for the child to notify that the child setup has been done
	waitCh := initWaiter(parent)
	fmt.Println("PARENT: Waiting for child setup signal...")
	if err := <-waitCh; err != nil {
		// if there's an error from the child, let's wait on cmd to ensure no zombie
		_ = cmd.Wait()
		return fmt.Errorf("child setup failed: %w", err)
	}
	fmt.Println("PARENT: Child setup done.")

	container.InitProcessPiD = cmd.Process.Pid
	container.Status = Running
	if err := SaveState(stateDir, container); err != nil {
		return err
	}
	// Optionally, wait for the parent-stage to complete fully.
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("error waiting for parent-stage cmd: %w", err)
	}

	container.Status = Stopped
	if err := SaveState(stateDir, container); err != nil {
		return err
	}

	return nil
}

// cpAlpineFS copies the local Alpine filesystem from /vagrant/alpine to /alpine.
func cpAlpineFS() error {
	src := "/vagrant/alpine"
	dst := "/alpine"

	cmd := exec.Command("cp", "-r", src, dst)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error copying directory from %s to %s: %w", src, dst, err)
	}
	fmt.Printf("Directory copied successfully from %s to %s\n", src, dst)
	return nil
}

// initSocketPair creates a Unix domain socket pair used for a simple
// one-byte handshake between processes.
func initSocketPair(name string) (parent, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_LOCAL, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create socket pair: %w", err)
	}
	parent = os.NewFile(uintptr(fds[1]), name+"-parent")
	child = os.NewFile(uintptr(fds[0]), name+"-child")
	return parent, child, nil
}

// handleParentStage is called from init() if we detect we're in the parent stage.
// It spawns a child process (with new namespaces) and notifies the actual parent
// once the child has started.
func handleParentStage() error {
	fmt.Println("INIT: Inside parent stage of the new child process")

	fd, err := strconv.Atoi(os.Getenv("INIT_PIPE"))
	if err != nil {
		return fmt.Errorf("invalid INIT_PIPE FD: %w", err)
	}
	initComm := os.NewFile(uintptr(fd), "init-pipe")
	defer initComm.Close()

	// Notify the real parent that we've made it into the child.
	fmt.Println("INIT (parent-stage): Notifying real parent we are ready")
	if _, err := initComm.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to write init setup byte: %w", err)
	}

	// Now spawn the *second* stage: a new process in new namespaces.
	fmt.Println("INIT (parent-stage): Spawning the child-stage in new namespaces")
	childCmd := exec.Command("/proc/self/exe", "init", ChildStage)
	childCmd.Stdin = os.Stdin
	childCmd.Stdout = os.Stdout
	childCmd.Stderr = os.Stderr

	// Note: removed the duplicate CLONE_NEWPID
	childCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUTS |
			unix.CLONE_NEWPID |
			unix.CLONE_NEWNET |
			unix.CLONE_NEWNS |
			unix.CLONE_NEWCGROUP,
	}

	if err := childCmd.Start(); err != nil {
		return fmt.Errorf("failed to start child stage: %w", err)
	}

	fmt.Printf("Child-stage PID (host) = %d\n", childCmd.Process.Pid)

	// Optionally write the child's PID to our parent (not strictly necessary).
	if _, err := initComm.Write([]byte(fmt.Sprintf("pid:%d", childCmd.Process.Pid))); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write child's PID: %v\n", err)
	}

	// Wait for the child stage to exit (so we don't leak a child).
	if err := childCmd.Wait(); err != nil {
		return fmt.Errorf("child stage error: %w", err)
	}

	return nil
}

// handleChildStage is called if we detect we're in the "init child" stage
// that is run inside the new namespaces.
func handleChildStage() error {
	fmt.Println("INIT: Entering child stage")
	fmt.Printf("INIT (child-stage): process pid on the host = %d\n", unix.Getpid())

	// Make / a private mount point so we don't propagate changes.
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to make / a private mount: %w", err)
	}

	// Bind-mount /alpine to itself so we can pivot-root later.
	if err := unix.Mount("/alpine", "/alpine", "bind", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind /alpine: %w", err)
	}

	oldroot, err := unix.Open("/", unix.O_DIRECTORY|unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening old root '/': %w", err)
	}
	defer unix.Close(oldroot)

	newroot, err := unix.Open("/alpine", unix.O_DIRECTORY|unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening new root '/alpine': %w", err)
	}
	defer unix.Close(newroot)

	// Move into /alpine so pivot_root operates on "."
	if err := unix.Fchdir(newroot); err != nil {
		return fmt.Errorf("failed to fchdir to new root: %w", err)
	}

	fmt.Println("INIT (child-stage): pivot_root into /alpine ...")
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("failed to pivot_root: %w", err)
	}

	// Move back to the old root's directory.
	if err := unix.Fchdir(oldroot); err != nil {
		return fmt.Errorf("failed to fchdir to old root: %w", err)
	}

	// The new root is now "/", so let's chdir into it.
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("failed to chdir('/'): %w", err)
	}

	// Mark old root as slave to avoid mount propagation back to host.
	if err := unix.Mount("", ".", "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to make old root private: %w", err)
	}

	// Unmount old root. MNT_DETACH means we detach the old mount tree.
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("failed to unmount old root: %w", err)
	}

	// Mount a new /proc in this PID namespace.
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("failed to mount /proc in child: %w", err)
	}

	fmt.Println("INIT (child-stage): Replacing current process with /bin/sh...")
	shellPath := "/bin/sh"
	argv := []string{shellPath}
	env := os.Environ()

	// Exec into /bin/sh. If this fails, we can't continue.
	if err := unix.Exec(shellPath, argv, env); err != nil {
		return fmt.Errorf("exec /bin/sh failed: %w", err)
	}
	return nil
}

// joinNamespace is an example of how you might join a particular namespace (UTS below).
// Not currently used in the main flow, but can be handy for debugging or advanced usage.
func joinNamespace(pid int, namespace string) {
	nsPath := fmt.Sprintf("/proc/%d/ns/%s", pid, namespace)
	fd, err := os.Open(nsPath)
	if err != nil {
		panic(fmt.Sprintf("Failed to open namespace file %s: %v", nsPath, err))
	}
	defer fd.Close()

	if err := unix.Setns(int(fd.Fd()), unix.CLONE_NEWUTS); err != nil {
		panic(fmt.Sprintf("failed to join namespace: %v", err))
	}
	fmt.Printf("Joined %s namespace of PID %d\n", namespace, pid)
}

// initWaiter reads a single byte from the provided reader to signal
// that the child has done preliminary setup. Returns a channel
// which yields an error if there was a problem or nil on success.
func initWaiter(r io.Reader) chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		buf := make([]byte, 1)

		n, err := r.Read(buf)
		if err == nil {
			if n < 1 {
				err = errors.New("short read (no bytes)")
			} else if buf[0] != 0 {
				err = fmt.Errorf("unexpected handshake byte %d != 0", buf[0])
			}
		}
		// If err is still nil here, we successfully got a 0 byte
		ch <- err
	}()
	return ch
}

// init is called automatically when this package is loaded (i.e., before main()).
// We detect whether we are in PARENT_STAGE or CHILD_STAGE and invoke the corresponding handler.
func init() {
	if len(os.Args) > 2 && os.Args[1] == "init" {
		stage := os.Args[2]
		switch stage {
		case ParentStage:
			if err := handleParentStage(); err != nil {
				fmt.Fprintf(os.Stderr, "Error in parent stage: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case ChildStage:
			if err := handleChildStage(); err != nil {
				fmt.Fprintf(os.Stderr, "Error in child stage: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}
