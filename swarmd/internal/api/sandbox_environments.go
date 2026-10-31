package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//	GET    /v3/sandbox-environments/list                               (sessions:read)
//	PUT    /v3/sandbox-environments  {workspace_path, base_image, setup, env, max_leases}  (sessions:write)
//	PATCH  /v3/sandbox-environments  {workspace_path, base_image?, setup?, append_setup?, env?, unset_env?, max_leases?, wait_seconds?}  (sessions:write)
//	POST   /v3/sandbox-environments/rebuild  {workspace_path, wait_seconds?}  (sessions:write)
//	DELETE /v3/sandbox-environments?workspace_path=P  (sessions:write)
//
// PUT replaces the definition; PATCH merges into it (fields left out are
// kept, append_setup adds commands, env sets variables). Every method goes
// through the service functions below, which the home's linked calls
// (ManageFolderEnvironment, fleet_environments.go) and MCP use as well.
//
// A home reaches these routes over the link as this machine's owner
// (fleet_listener.go). Secret grants and permission rules are never part of
// an environment and are refused by name, over the link or not; views never
// carry secret values.
const (
	SandboxEnvironmentsPath        = "/v3/sandbox-environments"
	sandboxEnvironmentBuildTimeout = 30 * time.Minute
	sandboxEnvironmentMaxWait      = 110
	sandboxEnvironmentBodyMax      = 256 * 1024
)

// sandboxEnvironmentBuilder builds a definition into an image: the machine's
// Docker manager (sandbox.Manager.BuildEnvironment).
type sandboxEnvironmentBuilder interface {
	BuildEnvironment(context.Context, sandbox.EnvironmentBuild) (sandbox.EnvironmentBuildResult, error)
}

