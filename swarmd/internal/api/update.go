package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/pkg/localupdate"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/update"
)

const (
	updateJobStatusIdle      = "idle"
	updateJobStatusRunning   = "running"
	updateJobStatusCompleted = "completed"
	updateJobStatusFailed    = "failed"

	updateKindRelease = "release"
	updateKindDev     = "dev"

	updateHelperScopeEnv = "SWARM_UPDATE_HELPER_SYSTEMD_SCOPE"
	updateHelperUnitEnv  = "SWARM_UPDATE_HELPER_SYSTEMD_UNIT"
)

type desktopUpdateJob struct {
	ID              string                            `json:"id"`
	Kind            string                            `json:"kind"`
	Status          string                            `json:"status"`
	Message         string                            `json:"message,omitempty"`
	Error           string                            `json:"error,omitempty"`
	Lane            string                            `json:"lane,omitempty"`
	Command         string                            `json:"command,omitempty"`
	HelperPID       int                               `json:"helper_pid,omitempty"`
	LogPath         string                            `json:"log_path,omitempty"`
	Hosts           []localupdate.UpdateJobHostStatus `json:"hosts,omitempty"`
	StartedAtUnix   int64                             `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64                             `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64                             `json:"completed_at_unix_ms,omitempty"`
}

type updateLaunchDetails struct {
	Lane      string
	Command   string
	HelperPID int
	LogPath   string
}

type updateJobRunner struct {
	mu      sync.Mutex
	current desktopUpdateJob
}

