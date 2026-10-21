package publish

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Script is one owner script Swarm runs on the host: the publish command, or
// a job automation's run step. It runs as the daemon's user, in Dir, with a
// minimal environment (PATH, HOME and TMPDIR set to Scratch) plus Env, in
// its own process group, and nothing else of the daemon's environment.
type Script struct {
	Path string
	Args []string
	Dir  string
	// Env is added after PATH, HOME and TMPDIR, as NAME=VALUE.
	Env []string
	// Scratch is the script's TMPDIR; the caller creates and removes it.
	Scratch        string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Timeout is the time limit; past it, or when ctx is cancelled, the
	// whole process group is killed.
	Timeout time.Duration
	// WaitDelay bounds the wait for the script's output after it was killed
	// (a child that left the process group and still holds it); 0 waits.
	WaitDelay time.Duration
}

// RunScript runs s and returns what exec.Cmd.Run returned. timedOut is true
// when s's own time limit (not ctx) killed it. Callers check the script with
// OwnedByRoot first: RunScript runs whatever path it is given.
func RunScript(ctx context.Context, s Script) (timedOut bool, err error) {
	limited, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(limited, s.Path, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = append([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + s.Scratch,
	}, s.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = s.WaitDelay
	cmd.Stdin = s.Stdin
	cmd.Stdout, cmd.Stderr = s.Stdout, s.Stderr
	err = cmd.Run()
	timedOut = err != nil && ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded)
	return timedOut, err
}

// The reasons OwnedByRoot refuses a file.
var (
	ErrNotRegular    = errors.New("not a regular file")
	ErrNoOwner       = errors.New("cannot read its owner")
	ErrNotRootOwned  = errors.New("must be owned by root")
	ErrWritable      = errors.New("must not be writable by group or others")
	ErrNotExecutable = errors.New("is not executable")
	// ErrChanged: a path component was a symlink when the file was opened,
	// or the opened file is no longer at path: something changed it after
	// it was resolved.
	ErrChanged = errors.New("changed while it was being checked")
)

// OpenOwnedByRoot opens the file at path, an absolute path with no symlinks
// in it (filepath.EvalSymlinks' result), and refuses it unless the opened
// file is still the file at exactly that path (so no symlink or rename was
// followed on the way), then applies OwnedByRoot's rules to the opened file
// itself. The file is returned open for reading (the caller closes it), so
// a caller can use exactly the file that was checked, whatever happens at
// path afterwards. info is the opened file's metadata. A FIFO or device is
// refused without blocking.
func OpenOwnedByRoot(path string, executable bool) (file *os.File, info os.FileInfo, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil, nil, ErrChanged
	}
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file = os.NewFile(uintptr(fd), path)
	fail := func(err error) (*os.File, os.FileInfo, error) {
		file.Close()
		return nil, nil, err
	}
	// The kernel's path for the open file: it differs from path when a
	// symlink was followed or a directory was renamed since path was
	// resolved.
	if at, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd)); err != nil || at != path {
		return fail(ErrChanged)
	}
	if info, err = file.Stat(); err != nil {
		return fail(err)
	}
	if err := checkOwnedByRoot(info, executable); err != nil {
		return fail(err)
	}
	return file, info, nil
}

// OwnedByRoot refuses a file another user could change: it must be a regular
// file owned by root, not writable by group or others and, when executable
// is set, executable. It follows symlinks; callers that run the file resolve
// the path first and run the resolved path.
func OwnedByRoot(path string, executable bool) error {
	return ownedByRoot(path, executable)
}

// AllowOwnerForTests makes OwnedByRoot also accept files owned by uid until
// restore is called. Only tests call it: there is no other way to make a
// test's own files pass the root-owner check.
func AllowOwnerForTests(uid int) (restore func()) {
	previous := allowOwner
	allowOwner = uid
	return func() { allowOwner = previous }
}
