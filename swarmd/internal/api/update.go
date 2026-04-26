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
	StartedAtUnix   int64  `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64  `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64  `json:"completed_at_unix_ms,omitempty"`
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": defaultUpdateJobRunner.Status()})
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

func (r *updateJobRunner) Status() desktopUpdateJob {
	if r == nil {
		return desktopUpdateJob{Status: updateJobStatusIdle}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(r.current.ID) == "" {
		return desktopUpdateJob{Status: updateJobStatusIdle}
	}
	return r.current
}

func (r *updateJobRunner) Start(ctx context.Context, s *Server) (desktopUpdateJob, error) {
	if r == nil {
		return desktopUpdateJob{}, errors.New("update runner is not configured")
	}
	status := s.update.Status(ctx, false)
	kind := updateKindRelease
	if status.DevMode {
		kind = updateKindDev
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	if r.current.Status == updateJobStatusRunning {
		job := r.current
		r.mu.Unlock()
		return job, nil
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

	s.emitUpdateNotification(job, pebblestore.NotificationSeverityInfo, "Swarm update started", job.Message, "update.started")
	if err := startDetachedUpdateCommand(kind); err != nil {
		failed := r.finish(job.ID, updateJobStatusFailed, "", err.Error())
		s.emitUpdateNotification(failed, pebblestore.NotificationSeverityError, "Swarm update failed", err.Error(), "update.failed")
		return failed, err
	}
	return job, nil
}

func (r *updateJobRunner) finish(id, status, message, errorMessage string) desktopUpdateJob {
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
	return r.current
}

func startDetachedUpdateCommand(kind string) error {
	swarmPath, err := resolveSwarmLauncherPath()
	if err != nil {
		return err
	}
	lane := strings.TrimSpace(os.Getenv("SWARM_LANE"))
	if lane == "" {
		lane = "main"
	}
	args := []string{lane, "update"}
	if kind == updateKindDev {
		args = append(args, "dev")
	} else {
		args = append(args, "apply")
	}
	cmd := exec.Command(swarmPath, args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		defer devNull.Close()
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start desktop update helper: %w", err)
	}
	return cmd.Process.Release()
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
		return "Updating Swarm. The desktop will reconnect when Swarm restarts."
	}
	return "Updating Swarm. The desktop will reconnect when Swarm restarts."
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