var (
	defaultUpdateJobRunner             = &updateJobRunner{}
	execCommandForUpdate               = exec.Command
	execLookPathForUpdate              = exec.LookPath
	osGeteuidForUpdate                 = os.Geteuid
	resolveSwarmLauncherPathForUpdate  = resolveSwarmLauncherPath
	prepareUpdateHelperLaunchForUpdate = prepareUpdateHelperLaunch
)

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	status := s.update.Status(ctx, false)
	s.emitUpdateAvailableNotificationForAccount(principal.AccountScopeID, status)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	plan, err := s.update.Apply(identity.ContextWithPrincipal(r.Context(), principal))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleUpdateRun(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() || strings.TrimSpace(principal.AccountScopeID) == "" {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	ctx := identity.ContextWithPrincipal(r.Context(), principal)
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": defaultUpdateJobRunner.StatusForAccount(principal.AccountScopeID, s)})
	case http.MethodPost:
		job, err := defaultUpdateJobRunner.Start(ctx, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": job})
	default:
		methodNotAllowed(w)
	}
}

func (r *updateJobRunner) Status(s *Server) desktopUpdateJob {
	return r.StatusForAccount("", s)
}

func (r *updateJobRunner) StatusForAccount(accountScopeID string, s *Server) desktopUpdateJob {
	_ = strings.TrimSpace(accountScopeID)
	if r == nil {
		return desktopUpdateJob{Status: updateJobStatusIdle}
	}
	r.mu.Lock()
	current := r.current
	r.mu.Unlock()
	if persisted, ok := s.readPersistedUpdateJobStatus(); ok {
		if persisted.Status == updateJobStatusRunning {
			if kind, err := s.desktopUpdateKind(); err == nil && !updateJobKindMatches(persisted.Kind, kind) {
				return r.supersedeMismatchedRunningJobForAccount(accountScopeID, persisted, kind, s)
			}
		}
		if strings.TrimSpace(current.ID) == "" || persisted.UpdatedAtUnix >= current.UpdatedAtUnix {
			return persisted
		}
	}
	if strings.TrimSpace(current.ID) != "" {
		return current
	}
	return desktopUpdateJob{Status: updateJobStatusIdle}
}

func (r *updateJobRunner) Start(ctx context.Context, s *Server) (desktopUpdateJob, error) {
	if r == nil {
		return desktopUpdateJob{}, errors.New("update runner is not configured")
	}
	kind, err := s.desktopUpdateKind()
	if err != nil {
		return desktopUpdateJob{}, err
	}
	principal, _ := identity.PrincipalFromContext(ctx)
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	statusSnapshot := r.StatusForAccount(accountScopeID, s)
	if statusSnapshot.Status == updateJobStatusRunning {
		if updateJobKindMatches(statusSnapshot.Kind, kind) {
			return statusSnapshot, nil
		}
		r.supersedeMismatchedRunningJobForAccount(accountScopeID, statusSnapshot, kind, s)
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	if r.current.Status == updateJobStatusRunning {
		if strings.TrimSpace(statusSnapshot.ID) != "" && statusSnapshot.ID == r.current.ID && statusSnapshot.Status != updateJobStatusRunning {
			r.current = statusSnapshot
		} else {
			job := r.current
			r.mu.Unlock()
			if updateJobKindMatches(job.Kind, kind) {
				return job, nil
			}
			r.supersedeMismatchedRunningJobForAccount(accountScopeID, job, kind, s)
			r.mu.Lock()
		}
	}
	job := desktopUpdateJob{
		ID:            newUpdateJobID(now, kind),
		Kind:          kind,
		Status:        updateJobStatusRunning,
		Message:       updateStartMessage(kind),
		StartedAtUnix: now,
		UpdatedAtUnix: now,
	}
	r.current = job
	r.mu.Unlock()

	if err := s.writePersistedUpdateJobStatus(job); err != nil {
		failed := r.finish(job.ID, updateJobStatusFailed, "", err.Error(), s)
		s.emitUpdateNotificationForAccount(accountScopeID, failed, pebblestore.NotificationSeverityError, "Swarm update failed", err.Error(), "update.failed")
		return failed, err
	}
	launch, err := s.startDetachedUpdateCommand(ctx, kind, job.ID, r)
	if err != nil {
		failed := r.finish(job.ID, updateJobStatusFailed, "", err.Error(), s)
		s.emitUpdateNotificationForAccount(accountScopeID, failed, pebblestore.NotificationSeverityError, "Swarm update failed", err.Error(), "update.failed")
		return failed, err
	}
	job = r.updateLaunchDetails(job.ID, launch, s)
	return job, nil
}

func updateJobKindMatches(existingKind, desiredKind string) bool {
	return strings.EqualFold(strings.TrimSpace(existingKind), strings.TrimSpace(desiredKind))
}

func (r *updateJobRunner) supersedeMismatchedRunningJob(existing desktopUpdateJob, desiredKind string, s *Server) desktopUpdateJob {
	return r.supersedeMismatchedRunningJobForAccount("", existing, desiredKind, s)
}

func (r *updateJobRunner) supersedeMismatchedRunningJobForAccount(accountScopeID string, existing desktopUpdateJob, desiredKind string, s *Server) desktopUpdateJob {
	_ = strings.TrimSpace(accountScopeID)
	if strings.TrimSpace(existing.ID) == "" {
		return existing
	}
	now := time.Now().UnixMilli()
	failed := existing
	failed.Status = updateJobStatusFailed
	failed.Message = ""
	failed.Error = fmt.Sprintf("superseded stale %s update job because startup config now requires %s update", firstNonEmpty(existing.Kind, "unknown"), strings.TrimSpace(desiredKind))
	failed.UpdatedAtUnix = now
	failed.CompletedAtUnix = now
	if failed.StartedAtUnix == 0 {
		failed.StartedAtUnix = now
	}
	r.mu.Lock()
	if strings.TrimSpace(r.current.ID) == "" || r.current.ID == existing.ID {
		r.current = failed
	}
	r.mu.Unlock()
	_ = s.writePersistedUpdateJobStatus(failed)
	return failed
}

func (r *updateJobRunner) finish(id, status, message, errorMessage string, s *Server) desktopUpdateJob {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.ID != id {
		return r.current
	}
	r.current.Status = status
	r.current.Message = strings.TrimSpace(message)
	r.current.Error = strings.TrimSpace(errorMessage)
	r.current.UpdatedAtUnix = now
	r.current.CompletedAtUnix = now
	_ = s.writePersistedUpdateJobStatus(r.current)
	return r.current
}

func (r *updateJobRunner) updateLaunchDetails(id string, launch updateLaunchDetails, s *Server) desktopUpdateJob {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current.ID != id {
		return r.current
	}
	r.current.Lane = strings.TrimSpace(launch.Lane)
	r.current.Command = strings.TrimSpace(launch.Command)
	r.current.HelperPID = launch.HelperPID
	r.current.LogPath = strings.TrimSpace(launch.LogPath)
	r.current.UpdatedAtUnix = now
	_ = s.writePersistedUpdateJobStatus(r.current)
	return r.current
}

func (s *Server) startDetachedUpdateCommand(ctx context.Context, kind, jobID string, runner *updateJobRunner) (updateLaunchDetails, error) {
	swarmPath, err := resolveSwarmLauncherPathForUpdate()
	if err != nil {
		return updateLaunchDetails{}, err
	}
	lane := updateLaneForKind(kind)
	helperArgs := []string{lane, "update"}
	if kind == updateKindDev {
		helperArgs = append(helperArgs, "dev")
	} else {
		helperArgs = append(helperArgs, "apply")
	}
	env := append(os.Environ(),
		"SWARM_UPDATE_JOB_ID="+strings.TrimSpace(jobID),
		"SWARM_UPDATE_JOB_KIND="+strings.TrimSpace(kind),
	)
	dir := ""
	if kind == updateKindDev {
		devRoot, err := s.configuredDevRoot()
		if err != nil {
			return updateLaunchDetails{}, err
		}
		toolchainEnv, err := devUpdateToolchainEnv(devRoot)
		if err != nil {
			return updateLaunchDetails{}, fmt.Errorf("dev update requires Go toolchain before stopping Swarm: %w", err)
		}
		env = append(env, "SWARM_ROOT="+devRoot)
		for key, value := range toolchainEnv {
			env = append(env, key+"="+value)
		}
		dir = devRoot
	}
	logPath := s.updateHelperLogPath(jobID)
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return updateLaunchDetails{}, fmt.Errorf("prepare update helper log: %w", err)
		}
	}
	launch, err := prepareUpdateHelperLaunchForUpdate(updateHelperLaunchConfig{
		SwarmPath:    swarmPath,
		Args:         helperArgs,
		Env:          env,
		Dir:          dir,
		LogPath:      logPath,
		SystemdUnit:  strings.TrimSpace(os.Getenv("SWARM_SYSTEMD_UNIT")),
		SystemdScope: strings.TrimSpace(os.Getenv("SWARM_SYSTEMD_SCOPE")),
	})
	if err != nil {
		return updateLaunchDetails{}, err
	}
	cmd := execCommandForUpdate(launch.CommandPath, launch.Args...)
	cmd.Env = launch.Env
	cmd.Dir = launch.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if logPath != "" {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return updateLaunchDetails{}, fmt.Errorf("open update helper log: %w", err)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		defer devNull.Close()
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	if err := cmd.Start(); err != nil {
		return updateLaunchDetails{}, fmt.Errorf("start desktop update helper: %w", err)
	}
	details := updateLaunchDetails{
		Lane:      lane,
		Command:   strings.Join(append([]string{swarmPath}, helperArgs...), " "),
		HelperPID: cmd.Process.Pid,
		LogPath:   logPath,
	}
	go s.watchDetachedUpdateCommand(ctx, cmd, strings.TrimSpace(jobID), runner)
	return details, nil
}

type updateHelperLaunchConfig struct {
	SwarmPath    string
	Args         []string
	Env          []string
	Dir          string
	LogPath      string
	SystemdUnit  string
	SystemdScope string
}

type updateHelperLaunchCommand struct {
	CommandPath string
	Args        []string
	Env         []string
	Dir         string
}

func prepareUpdateHelperLaunch(cfg updateHelperLaunchConfig) (updateHelperLaunchCommand, error) {
	command := updateHelperLaunchCommand{
		CommandPath: strings.TrimSpace(cfg.SwarmPath),
		Args:        append([]string(nil), cfg.Args...),
		Env:         append([]string(nil), cfg.Env...),
		Dir:         strings.TrimSpace(cfg.Dir),
	}
	if !shouldLaunchUpdateHelperWithSystemdScope(cfg) {
		return command, nil
	}
	systemdRunPath, err := execLookPathForUpdate("systemd-run")
	if err != nil {
		return updateHelperLaunchCommand{}, errors.New("systemd-run not found; cannot launch update helper outside swarm.service cgroup")
	}
	systemdArgs := []string{
		"--quiet",
		"--collect",
		"--property=KillMode=process",
		"--property=SendSIGHUP=no",
		"--unit=" + updateHelperSystemdScopeUnit(cfg),
	}
	if normalizeUpdateHelperSystemdScope(cfg.SystemdScope) == "system" {
		systemdArgs = append(systemdArgs, "--uid="+currentUpdateHelperUser())
		if osGeteuidForUpdate() == 0 {
			command.CommandPath = systemdRunPath
			command.Args = systemdArgs
		} else {
			sudoPath, err := updateHelperSudoPath()
			if err != nil {
				return updateHelperLaunchCommand{}, err
			}
			command.CommandPath = sudoPath
			command.Args = append([]string{"-n", systemdRunPath}, systemdArgs...)
		}
	} else {
		systemdArgs = append([]string{"--user"}, systemdArgs...)
		command.CommandPath = systemdRunPath
		command.Args = systemdArgs
	}
	if command.Dir != "" {
		command.Args = append(command.Args, "--working-directory="+command.Dir)
	}
	for _, entry := range updateHelperSystemdEnv(command.Env) {
		command.Args = append(command.Args, "--setenv="+entry)
	}
	command.Args = append(command.Args, strings.TrimSpace(cfg.SwarmPath))
	command.Args = append(command.Args, cfg.Args...)
	command.Env = os.Environ()
	command.Dir = ""
	return command, nil
}

func updateHelperSystemdEnv(env []string) []string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		values[strings.TrimSpace(key)] = entry
	}
	allowed := []string{
		"PATH",
		"HOME",
		"USER",
		"LOGNAME",
		"SHELL",
		"STATE_DIRECTORY",
		"CACHE_DIRECTORY",
		"RUNTIME_DIRECTORY",
		"CONFIGURATION_DIRECTORY",
		"LOGS_DIRECTORY",
		"SWARMD_DATA_DIR",
		"SWARMD_CACHE_DIR",
		"SWARMD_RUNTIME_DIR",
		"SWARMD_CONFIG_DIR",
		"SWARMD_LOG_DIR",
		"SWARM_SYSTEMD_SCOPE",
		"SWARM_SYSTEMD_UNIT",
		"SWARM_LANE",
		"SWARM_BIN_DIR",
		"SWARM_TOOL_BIN_DIR",
		"SWARM_ROOT",
		"GO_BIN",
		"GOFMT_BIN",
		"GOROOT",
		"GOTOOLCHAIN",
		"SWARM_REBUILD_REASON",
		"SWARM_UPDATE_JOB_ID",
		"SWARM_UPDATE_JOB_KIND",
	}
	out := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if entry := strings.TrimSpace(values[key]); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

