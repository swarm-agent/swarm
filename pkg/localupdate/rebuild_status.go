package localupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const rebuildStatusRelativePath = "update/local-container-rebuild.json"
const updateJobStatusRelativePath = "update/update-job.json"

// RebuildStatus records the local child-image target produced by a Swarm update.
// It is status-only traceability for local-container update checkpoints; it does
// not imply that existing child containers have been replaced.
type RebuildStatus struct {
	Mode        string `json:"mode,omitempty"`
	Version     string `json:"version,omitempty"`
	ImageRef    string `json:"image_ref,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// UpdateJobStatus is the durable desktop-visible state for an update helper.
// The helper runs outside swarmd while swarmd restarts, so this file lets the
// restarted backend keep reporting "running" until container image propagation
// has been attempted.
type UpdateJobStatus struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
	StartedAtUnix   int64  `json:"started_at_unix_ms,omitempty"`
	UpdatedAtUnix   int64  `json:"updated_at_unix_ms,omitempty"`
	CompletedAtUnix int64  `json:"completed_at_unix_ms,omitempty"`
}

func RebuildStatusPath(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), rebuildStatusRelativePath)
}

func UpdateJobStatusPath(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), updateJobStatusRelativePath)
}

func WriteRebuildStatus(dataDir string, status RebuildStatus) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return errors.New("local update rebuild status data dir is required")
	}
	statusPath := RebuildStatusPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		return err
	}
	status.Mode = strings.TrimSpace(status.Mode)
	status.Version = strings.TrimSpace(status.Version)
	status.ImageRef = strings.TrimSpace(status.ImageRef)
	status.Fingerprint = strings.TrimSpace(status.Fingerprint)
	status.UpdatedAt = strings.TrimSpace(status.UpdatedAt)
	if status.UpdatedAt == "" {
		status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statusPath, append(raw, '\n'), 0o644)
}

func ReadRebuildStatusPath(path string) (RebuildStatus, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return RebuildStatus{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RebuildStatus{}, false, nil
		}
		return RebuildStatus{}, false, err
	}
	var status RebuildStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return RebuildStatus{}, false, err
	}
	status.Mode = strings.TrimSpace(status.Mode)
	status.Version = strings.TrimSpace(status.Version)
	status.ImageRef = strings.TrimSpace(status.ImageRef)
	status.Fingerprint = strings.TrimSpace(status.Fingerprint)
	status.UpdatedAt = strings.TrimSpace(status.UpdatedAt)
	return status, true, nil
}

func WriteUpdateJobStatus(dataDir string, status UpdateJobStatus) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return errors.New("local update job status data dir is required")
	}
	statusPath := UpdateJobStatusPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		return err
	}
	status.ID = strings.TrimSpace(status.ID)
	status.Kind = strings.TrimSpace(status.Kind)
	status.Status = strings.TrimSpace(status.Status)
	status.Message = strings.TrimSpace(status.Message)
	status.Error = strings.TrimSpace(status.Error)
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statusPath, append(raw, '\n'), 0o644)
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
	return status, true, nil
}