// SandboxEnvironmentChange is a merge update of a folder's environment.
// Setup, when present, replaces the commands (an empty list clears them);
// AppendSetup adds commands after the current ones. Env sets variables and
// UnsetEnv removes them; other variables are kept.
type SandboxEnvironmentChange struct {
	WorkspacePath string            `json:"workspace_path"`
	BaseImage     string            `json:"base_image,omitempty"`
	Setup         *[]string         `json:"setup,omitempty"`
	AppendSetup   []string          `json:"append_setup,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	UnsetEnv      []string          `json:"unset_env,omitempty"`
	MaxLeases     *int              `json:"max_leases,omitempty"`
	WaitSeconds   int               `json:"wait_seconds,omitempty"`
	// Replace is update_environment's replace: true, the whole definition
	// (DefineSandboxEnvironment, PUT) instead of the merge (PATCH). It is
	// never part of the PATCH body.
	Replace bool `json:"-"`
}

// sandboxEnvironmentRefusedFields are never part of an environment: secret
// grants go through the secret system and permissions stay on the
// Permissions page of the machine itself.
var sandboxEnvironmentRefusedFields = []string{"secrets", "secret", "secret_grants", "grants", "secret_names", "permissions", "permission", "access"}

// sandboxEnvironmentError is a refusal with its HTTP status.
type sandboxEnvironmentError struct {
	status int
	err    error
}

func (e *sandboxEnvironmentError) Error() string { return e.err.Error() }
func (e *sandboxEnvironmentError) Unwrap() error { return e.err }

func envRefusal(status int, err error) error {
	return &sandboxEnvironmentError{status: status, err: err}
}

func sandboxEnvironmentStatus(err error, fallback int) int {
	var refused *sandboxEnvironmentError
	if errors.As(err, &refused) {
		return refused.status
	}
	if errors.Is(err, pebblestore.ErrSandboxEnvironmentNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, pebblestore.ErrSandboxEnvironmentBuilding) {
		return http.StatusConflict
	}
	return fallback
}

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
	_, remote := fleetHomeFromRequest(r)
	sub := strings.Trim(strings.TrimPrefix(r.URL.Path, SandboxEnvironmentsPath), "/")
	switch {
	case sub == "" && r.Method == http.MethodGet:
		if !s.requireScope(w, r, "sessions:read") {
			return
		}
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait_seconds"))
		view, err := s.GetSandboxEnvironment(r.Context(), principal, r.URL.Query().Get("workspace_path"), wait)
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusInternalServerError), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": view})
	case sub == "list" && r.Method == http.MethodGet:
		if !s.requireScope(w, r, "sessions:read") {
			return
		}
		views, err := s.ListSandboxEnvironments(r.Context(), principal)
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusInternalServerError), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environments": views, "count": len(views)})
	case sub == "" && r.Method == http.MethodPut:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		var req struct {
			WorkspacePath string            `json:"workspace_path"`
			BaseImage     string            `json:"base_image"`
			Setup         []string          `json:"setup,omitempty"`
			Env           map[string]string `json:"env,omitempty"`
			MaxLeases     *int              `json:"max_leases,omitempty"`
		}
		if err := decodeSandboxEnvironmentBody(w, r, remote, &req); err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		view, build, err := s.DefineSandboxEnvironment(r.Context(), principal, req.WorkspacePath, pebblestore.SandboxEnvironmentDefinition{BaseImage: req.BaseImage, Setup: req.Setup, Env: req.Env}, req.MaxLeases)
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "environment": view, "build_started": build})
	case sub == "" && r.Method == http.MethodPatch:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		var change SandboxEnvironmentChange
		if err := decodeSandboxEnvironmentBody(w, r, remote, &change); err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		view, build, err := s.UpdateSandboxEnvironment(r.Context(), principal, change)
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "environment": view, "build_started": build})
	case sub == "rebuild" && r.Method == http.MethodPost:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		var req struct {
			WorkspacePath string `json:"workspace_path"`
			WaitSeconds   int    `json:"wait_seconds,omitempty"`
		}
		if err := decodeSandboxEnvironmentBody(w, r, remote, &req); err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		view, err := s.RebuildSandboxEnvironment(r.Context(), principal, req.WorkspacePath, req.WaitSeconds)
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusBadRequest), err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "environment": view, "build_started": true})
	case sub == "" && r.Method == http.MethodDelete:
		if !s.requireScope(w, r, "sessions:write") {
			return
		}
		view, err := s.ResetSandboxEnvironment(principal, r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, sandboxEnvironmentStatus(err, http.StatusConflict), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "environment": view})
	case sub == "" || sub == "list" || sub == "rebuild":
		methodNotAllowed(w)
	default:
		writeError(w, http.StatusNotFound, errors.New("no such sandbox environment route"))
	}
}

// decodeSandboxEnvironmentBody refuses secret and permission fields by name,
// then decodes the body strictly.
func decodeSandboxEnvironmentBody(w http.ResponseWriter, r *http.Request, remote bool, dst any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sandboxEnvironmentBodyMax))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("invalid json payload: %w", err)
	}
	where := ""
	if remote {
		where = "this machine"
	}
	if err := refuseSandboxEnvironmentFields(fields, where); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid json payload: %w", err)
	}
	return nil
}

// refuseSandboxEnvironmentFields names the secret and permission fields in a
// request and refuses them. where is "" for a local caller, otherwise the
// machine where those are changed (the request crossed the link).
func refuseSandboxEnvironmentFields[V any](fields map[string]V, where string) error {
	refused := []string{}
	for _, name := range sandboxEnvironmentRefusedFields {
		if _, ok := fields[name]; ok {
			refused = append(refused, name)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	if where != "" {
		return envRefusal(http.StatusForbidden, fmt.Errorf("%s refused: secret grants and permissions are never managed over the link; change them on %s", strings.Join(refused, ", "), where))
	}
	return envRefusal(http.StatusBadRequest, fmt.Errorf("%s refused: secret grants and permissions are not part of a folder environment; grant secrets through the secret system and change permissions on the Permissions page", strings.Join(refused, ", ")))
}

func (s *Server) sandboxEnvironmentStore() error {
	if s == nil || s.sandboxEnvironments == nil {
		return envRefusal(http.StatusServiceUnavailable, errors.New("sandbox environments are not configured"))
	}
	return nil
}

// GetSandboxEnvironment is a folder's environment view, waiting up to wait
// seconds while its build runs in this process.
func (s *Server) GetSandboxEnvironment(ctx context.Context, p identity.Principal, path string, wait int) (map[string]any, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, err
	}
	root, err := s.environmentFolder(p, path)
	if err != nil {
		return nil, envRefusal(http.StatusBadRequest, err)
	}
	env, found, err := s.waitSandboxEnvironment(ctx, root, min(max(wait, 0), sandboxEnvironmentMaxWait))
	if err != nil {
		return nil, err
	}
	if found && env.Account != p.AccountScopeID {
		found = false
	}
	view := sandboxEnvironmentView(root, env, found)
	if manager := environmentManager(); manager != nil {
		view["leases_in_use"] = manager.LeasesInUse(root)
	}
	if manager := sandbox.Default(); manager != nil && manager.Active() {
		usage, cacheErr := manager.ProjectCacheUsage(ctx, root)
		view["cache"] = usage
		if cacheErr != nil {
			view["cache_error"] = cacheErr.Error()
		}
	}
	return view, nil
}

// ListSandboxEnvironments is every folder environment of the caller's
// account whose folder is still one of its registered folders.
func (s *Server) ListSandboxEnvironments(_ context.Context, p identity.Principal) ([]map[string]any, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, err
	}
	envs, err := s.sandboxEnvironments.List(p.AccountScopeID)
	if err != nil {
		return nil, err
	}
	views := make([]map[string]any, 0, len(envs))
	for _, env := range envs {
		if _, err := s.environmentFolder(p, env.WorkspacePath); err != nil {
			continue
		}
		view := sandboxEnvironmentView(env.WorkspacePath, env, true)
		delete(view, "log") // list stays short; get returns the log
		views = append(views, view)
	}
	return views, nil // ordered by folder path (SandboxEnvironmentStore.List)
}

// DefineSandboxEnvironment replaces a folder's definition (an empty base
// image keeps the current definition and changes only maxLeases), starts its
// build when needed and sets maxLeases when given. It is the one write path
// of definitions: PUT, PATCH (UpdateSandboxEnvironment) and MCP use it.
func (s *Server) DefineSandboxEnvironment(ctx context.Context, p identity.Principal, path string, def pebblestore.SandboxEnvironmentDefinition, maxLeases *int) (map[string]any, bool, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, false, err
	}
	root, err := s.environmentFolder(p, path)
	if err != nil {
		return nil, false, envRefusal(http.StatusBadRequest, err)
	}
	if s.environmentBuilder() == nil {
		return nil, false, envRefusal(http.StatusConflict, errors.New(sandbox.EnvironmentsOffMessage))
	}
	if strings.TrimSpace(def.BaseImage) == "" {
		current, found, err := s.sandboxEnvironments.Get(root)
		if err != nil {
			return nil, false, err
		}
		if !found || current.Account != p.AccountScopeID {
			return nil, false, envRefusal(http.StatusBadRequest, errors.New("base_image required for a new environment"))
		}
		def = current.Definition
	}
	// Serialize build creation with the update fence. Environment definitions
	// are explicit operations, not durable run admissions.
	s.envBuildMu.Lock()
	if manager := s.ExecutionCapacity(); manager != nil && manager.DrainStatus().Draining {
		s.envBuildMu.Unlock()
		return nil, false, envRefusal(http.StatusConflict, errors.New("daemon is draining; retry environment definition after restart"))
	}
	env, build, err := s.sandboxEnvironments.Define(p.AccountScopeID, root, def)
	s.envBuildMu.Unlock()
	if err != nil {
		return nil, false, envRefusal(http.StatusBadRequest, err)
	}
	if build {
		s.startSandboxEnvironmentBuild(env)
	}
	if maxLeases != nil {
		if env, err = s.sandboxEnvironments.SetMaxLeases(p.AccountScopeID, root, *maxLeases); err != nil {
			return nil, build, envRefusal(http.StatusBadRequest, err)
		}
	}
	return sandboxEnvironmentView(root, env, true), build, nil
}

// UpdateSandboxEnvironment merges change into a folder's definition: fields
// left out are kept. A folder without an environment needs base_image.
func (s *Server) UpdateSandboxEnvironment(ctx context.Context, p identity.Principal, change SandboxEnvironmentChange) (map[string]any, bool, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, false, err
	}
	root, err := s.environmentFolder(p, change.WorkspacePath)
	if err != nil {
		return nil, false, envRefusal(http.StatusBadRequest, err)
	}
	if change.Setup != nil && len(change.AppendSetup) > 0 {
		return nil, false, envRefusal(http.StatusBadRequest, errors.New("give setup (replaces the commands) or append_setup (adds commands), not both"))
	}
	current, found, err := s.sandboxEnvironments.Get(root)
	if err != nil {
		return nil, false, err
	}
	found = found && current.Account == p.AccountScopeID
	def := pebblestore.SandboxEnvironmentDefinition{}
	if found {
		def = current.Definition
	}
	if image := strings.TrimSpace(change.BaseImage); image != "" {
		def.BaseImage = image
	}
	if def.BaseImage == "" {
		return nil, false, envRefusal(http.StatusBadRequest, errors.New("this folder has no environment yet: base_image is required to define one"))
	}
	definitionChanged := change.BaseImage != "" || change.Setup != nil || len(change.AppendSetup) > 0 || len(change.Env) > 0 || len(change.UnsetEnv) > 0
	switch {
	case change.Setup != nil:
		def.Setup = append([]string(nil), (*change.Setup)...)
	case len(change.AppendSetup) > 0:
		def.Setup = append(append([]string(nil), def.Setup...), change.AppendSetup...)
	}
	if len(change.Env) > 0 || len(change.UnsetEnv) > 0 {
		env := make(map[string]string, len(def.Env)+len(change.Env))
		for name, value := range def.Env {
			env[name] = value
		}
		for _, name := range change.UnsetEnv {
			delete(env, strings.TrimSpace(name))
		}
		for name, value := range change.Env {
			env[name] = value
		}
		def.Env = env
	}
	if !definitionChanged && change.MaxLeases == nil {
		return nil, false, envRefusal(http.StatusBadRequest, errors.New("nothing to change: give base_image, setup, append_setup, env, unset_env or max_leases"))
	}
	if !definitionChanged && !found {
		return nil, false, envRefusal(http.StatusNotFound, pebblestore.ErrSandboxEnvironmentNotFound)
	}
	view, build, err := s.DefineSandboxEnvironment(ctx, p, root, def, change.MaxLeases)
	if err != nil || !build || change.WaitSeconds <= 0 {
		return view, build, err
	}
	view, err = s.GetSandboxEnvironment(ctx, p, root, change.WaitSeconds)
	return view, build, err
}

// RebuildSandboxEnvironment builds a folder's current definition again.
func (s *Server) RebuildSandboxEnvironment(ctx context.Context, p identity.Principal, path string, wait int) (map[string]any, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, err
	}
	root, err := s.environmentFolder(p, path)
	if err != nil {
		return nil, envRefusal(http.StatusBadRequest, err)
	}
	if s.environmentBuilder() == nil {
		return nil, envRefusal(http.StatusConflict, errors.New(sandbox.EnvironmentsOffMessage))
	}
	s.envBuildMu.Lock()
	if manager := s.ExecutionCapacity(); manager != nil && manager.DrainStatus().Draining {
		s.envBuildMu.Unlock()
		return nil, envRefusal(http.StatusConflict, errors.New("daemon is draining; retry the rebuild after restart"))
	}
	env, err := s.sandboxEnvironments.Rebuild(p.AccountScopeID, root)
	s.envBuildMu.Unlock()
	if err != nil {
		return nil, err
	}
	s.startSandboxEnvironmentBuild(env)
	if wait > 0 {
		return s.GetSandboxEnvironment(ctx, p, root, wait)
	}
	return sandboxEnvironmentView(root, env, true), nil
}

// ResetSandboxEnvironment removes a folder's environment: it runs on Swarm's
// default again.
func (s *Server) ResetSandboxEnvironment(p identity.Principal, path string) (map[string]any, error) {
	if err := s.sandboxEnvironmentStore(); err != nil {
		return nil, err
	}
	root, err := s.environmentFolder(p, path)
	if err != nil {
		return nil, envRefusal(http.StatusBadRequest, err)
	}
	if err := s.sandboxEnvironments.Clear(p.AccountScopeID, root); err != nil {
		return nil, envRefusal(http.StatusConflict, err)
	}
	return sandboxEnvironmentView(root, pebblestore.SandboxEnvironment{}, false), nil
}

// environmentManager builds and runs folder environments: the machine's Docker
// sandbox when it is on, otherwise the Docker manager for leases. It is nil
// when this machine has no working Docker.
func environmentManager() *sandbox.Manager {
	if m := sandbox.Leases(); m != nil && m.Active() {
		return m
	}
	if m := sandbox.Default(); m != nil && m.Active() && !m.Local() {
		return m
	}
	return nil
}

// environmentBuilder is what builds definitions here, nil when this machine
// cannot (environments are off).
func (s *Server) environmentBuilder() sandboxEnvironmentBuilder {
	if s.envBuilder != nil {
		return s.envBuilder
	}
	if m := environmentManager(); m != nil {
		return m
	}
	return nil
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
	builder := s.environmentBuilder()
	done := s.sandboxEnvironmentWaiter(env.WorkspacePath)
	go func() {
		defer s.finishSandboxEnvironmentWaiter(env.WorkspacePath, done)
		if builder == nil {
			_, _ = s.sandboxEnvironments.Finish(env.WorkspacePath, env.Hash, "", "", "", errors.New(sandbox.EnvironmentsOffMessage))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), sandboxEnvironmentBuildTimeout)
		defer cancel()
		result, err := builder.BuildEnvironment(ctx, sandbox.EnvironmentBuild{
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
	view["max_leases"] = env.MaxLeases
	return view
}