func devUpdateToolchainEnv(root string) (map[string]string, error) {
	goBin, err := findDevUpdateGoBin(root)
	if err != nil {
		return nil, err
	}
	goBinDir := filepath.Dir(goBin)
	env := map[string]string{
		"GO_BIN": goBin,
		"PATH":   prependPathEntry(os.Getenv("PATH"), goBinDir),
	}
	if isExecutable(filepath.Join(goBinDir, "gofmt")) {
		env["GOFMT_BIN"] = filepath.Join(goBinDir, "gofmt")
	}
	if goRoot := resolveGoRoot(goBin); goRoot != "" {
		env["GOROOT"] = goRoot
	}
	if strings.TrimSpace(os.Getenv("GOTOOLCHAIN")) == "" {
		env["GOTOOLCHAIN"] = "auto"
	}
	return env, nil
}

func findDevUpdateGoBin(root string) (string, error) {
	if value := strings.TrimSpace(os.Getenv("GO_BIN")); value != "" && isExecutable(value) {
		return value, nil
	}
	candidates := []string{
		filepath.Join(root, ".tools", "go", "bin", "go"),
		filepath.Join(filepath.Dir(root), ".tools", "go", "bin", "go"),
	}
	for _, candidate := range candidates {
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	if path, err := execLookPathForUpdate("go"); err == nil {
		return path, nil
	}
	return "", errors.New("missing Go toolchain")
}

func isExecutable(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func resolveGoRoot(goBin string) string {
	goBin = strings.TrimSpace(goBin)
	if goBin == "" {
		return ""
	}
	cmd := execCommandForUpdate(goBin, "env", "GOROOT")
	cmd.Env = envWithoutKey(os.Environ(), "GOROOT")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func envWithoutKey(env []string, key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return append([]string(nil), env...)
	}
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func prependPathEntry(existing, entry string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return existing
	}
	cleanEntry := filepath.Clean(entry)
	for _, candidate := range filepath.SplitList(existing) {
		if filepath.Clean(strings.TrimSpace(candidate)) == cleanEntry {
			return existing
		}
	}
	if strings.TrimSpace(existing) == "" {
		return cleanEntry
	}
	return cleanEntry + string(os.PathListSeparator) + existing
}

func updateHelperSudoPath() (string, error) {
	if osGeteuidForUpdate() == 0 {
		return "", nil
	}
	sudoPath, err := execLookPathForUpdate("sudo")
	if err != nil {
		return "", errors.New("sudo not found; cannot launch update helper outside swarm.service cgroup")
	}
	return sudoPath, nil
}

func currentUpdateHelperUser() string {
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fmt.Sprintf("%d", osGeteuidForUpdate())
}

func shouldLaunchUpdateHelperWithSystemdScope(cfg updateHelperLaunchConfig) bool {
	if os.Getenv(updateHelperScopeEnv) == "0" {
		return false
	}
	if strings.TrimSpace(cfg.SwarmPath) == "" {
		return false
	}
	return normalizeUpdateHelperSystemdScope(cfg.SystemdScope) != "" && strings.TrimSpace(cfg.SystemdUnit) != ""
}

func normalizeUpdateHelperSystemdScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "system", "systemd-system":
		return "system"
	case "user", "systemd-user":
		return "user"
	default:
		return ""
	}
}

