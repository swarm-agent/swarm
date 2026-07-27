package localupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const updateJobStatusRelativePath = "update/update-job.json"

// UpdateJobStatus is the durable desktop-visible state for an update helper.
// The helper runs outside swarmd while swarmd restarts, so this file lets the
// restarted backend keep reporting progress until the update helper completes.
type UpdateJobStatus struct {
	ID              string                `json:"id"`
	Kind            string                `json:"kind"`
	Status          string                `json:"status"`
	Message         string                `json:"message,omitempty"`
	Error           string                `json:"error,omitempty"`
	Lane            string                `json:"lane,omitempty"`
	Command         string                `json:"command,omitempty"`
	HelperPID       int                   `json:"helper_pid,omitempty"`
	LogPath         string                `json:"log_path,omitempty"`
	Hosts           []UpdateJobHostStatus `json:"hosts,omitempty"`
	StartedAtUnix   int64                 `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64                 `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64                 `json:"completed_at_unix_ms,omitempty"`
}

type UpdateJobHostStatus struct {
	HostID       string                 `json:"host_id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Role         string                 `json:"role,omitempty"`
	CurrentPhase string                 `json:"current_phase,omitempty"`
	Status       string                 `json:"status,omitempty"`
	Message      string                 `json:"message,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Phases       []UpdateJobHostPhase   `json:"phases,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type UpdateJobHostPhase struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
	StartedAtUnix   int64  `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64  `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64  `json:"completed_at_unix_ms,omitempty"`
}

func UpdateJobStatusPath(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), updateJobStatusRelativePath)
}

func WriteUpdateJobStatus(dataDir string, status UpdateJobStatus) error {
	_, err := writeUpdateJobStatus(dataDir, status, "")
	return err
}

// WriteUpdateJobStatusIfCurrent updates status only while jobID still owns the
// durable status slot. This prevents a detached helper from overwriting a newer
// job after the daemon has restarted.
func WriteUpdateJobStatusIfCurrent(dataDir, jobID string, status UpdateJobStatus) (bool, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false, errors.New("local update job id is required")
	}
	return writeUpdateJobStatus(dataDir, status, jobID)
}

func writeUpdateJobStatus(dataDir string, status UpdateJobStatus, expectedJobID string) (bool, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return false, errors.New("local update job status data dir is required")
	}
	statusPath := UpdateJobStatusPath(dataDir)
	statusDir := filepath.Dir(statusPath)
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		return false, err
	}
	if err := os.Chmod(statusDir, 0o700); err != nil {
		return false, err
	}
	lock, err := os.OpenFile(filepath.Join(statusDir, ".update-job.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := lockUpdateStatusFile(lock); err != nil {
		return false, err
	}
	defer unlockUpdateStatusFile(lock)
	if expectedJobID != "" {
		current, ok, err := ReadUpdateJobStatusPath(statusPath)
		if err != nil {
			return false, err
		}
		if !ok || current.ID != expectedJobID {
			return false, nil
		}
	}
	status.ID = strings.TrimSpace(status.ID)
	status.Kind = strings.TrimSpace(status.Kind)
	status.Status = strings.TrimSpace(status.Status)
	status.Message = strings.TrimSpace(status.Message)
	status.Error = strings.TrimSpace(status.Error)
	status.Lane = strings.TrimSpace(status.Lane)
	status.Command = strings.TrimSpace(status.Command)
	status.LogPath = strings.TrimSpace(status.LogPath)
	for i := range status.Hosts {
		status.Hosts[i] = normalizeUpdateJobHostStatus(status.Hosts[i])
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(statusDir, ".update-job-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, err
	}
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, statusPath); err != nil {
		return false, fmt.Errorf("replace local update job status: %w", err)
	}
	return true, nil
}

func ReadUpdateJobStatusPath(path string) (UpdateJobStatus, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return UpdateJobStatus{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UpdateJobStatus{}, false, nil
		}
		return UpdateJobStatus{}, false, err
	}
	var status UpdateJobStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return UpdateJobStatus{}, false, err
	}
	status.ID = strings.TrimSpace(status.ID)
	status.Kind = strings.TrimSpace(status.Kind)
	status.Status = strings.TrimSpace(status.Status)
	status.Message = strings.TrimSpace(status.Message)
	status.Error = strings.TrimSpace(status.Error)
	status.Lane = strings.TrimSpace(status.Lane)
	status.Command = strings.TrimSpace(status.Command)
	status.LogPath = strings.TrimSpace(status.LogPath)
	for i := range status.Hosts {
		status.Hosts[i] = normalizeUpdateJobHostStatus(status.Hosts[i])
	}
	return status, true, nil
}

func normalizeUpdateJobHostStatus(host UpdateJobHostStatus) UpdateJobHostStatus {
	host.HostID = strings.TrimSpace(host.HostID)
	host.Name = strings.TrimSpace(host.Name)
	host.Role = strings.TrimSpace(host.Role)
	host.CurrentPhase = strings.TrimSpace(host.CurrentPhase)
	host.Status = strings.TrimSpace(host.Status)
	host.Message = strings.TrimSpace(host.Message)
	host.Error = strings.TrimSpace(host.Error)
	for i := range host.Phases {
		host.Phases[i].Name = strings.TrimSpace(host.Phases[i].Name)
		host.Phases[i].Status = strings.TrimSpace(host.Phases[i].Status)
		host.Phases[i].Message = strings.TrimSpace(host.Phases[i].Message)
		host.Phases[i].Error = strings.TrimSpace(host.Phases[i].Error)
	}
	return host
}
