package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The routed container identity is persisted before dispatch, so restart never
// relies on a daemon-local PID (which may have been reused).
type reviewWriter struct {
	Engine    string
	Container string
}

func recordReviewWriter(path string, cmd *exec.Cmd) error {
	w := reviewWriter{}
	if allocationGitRouted(cmd) {
		for i, arg := range cmd.Args {
			if arg == "timeout" && i > 1 {
				w = reviewWriter{cmd.Path, cmd.Args[i-1]}
				break
			}
		}
		if w.Container == "" {
			return ErrAllocationWriterUnconfirmed
		}
	} else {
		// Unrouted writers cannot be inspected through the engine after restart.
		return os.WriteFile(path, []byte("local-writer"), 0600)
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// Only a positively observed stopped container authorizes guard removal. A
// missing/unreachable engine or ambiguous response never authorizes deletion.
func recoverReviewWriter(path string, startup ...bool) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) == "not-started" || (len(startup) > 0 && startup[0] && len(strings.TrimSpace(string(data))) == 0) {
		return os.Remove(path)
	}
	var w reviewWriter
	if json.Unmarshal(data, &w) != nil || w.Engine == "" || w.Container == "" {
		return ErrAllocationWriterUnconfirmed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Engine, "inspect", "--format", "{{.State.Running}}", w.Container)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	// Only the engine's explicit not-found response proves absence; transport
	// errors, permission failures and unavailable daemons remain fail-closed.
	missing := err != nil && ctx.Err() == nil && (strings.Contains(string(out), "No such container:") || strings.Contains(string(out), "No such object:"))
	if !missing && (err != nil || strings.TrimSpace(string(out)) != "false") {
		return ErrAllocationWriterUnconfirmed
	}
	return os.Remove(path)
}