func updateHelperSystemdScopeUnit(cfg updateHelperLaunchConfig) string {
	if unit := strings.TrimSpace(os.Getenv(updateHelperUnitEnv)); unit != "" {
		return unit
	}
	seed := strings.Join(append([]string{cfg.SwarmPath}, cfg.Args...), " ") + "\x00" + cfg.Dir + "\x00" + cfg.LogPath + "\x00" + time.Now().UTC().Format(time.RFC3339Nano)
	sum := sha1.Sum([]byte(seed))
	return "swarm-update-" + hex.EncodeToString(sum[:])[:12]
}

func (s *Server) watchDetachedUpdateCommand(ctx context.Context, cmd *exec.Cmd, jobID string, runner *updateJobRunner) {
	if cmd == nil || runner == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if err := cmd.Wait(); err != nil {
		if persisted, ok := s.readPersistedUpdateJobStatus(); ok && persisted.ID == jobID && persisted.Status != updateJobStatusRunning {
			return
		}
		failed := runner.finish(jobID, updateJobStatusFailed, "", fmt.Sprintf("update helper exited early: %v", err), s)
		principal, _ := identity.PrincipalFromContext(ctx)
		s.emitUpdateNotificationForAccount(strings.TrimSpace(principal.AccountScopeID), failed, pebblestore.NotificationSeverityError, "Swarm update failed", failed.Error, "update.failed")
	}
}

