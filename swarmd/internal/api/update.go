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

	"swarm-refactor/swarmtui/pkg/devmode"
	"swarm-refactor/swarmtui/pkg/localupdate"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/update"
)

const (
	updateJobStatusIdle    = "idle"
	updateJobStatusRunning = "running"
	updateJobStatusFailed  = "failed"

	updateKindRelease = "release"
	updateKindDev     = "dev"
)

type desktopUpdateJob struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
	Lane            string `json:"lane,omitempty"`
	Command         string `json:"command,omitempty"`
	HelperPID       int    `json:"helper_pid,omitempty"`
	LogPath         string `json:"log_path,omitempty"`
	StartedAtUnix   int64  `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64  `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64  `json:"completed_at_unix_ms,omitempty"`
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

var defaultUpdateJobRunner = &updateJobRunner{}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status := s.update.Status(r.Context(), false)
	s.emitUpdateAvailableNotification(status)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	plan, err := s.update.Apply(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleUpdateLocalContainers(w http.ResponseWriter, r *http.Request) {
	if s.localContainers == nil {
		writeError(w, http.StatusInternalServerError, errors.New("local container service not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	input := localcontainers.UpdatePlanInput{}
	if devModeRaw := strings.TrimSpace(r.URL.Query().Get("dev_mode")); devModeRaw != "" {
		switch strings.ToLower(devModeRaw) {
		case "1", "true", "yes", "dev":
			value := true
			input.DevMode = &value
		case "0", "false", "no", "release", "prod", "production":
			value := false
			input.DevMode = &value
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid dev_mode %q", devModeRaw))
			return
		}
	}
	if postRebuildRaw := strings.TrimSpace(r.URL.Query().Get("post_rebuild_check")); postRebuildRaw != "" {
		switch strings.ToLower(postRebuildRaw) {
		case "1", "true", "yes":
			input.PostRebuildCheck = true
		case "0", "false", "no":
			input.PostRebuildCheck = false
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid post_rebuild_check %q", postRebuildRaw))
			return
		}
	}
	input.TargetVersion = strings.TrimSpace(r.URL.Query().Get("target_version"))
	plan, err := s.localContainers.UpdatePlan(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleUpdateRun(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("update service"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": defaultUpdateJobRunner.Status(s)})
	case http.MethodPost:
		job, err := defaultUpdateJobRunner.Start(r.Context(), s)
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
	if r == nil {
		return desktopUpdateJob{Status: updateJobStatusIdle}
	}
	r.mu.Lock()
	current := r.current
	r.mu.Unlock()
	if persisted, ok := s.readPersistedUpdateJobStatus(); ok {
		if persisted.Status == updateJobStatusRunning {
			if kind, err := s.desktopUpdateKind(); err == nil && !updateJobKindMatches(persisted.Kind, kind) {
				return r.supersedeMismatchedRunningJob(persisted, kind, s)
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
	if existing := r.Status(s); existing.Status == updateJobStatusRunning {
		if updateJobKindMatches(existing.Kind, kind) {
			return existing, nil
		}
		r.supersedeMismatchedRunningJob(existing, kind, s)
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	if r.current.Status == updateJobStatusRunning {
		job := r.current
		r.mu.Unlock()
		if updateJobKindMatches(job.Kind, kind) {
			return job, nil
		}
		r.supersedeMismatchedRunningJob(job, kind, s)
		r.mu.Lock()
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
		s.emitUpdateNotification(failed, pebblestore.NotificationSeverityError, "Swarm update failed", err.Error(), "update.failed")
		return failed, err
	}
	s.emitUpdateNotification(job, pebblestore.NotificationSeverityInfo, "Swarm update started", job.Message, "update.started")
	launch, err := s.startDetachedUpdateCommand(kind, job.ID, r)
	if err != nil {
		failed := r.finish(job.ID, updateJobStatusFailed, "", err.Error(), s)
		s.emitUpdateNotification(failed, pebblestore.NotificationSeverityError, "Swarm update failed", err.Error(), "update.failed")
		return failed, err
	}
	job = r.updateLaunchDetails(job.ID, launch, s)
	return job, nil
}

func updateJobKindMatches(existingKind, desiredKind string) bool {
	return strings.EqualFold(strings.TrimSpace(existingKind), strings.TrimSpace(desiredKind))
}

func (r *updateJobRunner) supersedeMismatchedRunningJob(existing desktopUpdateJob, desiredKind string, s *Server) desktopUpdateJob {
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

func (s *Server) startDetachedUpdateCommand(kind, jobID string, runner *updateJobRunner) (updateLaunchDetails, error) {
	swarmPath, err := resolveSwarmLauncherPath()
	if err != nil {
		return updateLaunchDetails{}, err
	}
	lane := updateLaneForKind(kind)
	args := []string{lane, "update"}
	if kind == updateKindDev {
		args = append(args, "dev")
	} else {
		args = append(args, "apply")
	}
	cmd := exec.Command(swarmPath, args...)
	env := append(os.Environ(),
		"SWARM_UPDATE_JOB_ID="+strings.TrimSpace(jobID),
		"SWARM_UPDATE_JOB_KIND="+strings.TrimSpace(kind),
	)
	if kind == updateKindDev {
		devRoot, err := s.configuredDevRoot()
		if err != nil {
			return updateLaunchDetails{}, err
		}
		env = append(env, "SWARM_ROOT="+devRoot)
		cmd.Dir = devRoot
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logPath := s.updateHelperLogPath(jobID)
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return updateLaunchDetails{}, fmt.Errorf("prepare update helper log: %w", err)
		}
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
	launch := updateLaunchDetails{
		Lane:      lane,
		Command:   strings.Join(append([]string{swarmPath}, args...), " "),
		HelperPID: cmd.Process.Pid,
		LogPath:   logPath,
	}
	go s.watchDetachedUpdateCommand(cmd, strings.TrimSpace(jobID), runner)
	return launch, nil
}

func (s *Server) watchDetachedUpdateCommand(cmd *exec.Cmd, jobID string, runner *updateJobRunner) {
	if cmd == nil || runner == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if err := cmd.Wait(); err != nil {
		if persisted, ok := s.readPersistedUpdateJobStatus(); ok && persisted.ID == jobID && persisted.Status != updateJobStatusRunning {
			return
		}
		failed := runner.finish(jobID, updateJobStatusFailed, "", fmt.Sprintf("update helper exited early: %v", err), s)
		s.emitUpdateNotification(failed, pebblestore.NotificationSeverityError, "Swarm update failed", failed.Error, "update.failed")
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
	resolved, err := devmode.ResolveRoot(devRoot)
	if err != nil {
		return "", fmt.Errorf("resolve dev_root %q: %w", devRoot, err)
	}
	return resolved, nil
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
	if job.Status == updateJobStatusFailed {
		record.Status = pebblestore.NotificationStatusResolved
	}
	_, _, _ = s.notifications.UpsertSystemNotification(record)
}

func (s *Server) emitUpdateAvailableNotification(status update.Status) {
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
	_, _, _ = s.notifications.UpsertSystemNotification(record)
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
