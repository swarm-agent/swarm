package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// AwaitFolderEnvironmentBuild is the Orchestrator's environment watch
// (manage_projects wait_environment) following one folder's build until it
// ends. It returns get_environment's answer with the log (ready or failed),
// or an error when the build cannot be followed (the folder has no
// environment, linked work was turned off, the machine stayed unreachable).
//
// Here it waits on the build's own end in this process (the waiter that
// startSandboxEnvironmentBuild closes): nothing is read on a timer. On a
// linked machine it asks the machine's get_environment with the longest
// held wait: the machine answers as soon as its build ends, and the next
// hold starts only after the previous one returned still building.
func (s *Server) AwaitFolderEnvironmentBuild(ctx context.Context, p identity.Principal, machine, path string) (json.RawMessage, error) {
	machine, path = strings.TrimSpace(machine), strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("wait_environment needs workspace_path: the absolute path of the folder (list_environments shows them)")
	}
	args := map[string]any{"workspace_path": path, "include_log": true, "wait_seconds": float64(sandboxEnvironmentMaxWait)}
	var unreachableSince time.Time
	notHere := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := s.ManageFolderEnvironment(ctx, p, machine, tool.FolderEnvironmentGet, args)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if machine == "" || !folderEnvironmentUnreachable(err) {
				return nil, err
			}
			// The machine did not answer: try again after a pause, for
			// at most environmentWatchUnreachableGrace.
			if unreachableSince.IsZero() {
				unreachableSince = time.Now()
			}
			if time.Since(unreachableSince) > environmentWatchUnreachableGrace {
				return nil, fmt.Errorf("stopped following the build: %w", err)
			}
			if err := sleepContext(ctx, environmentWatchRetry); err != nil {
				return nil, err
			}
			continue
		}
		unreachableSince = time.Time{}
		status, defined := folderEnvironmentStatus(raw)
		if !defined {
			return nil, fmt.Errorf("%s has no environment (it runs on Swarm's default); there is no build to watch", path)
		}
		if status != pebblestore.SandboxEnvironmentBuilding {
			return raw, nil
		}
		if machine == "" && !s.sandboxEnvironmentBuildRunsHere(path) {
			// Building, but no build of it runs in this process: the
			// waiter is registered right after the definition is stored,
			// so read again shortly, a bounded number of times.
			if notHere++; notHere > environmentWatchNotHereReads {
				return nil, fmt.Errorf("%s says building, but no build of it runs in this Swarm; rebuild it with rebuild_environment", path)
			}
			if err := sleepContext(ctx, time.Second); err != nil {
				return nil, err
			}
		}
	}
}

const (
	environmentWatchRetry            = 30 * time.Second
	environmentWatchUnreachableGrace = 10 * time.Minute
	environmentWatchNotHereReads     = 30
)

// sandboxEnvironmentBuildRunsHere reports whether a build of the folder runs
// in this process (a waiter to wait on).
func (s *Server) sandboxEnvironmentBuildRunsHere(path string) bool {
	root := filepath.Clean(strings.TrimSpace(path))
	s.envBuildMu.Lock()
	defer s.envBuildMu.Unlock()
	return s.envBuildDone[root] != nil
}

func folderEnvironmentStatus(raw json.RawMessage) (string, bool) {
	var answer struct {
		Environment struct {
			Defined bool   `json:"defined"`
			Status  string `json:"status"`
		} `json:"environment"`
	}
	if json.Unmarshal(raw, &answer) != nil {
		return "", false
	}
	return answer.Environment.Status, answer.Environment.Defined
}

// folderEnvironmentUnreachable reports a linked machine that did not answer
// (fleetUnreachableError), as opposed to one that refused the call.
func folderEnvironmentUnreachable(err error) bool {
	var refused *fleet.Error
	if errors.As(err, &refused) {
		return false
	}
	return strings.Contains(err.Error(), "could not reach linked machine")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