func (s *Server) desktopUpdateKind() (string, error) {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return "", err
	}
	if cfg.DevMode {
		return updateKindDev, nil
	}
	return updateKindRelease, nil
}

func (s *Server) configuredDevRoot() (string, error) {
	if s == nil {
		return "", errors.New("update server is not configured")
	}
	path := strings.TrimSpace(s.startupConfigPath)
	if path == "" {
		return "", errors.New("startup config path is not configured")
	}
	cfg, err := startupconfig.Load(path)
	if err != nil {
		return "", fmt.Errorf("load startup config: %w", err)
	}
	if !cfg.DevMode {
		return "", errors.New("update dev requires dev_mode=true in swarm.conf")
	}
	devRoot := strings.TrimSpace(cfg.DevRoot)
	if devRoot == "" {
		return "", errors.New("update dev requires dev_root in swarm.conf; run rebuild once from the source checkout")
	}
	resolved, err := resolveUpdateDevRoot(devRoot)
	if err != nil {
		return "", fmt.Errorf("resolve dev_root %q: %w", devRoot, err)
	}
	return resolved, nil
}

func resolveUpdateDevRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	absRoot = filepath.Clean(absRoot)
	for _, path := range []string{
		filepath.Join(absRoot, "go.mod"),
		filepath.Join(absRoot, "cmd", "swarmtui", "main.go"),
		filepath.Join(absRoot, "swarmd", "go.mod"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("missing required path %s", path)
		}
		if info.IsDir() {
			return "", fmt.Errorf("expected file at %s", path)
		}
	}
	return absRoot, nil
}

