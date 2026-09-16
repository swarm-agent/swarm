package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Access uses the effective daemon identity and ACL-aware kernel checks, not
// HOME, terminal privileges, or ownership alone. No chmod/chown/trust bypass.
func rejectRepositoryHome(path string) error {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return errors.New("daemon account identity unavailable")
	}
	home, err := filepath.EvalSymlinks(account.HomeDir)
	if err == nil && path == home {
		return errors.New("choose a project directory, not home")
	}
	return nil
}

func runtimeRepositoryAccess(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("selected path is not a directory")
	}
	return unix.Faccessat(unix.AT_FDCWD, path, unix.R_OK|unix.W_OK|unix.X_OK, unix.AT_EACCESS)
}

func repositoryFailure(path string, err error) RepositoryState {
	state := RepositoryState{Path: path, State: RepositoryStateError, Message: "Git repository inspection failed; retry or choose another directory", Actions: []string{"retry", "choose_directory"}}
	text := ""
	if err != nil {
		text = strings.ToLower(err.Error())
	}
	switch {
	case errors.Is(err, os.ErrPermission), strings.Contains(text, "permission denied"), strings.Contains(text, "read-only file system"):
		state.State = RepositoryStateAccessDenied
		state.Message = "The daemon account cannot read, write, and traverse this project; choose an accessible directory or use installation access repair"
		state.Actions = []string{"choose_directory", "repair_installation", "retry"}
	case errors.Is(err, os.ErrNotExist):
		state.State = RepositoryStateMissing
		state.Message = "The selected project directory does not exist; create it explicitly or choose another directory"
		state.Actions = []string{"create_directory", "choose_directory"}
	case strings.Contains(text, "dubious ownership"), strings.Contains(text, "unsafe repository"):
		state.State = RepositoryStateTrustRequired
		state.Message = "Git rejected repository ownership; select a repository trusted by the daemon account or repair its installation identity. Swarm will not disable Git trust checks"
		state.Actions = []string{"choose_directory", "repair_installation", "retry"}
	}
	return state
}

// A nonzero Git exit is not evidence that the directory lacks a repository.
// Only Git's locale-pinned absence diagnostic with no local metadata admits init.
func isAbsentRepository(path string, err error) bool {
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		return false
	}
	_, statErr := os.Lstat(path + string(os.PathSeparator) + ".git")
	return errors.Is(statErr, os.ErrNotExist)
}

func repositoryGitExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}

// Bound aggregate command output, including descendants; excess output fails
// rather than accepting a truncated repository or content listing.
type repositoryOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *repositoryOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		b.exceeded = true
		return 0, errors.New("Git output exceeds repository inspection limit")
	}
	return b.Buffer.Write(p)
}
func repositoryEnvironment(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra)+3)
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "GIT_") || key == "LC_ALL" {
			continue
		}
		env = append(env, v)
	}
	// Retain explicit hermetic config isolation, but never inherited index/tree,
	// config injection, alternate object databases, or process routing variables.
	for _, key := range []string{"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

func repositoryCommandError(err error, output string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("git command failed: %s: %w", strings.TrimSpace(output), err)
}
