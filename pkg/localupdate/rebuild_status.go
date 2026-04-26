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

func RebuildStatusPath(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), rebuildStatusRelativePath)
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
