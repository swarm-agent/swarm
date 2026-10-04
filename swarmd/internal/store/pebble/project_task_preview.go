package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cockroachdb/pebble"
	"net/url"
	"strings"
	"time"
)

// GetProjectTaskSummary reads one display projection, never the canonical body.
func (s *SessionStore) GetProjectTaskSummary(account, project, id string) (ProjectTaskRecord, bool, error) {
	var locator string
	ok, err := s.store.GetJSON(taskSummaryPrefix(account, project)+"ids/"+keyPart(id), &locator)
	if err != nil || !ok {
		return ProjectTaskRecord{}, ok, err
	}
	if !strings.HasPrefix(locator, taskSummaryPrefix(account, project)+"rows/") {
		return ProjectTaskRecord{}, false, ErrProjectTaskSummaryCorrupt
	}
	raw, found, err := s.store.GetBytes(locator)
	if err != nil || !found {
		return ProjectTaskRecord{}, false, err
	}
	if len(raw) > taskSummaryMaxBytes {
		return ProjectTaskRecord{}, false, ErrProjectTaskSummaryCorrupt
	}
	var row taskSummaryRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return ProjectTaskRecord{}, false, err
	}
	if row.Version != taskSummaryVersion || row.Task.AccountID != account || row.Task.ProjectID != project || row.Task.ID != id || taskSummaryRowKey(row.Task) != locator {
		return ProjectTaskRecord{}, false, ErrProjectTaskSummaryCorrupt
	}
	return row.Task, true, nil
}

// Fixed global slots bound durable derivative storage to 512 * 80 KiB plus
// metadata. Collisions are misses, never cross-account cache reuse. Entries expire
// after 24 hours; no unbounded per-revision keys or cleanup scan on card reads.
func taskPreviewKey(account, project, task, deliverable, digest string) string {
	identity := fmt.Sprintf("%q", []string{account, project, task, deliverable, digest})
	hash := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("project_task_preview/v2/%03x", (uint16(hash[0])<<8|uint16(hash[1]))%512)
}

type taskPreviewEntry struct {
	Identity []string
	Expires  int64
	Data     []byte
}

func (s *SessionStore) GetProjectTaskPreview(account, project, task, deliverable, digest string) ([]byte, bool, error) {
	var entry taskPreviewEntry
	raw, found, err := s.store.GetBytes(taskPreviewKey(account, project, task, deliverable, digest))
	if err != nil || !found {
		return nil, false, err
	}
	if len(raw) > 120<<10 {
		return nil, false, errors.New("preview cache row exceeds limit")
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, false, err
	}
	want := []string{account, project, task, deliverable, digest}
	if len(entry.Identity) != len(want) || entry.Expires <= time.Now().UnixMilli() || len(entry.Data) == 0 || len(entry.Data) > 80<<10 {
		return nil, false, nil
	}
	for i := range want {
		if entry.Identity[i] != want[i] {
			return nil, false, nil
		}
	}
	return entry.Data, true, nil
}
func (s *SessionStore) PutProjectTaskPreview(account, project, task, deliverable, digest string, data []byte) error {
	for _, id := range []string{account, project, task, deliverable, digest} {
		if id == "" || len(id) > 1024 {
			return errors.New("invalid preview cache identity")
		}
	}
	if len(data) == 0 || len(data) > 80<<10 {
		return errors.New("invalid task preview byte size")
	}
	return s.store.PutJSON(taskPreviewKey(account, project, task, deliverable, digest), taskPreviewEntry{Identity: []string{account, project, task, deliverable, digest}, Expires: time.Now().Add(24 * time.Hour).UnixMilli(), Data: data})
}

// Discard the superseded unbounded derivative namespace at startup only. These
// are disposable previews, never canonical media. Pebble reclaims physical bytes
// through its normal compaction; no request-triggered scan or migration is needed.
func (s *SessionStore) prepareProjectPreviewCache() error {
	return s.store.db.DeleteRange([]byte("project_task_preview/v1/"), []byte("project_task_preview/v1/\xff"), pebble.Sync)
}

// ProjectTaskPreviewReference wraps originals without exposing them as card
// sources. Resolution is restricted by the API; unsupported sources fail closed.
func ProjectTaskPreviewReference(task ProjectTaskRecord, id, field, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("/v3/projects/%s/tasks/%s/deliverables/%s?field=%s&sha256=%x", url.PathEscape(task.ProjectID), url.PathEscape(task.ID), url.PathEscape(id), field, sha256.Sum256([]byte(value)))
}
