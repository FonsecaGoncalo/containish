package container

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// stageOptions represents configuration passed from the runtime to the parent
// stage through the init pipe.
type stageOptions struct {
	Detach bool   `json:"detach"`
	Rootfs string `json:"rootfs"`
}

// baseStateDir is where container state directories are created. It is a
// variable so tests can override it.
var baseStateDir = "/run/miniruntime"

// StateDir returns the path to the state directory for a container.
func StateDir(id string) string {
	return filepath.Join(baseStateDir, id)
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
	stateDir := StateDir(containerId)
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create container state dir: %w", err)
	}
	return stateDir, nil
}

// RunContainer prepares, forks, and executes the container process.
// RunContainer prepares, forks, and executes the container process. If detach is
// true, the function returns once the container init process is running.
func RunContainer(containerId, specPath string, detach bool) error {
	spec, err := LoadSpec(specPath)
	if err != nil {
		return fmt.Errorf("loading spec: %w", err)
	}

	rootfs := spec.Root.Path
	if rootfs == "" {
		rootfs = "/alpine"
	}

	// Copy the local Alpine root filesystem into the location specified by the spec.
	if err := cpAlpineFS(rootfs); err != nil {
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
	parent, child, err := initSocketPair("init", unix.SOCK_CLOEXEC)
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

	// Send runtime options to the parent stage through the pipe
	opts := stageOptions{Detach: detach, Rootfs: rootfs}
	if err := json.NewEncoder(parent).Encode(&opts); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("failed to send stage options: %w", err)
	}

	// Wait for the child to signal readiness and report its PID
	fmt.Println("PARENT: Waiting for child setup signal...")
	childPID, err := readInitInfo(parent)
	if err != nil {
		// if there's an error from the child, let's wait on cmd to ensure no zombie
		_ = cmd.Wait()
		return fmt.Errorf("child setup failed: %w", err)
	}
	fmt.Println("PARENT: Child setup done.")

	container.InitProcessPiD = childPID
	container.Status = Running
	if err := SaveState(stateDir, container); err != nil {
		return err
	}

	if detach {
		// In detached mode we release the parent-stage process so it can
		// be reaped by the OS and return immediately after the init
		// process has started and the state has been saved.
		if err := cmd.Process.Release(); err != nil {
			return fmt.Errorf("failed to release parent-stage: %w", err)
		}
		return nil
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

// StopContainer terminates the container's init process and updates its state
// to Stopped.
func StopContainer(containerId string) error {
	stateDir := StateDir(containerId)
	c, err := LoadState(stateDir)
	if err != nil {
		return err
	}
	if c.Status != Running {
		return fmt.Errorf("container %s is not running", containerId)
	}

	proc, err := os.FindProcess(c.InitProcessPiD)
	if err != nil {
		return fmt.Errorf("cannot find process: %w", err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("failed to kill process: %w", err)
	}

	c.Status = Stopped
	if err := SaveState(stateDir, c); err != nil {
		return err
	}
	return nil
}

// cpAlpineFS copies the local Alpine filesystem from /vagrant/alpine to dst.
func cpAlpineFS(dst string) error {
	src := "/vagrant/alpine"
	if _, err := os.Stat(src); os.IsNotExist(err) {
		// fallback to local alpine directory when running outside the VM
		if wd, err2 := os.Getwd(); err2 == nil {
			alt := filepath.Join(wd, "alpine")
			if _, err3 := os.Stat(alt); err3 == nil {
				src = alt
			} else {
				return fmt.Errorf("alpine rootfs not found at %s or %s", src, alt)
			}
		} else {
			return fmt.Errorf("failed to stat %s: %w", src, err)
		}
	}

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("failed to create rootfs dir %s: %w", dst, err)
	}

	// Copy the contents of src into dst rather than nesting the directory
	cmd := exec.Command("cp", "-a", src+"/.", dst)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error copying directory from %s to %s: %w", src, dst, err)
	}
	fmt.Printf("Directory copied successfully from %s to %s\n", src, dst)
	return nil
}

// initSocketPair creates a Unix domain socket pair used for a simple
// one-byte handshake between processes.
func initSocketPair(name string, flags int) (parent, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_LOCAL, unix.SOCK_STREAM|flags, 0)
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

	var opts stageOptions
	if err := json.NewDecoder(initComm).Decode(&opts); err != nil {
		return fmt.Errorf("failed to read stage options: %w", err)
	}

	// Notify the real parent that we've made it into the child.
	fmt.Println("INIT (parent-stage): Notifying real parent we are ready")
	if _, err := initComm.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to write init setup byte: %w", err)
	}

	// create a pipe used for the child stage to notify when setup is
	// complete. This must remain open across exec so we don't set CLOEXEC.
	notifyParent, notifyChild, err := initSocketPair("stage", 0)
	if err != nil {
		return fmt.Errorf("failed to create stage pipe: %w", err)
	}
	defer notifyParent.Close()

	detach := opts.Detach
	rootfs := opts.Rootfs
	if rootfs == "" {
		rootfs = "/alpine"
	}

	// Now spawn the *second* stage: a new process in new namespaces.
	fmt.Println("INIT (parent-stage): Spawning the child-stage in new namespaces")
	childCmd := exec.Command("/proc/self/exe", "init", ChildStage)
	childExtraFiles := []*os.File{notifyChild}
	childCmd.ExtraFiles = append(childCmd.ExtraFiles, childExtraFiles...)

	// FD number for the notify pipe inside the child
	notifyFD := 3 + len(childCmd.ExtraFiles) - 1
	childCmd.Env = append(os.Environ(),
		"ROOTFS_PATH="+rootfs,
		fmt.Sprintf("STAGE_PIPE=%d", notifyFD),
	)
	if detach {
		// Detach the child from our terminal for output. If something is
		// piped into the runtime's stdin, forward it through a pipe so
		// the container can still read the script without holding the
		// controlling terminal.
		pr, pw, err := os.Pipe()
		if err != nil {
			return fmt.Errorf("failed to create stdin pipe: %w", err)
		}
		childCmd.Stdin = pr
		childCmd.Stdout = nil
		childCmd.Stderr = nil

		go func() {
			_, _ = io.Copy(pw, os.Stdin)
			pw.Close()
		}()
	} else {
		childCmd.Stdin = os.Stdin
		childCmd.Stdout = os.Stdout
		childCmd.Stderr = os.Stderr
	}

	// Note: removed the duplicate CLONE_NEWPID
	childCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUTS |
			unix.CLONE_NEWPID |
			unix.CLONE_NEWNET |
			unix.CLONE_NEWNS |
			unix.CLONE_NEWCGROUP,
	}
	if detach {
		// Ensure the container does not receive a SIGHUP when the
		// runtime process exits.
		childCmd.SysProcAttr.Setsid = true
	}

	if err := childCmd.Start(); err != nil {
		return fmt.Errorf("failed to start child stage: %w", err)
	}

	// close our copy of the child end after the fork
	_ = notifyChild.Close()

	// wait for the child stage to signal successful setup
	b := make([]byte, 1)
	if _, err := notifyParent.Read(b); err != nil {
		return fmt.Errorf("failed waiting for child setup: %w", err)
	}
	if b[0] != 0 {
		return fmt.Errorf("unexpected stage byte %d", b[0])
	}

	fmt.Printf("Child-stage PID (host) = %d\n", childCmd.Process.Pid)

	// Optionally write the child's PID to our parent (not strictly necessary).
	if _, err := initComm.Write([]byte(fmt.Sprintf("pid:%d\n", childCmd.Process.Pid))); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write child's PID: %v\n", err)
	}

	if detach {
		if err := childCmd.Process.Release(); err != nil {
			return fmt.Errorf("failed to release child: %w", err)
		}
		return nil
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

	stageFDStr := os.Getenv("STAGE_PIPE")
	var stagePipe *os.File
	if stageFDStr != "" {
		fd, err := strconv.Atoi(stageFDStr)
		if err != nil {
			return fmt.Errorf("invalid STAGE_PIPE fd: %w", err)
		}
		stagePipe = os.NewFile(uintptr(fd), "stage-pipe")
		defer stagePipe.Close()
	}

	rootfs := os.Getenv("ROOTFS_PATH")
	if rootfs == "" {
		rootfs = "/alpine"
	}

	// Make / a private mount point so we don't propagate changes.
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to make / a private mount: %w", err)
	}

	// Bind-mount the rootfs to itself so we can pivot-root later.
	if err := unix.Mount(rootfs, rootfs, "bind", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind %s: %w", rootfs, err)
	}

	oldroot, err := unix.Open("/", unix.O_DIRECTORY|unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening old root '/': %w", err)
	}
	defer unix.Close(oldroot)

	newroot, err := unix.Open(rootfs, unix.O_DIRECTORY|unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("error opening new root '%s': %w", rootfs, err)
	}
	defer unix.Close(newroot)

	// Move into the new root so pivot_root operates on "."
	if err := unix.Fchdir(newroot); err != nil {
		return fmt.Errorf("failed to fchdir to new root: %w", err)
	}

	fmt.Printf("INIT (child-stage): pivot_root into %s ...\n", rootfs)
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

	// signal the parent-stage that setup succeeded
	if stagePipe != nil {
		if _, err := stagePipe.Write([]byte{0}); err != nil {
			return fmt.Errorf("failed to signal parent: %w", err)
		}
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

// readInitInfo reads the setup handshake from the init process and returns the
// PID of the child stage. The protocol is a single 0 byte followed by
// "pid:<num>\n".
func readInitInfo(r io.Reader) (int, error) {
	br := bufio.NewReader(r)

	b, err := br.ReadByte()
	if err != nil {
		return 0, err
	}
	if b != 0 {
		return 0, fmt.Errorf("unexpected handshake byte %d != 0", b)
	}

	line, err := br.ReadString('\n')
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(line, "pid:") {
		return 0, fmt.Errorf("unexpected init message %q", strings.TrimSpace(line))
	}
	pidStr := strings.TrimSpace(strings.TrimPrefix(line, "pid:"))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid pid %q: %w", pidStr, err)
	}
	return pid, nil
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
