package publish

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
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
)

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