func (s *Server) readPersistedUpdateJobStatus() (desktopUpdateJob, bool) {
	if s == nil || strings.TrimSpace(s.dataDir) == "" {
		return desktopUpdateJob{}, false
	}
	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(s.dataDir))
	if err != nil || !ok || strings.TrimSpace(status.ID) == "" {
		return desktopUpdateJob{}, false
	}
	return desktopUpdateJob{
		ID:              status.ID,
		Kind:            status.Kind,
		Status:          firstNonEmpty(status.Status, updateJobStatusIdle),
		Message:         status.Message,
		Error:           status.Error,
		Lane:            status.Lane,
		Command:         status.Command,
		HelperPID:       status.HelperPID,
		LogPath:         status.LogPath,
		Hosts:           status.Hosts,
		StartedAtUnix:   status.StartedAtUnix,
		UpdatedAtUnix:   status.UpdatedAtUnix,
		CompletedAtUnix: status.CompletedAtUnix,
	}, true
}

func (s *Server) writePersistedUpdateJobStatus(job desktopUpdateJob) error {
	if s == nil || strings.TrimSpace(s.dataDir) == "" {
		return nil
	}
	return localupdate.WriteUpdateJobStatus(s.dataDir, localupdate.UpdateJobStatus{
		ID:              job.ID,
		Kind:            job.Kind,
		Status:          job.Status,
		Message:         job.Message,
		Error:           job.Error,
		Lane:            job.Lane,
		Command:         job.Command,
		HelperPID:       job.HelperPID,
		LogPath:         job.LogPath,
		Hosts:           job.Hosts,
		StartedAtUnix:   job.StartedAtUnix,
		UpdatedAtUnix:   job.UpdatedAtUnix,
		CompletedAtUnix: job.CompletedAtUnix,
	})
}

func (s *Server) updateHelperLogPath(jobID string) string {
	if s == nil || strings.TrimSpace(s.dataDir) == "" || strings.TrimSpace(jobID) == "" {
		return ""
	}
	return filepath.Join(s.dataDir, "update", "helpers", strings.TrimSpace(jobID)+".log")
}

func updateLaneForKind(kind string) string {
	_ = kind
	lane := strings.ToLower(strings.TrimSpace(os.Getenv("SWARM_LANE")))
	if lane == "dev" {
		return "dev"
	}
	return "main"
}

