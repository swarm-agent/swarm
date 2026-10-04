package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
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
func taskPreviewKey(account, project, task, deliverable, digest string) string {
	return "project_task_preview/v1/" + keyPart(account) + "/" + keyPart(project) + "/" + keyPart(task) + "/" + keyPart(deliverable) + "/" + keyPart(digest)
}
func (s *SessionStore) GetProjectTaskPreview(account, project, task, deliverable, digest string) ([]byte, bool, error) {
	return s.store.GetBytes(taskPreviewKey(account, project, task, deliverable, digest))
}
func (s *SessionStore) PutProjectTaskPreview(account, project, task, deliverable, digest string, data []byte) error {
	if len(data) == 0 || len(data) > 80<<10 {
		return errors.New("invalid task preview byte size")
	}
	return s.store.PutBytes(taskPreviewKey(account, project, task, deliverable, digest), data)
}
