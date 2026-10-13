package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Sandbox environments: what a registered folder's sandbox starts from (a
// base image plus setup commands and environment variables), defined by the
// person's AI and built by Swarm under gVisor (sandbox/environment.go).
//
//	GET    /v3/sandbox-environments?workspace_path=P[&wait_seconds=N]  (sessions:read)
//	PUT    /v3/sandbox-environments  {workspace_path, base_image, setup, env}  (sessions:write)
//	DELETE /v3/sandbox-environments?workspace_path=P  (sessions:write)
const (
	SandboxEnvironmentsPath        = "/v3/sandbox-environments"
	sandboxEnvironmentBuildTimeout = 30 * time.Minute
	sandboxEnvironmentMaxWait      = 110
)

func (s *Server) SetSandboxEnvironmentStore(store *pebblestore.SandboxEnvironmentStore) {
	s.sandboxEnvironments = store
}

func (s *Server) handleSandboxEnvironments(w http.ResponseWriter, r *http.Request) {
	if s.sandboxEnvironments == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("sandbox environments are not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !s.requireScope(w, r, "sessions:read") {
			return
		}
		root, err := s.environmentFolder(principal, r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait_seconds"))
		env, found, err := s.waitSandboxEnvironment(r.Context(), root, min(max(wait, 0), sandboxEnvironmentMaxWait))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if found && env.Account != principal.AccountScopeID {
			found = false
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": sandboxEnvironmentView(root, env, found)})
	case http.MethodPut:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		var req struct {
			WorkspacePath string            `json:"workspace_path"`
			BaseImage     string            `json:"base_image"`
			Setup         []string          `json:"setup,omitempty"`
			Env           map[string]string `json:"env,omitempty"`
		}
		if err := decodeJSONStrict(w, r, 256*1024, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		root, err := s.environmentFolder(principal, req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if sandbox.Default() == nil || !sandbox.Default().Active() {
			writeError(w, http.StatusConflict, errors.New("this machine's sandbox is not active, so no environment can be built or used"))
			return
		}
		env, build, err := s.sandboxEnvironments.Define(principal.AccountScopeID, root, pebblestore.SandboxEnvironmentDefinition{BaseImage: req.BaseImage, Setup: req.Setup, Env: req.Env})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if build {
			s.startSandboxEnvironmentBuild(env)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "environment": sandboxEnvironmentView(root, env, true), "build_started": build})
	case http.MethodDelete:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		root, err := s.environmentFolder(principal, r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.sandboxEnvironments.Clear(principal.AccountScopeID, root); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": sandboxEnvironmentView(root, pebblestore.SandboxEnvironment{}, false)})
	default:
		methodNotAllowed(w)
	}
}

// environmentFolder is the caller's registered folder root at path.
func (s *Server) environmentFolder(p identity.Principal, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("workspace_path must be the absolute path of a registered folder")
	}
	if s.workspace == nil {
		return "", errors.New("workspace catalog is unavailable")
	}
	root := filepath.Clean(path)
	scope, err := s.workspace.ScopeForPathForPrincipal(p, root)
	if err != nil || !scope.Matched || scope.WorkspacePath != root {
		return "", errors.New(root + " is not a registered folder root")
	}
	return root, nil
}

// startSandboxEnvironmentBuild builds env's definition in the background and
// records the outcome; waiters on the folder wake when it ends.
func (s *Server) startSandboxEnvironmentBuild(env pebblestore.SandboxEnvironment) {
	done := s.sandboxEnvironmentWaiter(env.WorkspacePath)
	go func() {
		defer s.finishSandboxEnvironmentWaiter(env.WorkspacePath, done)
		ctx, cancel := context.WithTimeout(context.Background(), sandboxEnvironmentBuildTimeout)
		defer cancel()
		result, err := sandbox.Default().BuildEnvironment(ctx, sandbox.EnvironmentBuild{
			Hash:      env.Hash,
			Image:     pebblestore.SandboxEnvironmentImage(env.Hash),
			BaseImage: env.Definition.BaseImage,
			Setup:     env.Definition.Setup,
			Env:       env.Definition.Env,
		})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && err != nil {
			err = errors.New("the build took longer than 30 minutes and was stopped: " + err.Error())
		}
		_, _ = s.sandboxEnvironments.Finish(env.WorkspacePath, env.Hash, result.Image, result.Path, result.Log, err)
	}()
}

func (s *Server) sandboxEnvironmentWaiter(root string) chan struct{} {
	s.envBuildMu.Lock()
	defer s.envBuildMu.Unlock()
	if s.envBuildDone == nil {
		s.envBuildDone = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	s.envBuildDone[root] = done
	return done
}

func (s *Server) finishSandboxEnvironmentWaiter(root string, done chan struct{}) {
	s.envBuildMu.Lock()
	if s.envBuildDone[root] == done {
		delete(s.envBuildDone, root)
	}
	s.envBuildMu.Unlock()
	close(done)
}

// waitSandboxEnvironment reads a folder's environment, waiting up to seconds
// while its build runs in this process.
func (s *Server) waitSandboxEnvironment(ctx context.Context, root string, seconds int) (pebblestore.SandboxEnvironment, bool, error) {
	env, found, err := s.sandboxEnvironments.Get(root)
	if err != nil || !found || env.Status != pebblestore.SandboxEnvironmentBuilding || seconds <= 0 {
		return env, found, err
	}
	s.envBuildMu.Lock()
	done := s.envBuildDone[root]
	s.envBuildMu.Unlock()
	if done != nil {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return s.sandboxEnvironments.Get(root)
}

// sandboxEnvironmentView is the API shape: the definition, its build state,
// what the folder's sandbox runs on now, and the build log tail.
func sandboxEnvironmentView(root string, env pebblestore.SandboxEnvironment, found bool) map[string]any {
	if !found {
		return map[string]any{"workspace_path": root, "defined": false, "runs_on": "Swarm's default environment"}
	}
	view := map[string]any{
		"workspace_path": root,
		"defined":        true,
		"definition":     env.Definition,
		"hash":           env.Hash,
		"status":         env.Status,
		"updated_at":     env.UpdatedAt,
	}
	if env.Error != "" {
		view["error"] = env.Error
	}
	if env.Log != "" {
		view["log"] = env.Log
	}
	switch {
	case env.ReadyImage != "" && env.ReadyHash == env.Hash:
		view["runs_on"] = "this definition (" + env.ReadyImage + ")"
	case env.ReadyImage != "":
		view["runs_on"] = "the previous definition (" + env.ReadyImage + ") until this one is ready"
	default:
		view["runs_on"] = "Swarm's default environment until this one is ready"
	}
	if env.BuiltAt != 0 {
		view["built_at"] = env.BuiltAt
	}
	return view
}