func resolveSwarmLauncherPath() (string, error) {
	var candidates []string
	addCandidate := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		candidates = append(candidates, filepath.Clean(path))
	}
	if toolBin := strings.TrimSpace(os.Getenv("SWARM_TOOL_BIN_DIR")); toolBin != "" {
		addCandidate(filepath.Join(toolBin, "swarm"))
	}
	if swarmBin := strings.TrimSpace(os.Getenv("SWARM_BIN_DIR")); swarmBin != "" {
		addCandidate(filepath.Join(filepath.Dir(filepath.Clean(swarmBin)), "libexec", "swarm"))
		addCandidate(filepath.Join(swarmBin, "swarm"))
	}
	if path, err := exec.LookPath("swarm"); err == nil {
		addCandidate(path)
	}
	if len(candidates) == 0 {
		return "", errors.New("swarm launcher path is not configured")
	}
	var checked []string
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
		if err != nil {
			checked = append(checked, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		if info.IsDir() {
			checked = append(checked, candidate+": is a directory")
			continue
		}
		checked = append(checked, candidate+": not executable")
	}
	return "", fmt.Errorf("missing executable swarm launcher; checked %s", strings.Join(checked, "; "))
}

func (s *Server) emitUpdateNotification(job desktopUpdateJob, severity, title, body, eventType string) {
	s.emitUpdateNotificationForAccount("", job, severity, title, body, eventType)
}

func (s *Server) emitUpdateNotificationForAccount(accountScopeID string, job desktopUpdateJob, severity, title, body, eventType string) {
	if s == nil || s.notifications == nil {
		return
	}
	now := time.Now().UnixMilli()
	record := pebblestore.NotificationRecord{
		ID:              "update-" + job.ID,
		SwarmID:         s.notifications.LocalSwarmID(),
		OriginSwarmID:   s.notifications.LocalSwarmID(),
		Category:        pebblestore.NotificationCategorySystem,
		Severity:        severity,
		Title:           strings.TrimSpace(title),
		Body:            strings.TrimSpace(body),
		Status:          pebblestore.NotificationStatusActive,
		SourceEventType: strings.TrimSpace(eventType),
		CreatedAt:       firstPositive(job.StartedAtUnix, now),
		UpdatedAt:       now,
	}
	if record.SwarmID == "" {
		return
	}
	if job.Status == updateJobStatusCompleted && severity == pebblestore.NotificationSeverityInfo {
		return
	}
	_, _, _ = notificationServiceForAccount(s.notifications, accountScopeID).UpsertSystemNotification(record)
}

func (s *Server) emitUpdateAvailableNotificationForAccount(accountScopeID string, status update.Status) {
	if s == nil || s.notifications == nil || !status.UpdateAvailable {
		return
	}
	latest := strings.TrimSpace(status.LatestVersion)
	if latest == "" {
		latest = "new release"
	}
	current := strings.TrimSpace(status.CurrentVersion)
	body := fmt.Sprintf("Swarm %s is ready to install.", latest)
	if current != "" {
		body = fmt.Sprintf("Swarm %s is ready to install. Current version: %s.", latest, current)
	}
	if status.Stale {
		body += " Latest check is using cached release data."
	}
	now := time.Now().UnixMilli()
	record := pebblestore.NotificationRecord{
		ID:              "update-available-" + strings.ToLower(latest),
		SwarmID:         s.notifications.LocalSwarmID(),
		OriginSwarmID:   s.notifications.LocalSwarmID(),
		Category:        pebblestore.NotificationCategorySystem,
		Severity:        pebblestore.NotificationSeverityInfo,
		Title:           "Swarm update available",
		Body:            body,
		Status:          pebblestore.NotificationStatusActive,
		SourceEventType: "update.available",
		CreatedAt:       firstPositive(status.CheckedAtUnixMS, now),
		UpdatedAt:       now,
	}
	if record.SwarmID == "" {
		return
	}
	_, _, _ = notificationServiceForAccount(s.notifications, accountScopeID).UpsertSystemNotification(record)
}

func newUpdateJobID(now int64, kind string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%d:%s:%d", now, kind, os.Getpid())))
	return fmt.Sprintf("%d-%s", now, hex.EncodeToString(sum[:4]))
}

func updateStartMessage(kind string) string {
	if kind == updateKindDev {
		return "Starting /update dev helper."
	}
	return "Starting update apply helper."
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (s *Server) SetUpdateService(updateSvc *update.Service) {
	if s == nil {
		return
	}
	s.update = updateSvc
}
