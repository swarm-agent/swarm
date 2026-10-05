package pebblestore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

const taskSummaryVersion = 4
const taskSummaryMaxBytes = 256 << 10
const taskSummaryBackfillRows = 32
const taskSummaryBackfillBytes = 16 << 20

// ErrProjectTaskSummariesNotReady never means an empty board. Startup owns
// preparation; readers surface unavailable without advancing migration.
var ErrProjectTaskSummariesNotReady = errors.New("project task summaries not ready")
var ErrProjectTaskSummaryCorrupt = errors.New("project task summary consistency failure")

type ProjectTaskReadStats struct {
	ScannedRows         int
	DecodedBytes        int64
	RelatedRecordReads  int
	RelatedDecodedBytes int64
	ScanElapsed         time.Duration
	RelatedElapsed      time.Duration
	BackfillRows        int
	BackfillBytes       int64
}

type taskSummaryState struct {
	Version int    `json:"version"`
	Cursor  string `json:"cursor,omitempty"`
	Ready   bool   `json:"ready"`
	Counts  [2]int `json:"counts"`
}

type taskSummaryRow struct {
	Version int               `json:"version"`
	Task    ProjectTaskRecord `json:"task"`
}

func taskSummaryPrefix(account, project string) string {
	return "project_task_summary/v3/" + keyPart(account) + "/" + keyPart(project) + "/"
}
func taskSummaryPartition(archived bool) int {
	if archived {
		return 1
	}
	return 0
}
func taskSummaryRowKey(task ProjectTaskRecord) string {
	return fmt.Sprintf("%srows/%d/%016x/%s", taskSummaryPrefix(task.AccountID, task.ProjectID), taskSummaryPartition(task.Archived), uint64(task.CreatedAt)^(uint64(1)<<63), keyPart(task.ID))
}
func taskSummaryBrief(text string) string {
	if len(text) <= 4000 {
		return text
	}
	text = text[:4000]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
func taskSummaryMedia(task ProjectTaskRecord, id, field, value string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:") {
		return value
	}
	return fmt.Sprintf("/v3/projects/%s/tasks/%s/deliverables/%s?field=%s&sha256=%x", url.PathEscape(task.ProjectID), url.PathEscape(task.ID), url.PathEscape(id), field, sha256.Sum256([]byte(value)))
}

func compactProjectTask(task ProjectTaskRecord) ProjectTaskRecord {
	task.Attempts = append([]ProjectTaskAttempt(nil), task.Attempts...)
	task.EnsureTaskAttempts()
	var active []ProjectTaskAttempt
	if a := task.ActiveAttempt(); a != nil {
		copy := *a
		copy.Request, copy.Deliverables = "", nil
		copy.Summary, copy.LastError = taskSummaryBrief(copy.Summary), taskSummaryBrief(copy.LastError)
		active = []ProjectTaskAttempt{copy}
	}
	// Explicit card allowlist: new detail fields must not silently enter the index.
	task = ProjectTaskRecord{
		ID: task.ID, ProjectID: task.ProjectID, AccountID: task.AccountID,
		EnvironmentAttachments: taskEnvironmentProjection(task),
		Title:                  task.Title, Description: task.Description, Status: task.Status,
		SessionID: task.SessionID, OriginSessionID: task.OriginSessionID, ActiveAttemptID: task.ActiveAttemptID, Attempts: active,
		Agent: task.Agent, WorkerID: task.WorkerID, WorkerName: task.WorkerName, WorkerRunID: task.WorkerRunID, AutomationID: task.AutomationID,
		OutcomeType: task.OutcomeType, WorkspacePath: task.WorkspacePath, SourceWorkspace: task.SourceWorkspace,
		WorktreeBranch: task.WorktreeBranch, WorktreeName: task.WorktreeName, BaseBranch: task.BaseBranch, BaseCommit: task.BaseCommit,
		GitStatus: task.GitStatus, UnintegratedCommits: task.UnintegratedCommits, BehindCommits: task.BehindCommits,
		IsIntegrated: task.IsIntegrated, Integration: task.Integration, IsDirty: task.IsDirty, DirtyCount: task.DirtyCount,
		SyncWarning: taskSummaryBrief(task.SyncWarning), ActionNeeded: taskSummaryBrief(task.ActionNeeded),
		PipelineStages: task.PipelineStages, CurrentStageIndex: task.CurrentStageIndex, Deliverables: task.Deliverables,
		PlanSummary: task.PlanSummary, Tier: task.Tier, FeatureSize: task.FeatureSize, Revision: task.Revision,
		Priority: task.Priority, Group: task.Group, Order: task.Order, Archived: task.Archived, LastError: task.LastError,
		AspectRatio: task.AspectRatio, Resolution: task.Resolution, VariantCount: task.VariantCount, DurationSeconds: task.DurationSeconds,
		Model: task.Model, Provider: task.Provider, Thinking: task.Thinking, ServiceTier: task.ServiceTier, ContextMode: task.ContextMode,
		Operation: task.Operation, RouterAlert: taskSummaryBrief(task.RouterAlert), PlanBinding: task.PlanBinding,
		TaskProgramID: task.TaskProgramID, TaskProgram: task.TaskProgram, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
	task.Description, task.LastError = taskSummaryBrief(task.Description), taskSummaryBrief(task.LastError)
	task.PlanSummary, task.ContextPoolSummary = taskSummaryBrief(task.PlanSummary), taskSummaryBrief(task.ContextPoolSummary)
	task.FullPlanMarkdown, task.DiffSummary = "", ""
	task.PlanDocument, task.TaskProgramStatus = nil, nil
	if task.TaskProgramID == "" && task.TaskProgram != nil {
		task.TaskProgramID = task.TaskProgram.ID
	}
	task.TaskProgram, task.CoderAssignments = nil, nil
	task.ImagePrompts, task.Scenes, task.AttachedMedia, task.FeedbackHistory = nil, nil, nil, nil
	task.Soundtrack, task.VideoProvenance, task.DeliveryAssessment = "", nil, nil
	task.Deliverables = append([]ProjectTaskDeliverable(nil), task.Deliverables...)
	for i := range task.Deliverables {
		d := &task.Deliverables[i]
		media, thumbnail := d.MediaURL, d.Thumbnail
		d.MediaURL = taskSummaryMedia(task, d.ID, "media", media)
		field, source := "thumbnail", thumbnail
		if thumbnail == "" || thumbnail == media {
			field, source = "media", media
		}
		d.Thumbnail = ProjectTaskPreviewReference(task, d.ID, field, source)
		d.PreviewSource = ""
		if !strings.HasPrefix(source, "data:") && len(source) <= 4096 {
			d.PreviewSource = source
		}
		d.CodeDiff, d.Description, d.VideoProvenance = "", taskSummaryBrief(d.Description), nil
	}
	return task
}

// Called by the canonical realtime batch for ALL task writes, including plan
// publication. Derived keys are not independently accepted mutation inputs.
func setTaskSummariesInBatch(batch *pebble.Batch, reader pebble.Reader, m *projectRealtimeMutation) error {
	prefix := taskSummaryPrefix(m.accountScopeID, m.projectID)
	var state taskSummaryState
	found, err := getJSONFromReader(reader, prefix+"state", &state)
	if err != nil {
		return err
	}
	if found && state.Version != taskSummaryVersion {
		return ErrProjectTaskSummaryCorrupt
	}
	state.Version = taskSummaryVersion
	if !found {
		canonical := ProjectTaskPrefix(m.accountScopeID, m.projectID)
		iter, err := reader.NewIter(&pebble.IterOptions{LowerBound: []byte(canonical), UpperBound: []byte(canonical + "\xff")})
		if err != nil {
			return err
		}
		state.Ready = !iter.First()
		err = iter.Error()
		iter.Close()
		if err != nil {
			return err
		}
	}
	changed := !found
	apply := func(key string, data []byte) error {
		if !strings.HasPrefix(key, ProjectTaskPrefix(m.accountScopeID, m.projectID)) {
			return nil
		}
		changed = true
		id := strings.TrimPrefix(key, ProjectTaskPrefix(m.accountScopeID, m.projectID))
		locator := prefix + "ids/" + id
		var prior string
		hasPrior, err := getJSONFromReader(reader, locator, &prior)
		if err != nil {
			return err
		}
		if hasPrior {
			partition := -1
			for i := 0; i < 2; i++ {
				if strings.HasPrefix(prior, fmt.Sprintf("%srows/%d/", prefix, i)) {
					partition = i
				}
			}
			if partition < 0 || state.Counts[partition] <= 0 {
				return ErrProjectTaskSummaryCorrupt
			}
			state.Counts[partition]--
			if err := batch.Delete([]byte(prior), nil); err != nil {
				return err
			}
		}
		if data == nil {
			return batch.Delete([]byte(locator), nil)
		}
		var task ProjectTaskRecord
		if err := json.Unmarshal(data, &task); err != nil {
			return err
		}
		if task.AccountID != m.accountScopeID || task.ProjectID != m.projectID || task.ID == "" || KeyProjectTask(task.AccountID, task.ProjectID, task.ID) != key {
			return ErrProjectTaskSummaryCorrupt
		}
		row := taskSummaryRow{Version: taskSummaryVersion, Task: compactProjectTask(task)}
		raw, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if len(raw) > taskSummaryMaxBytes {
			return fmt.Errorf("task summary exceeds %d bytes", taskSummaryMaxBytes)
		}
		rowKey := taskSummaryRowKey(task)
		if err := batch.Set([]byte(rowKey), raw, nil); err != nil {
			return err
		}
		link, _ := json.Marshal(rowKey)
		if err := batch.Set([]byte(locator), link, nil); err != nil {
			return err
		}
		state.Counts[taskSummaryPartition(task.Archived)]++
		return nil
	}
	for key, data := range m.writes {
		if err := apply(key, data); err != nil {
			return err
		}
	}
	for _, key := range m.deletes {
		if err := apply(key, nil); err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return batch.Set([]byte(prefix+"state"), raw, nil)
}

// BackfillProjectTaskSummaries advances 32 records / 16 MiB, allowing one
// oversized record up to the configured media envelope limit (default 256 MiB).
// The cursor and rows commit together; canonical writes share projectsMu.
func (s *SessionStore) BackfillProjectTaskSummaries(account, project string) (ProjectTaskReadStats, error) {
	var stats ProjectTaskReadStats
	if s == nil || s.store == nil || s.store.db == nil {
		return stats, errors.New("database not available")
	}
	account, project = strings.TrimSpace(account), strings.TrimSpace(project)
	if account == "" || project == "" {
		return stats, ErrProjectInvalid
	}
	prefix := taskSummaryPrefix(account, project)
	// Warm boards never wait behind full canonical task writes/hydration.
	var ready taskSummaryState
	if ok, err := getJSONFromReader(s.store.db, prefix+"state", &ready); err != nil {
		return stats, err
	} else if ok && ready.Ready && ready.Version == taskSummaryVersion {
		return stats, nil
	}
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	var state taskSummaryState
	found, err := getJSONFromReader(s.store.db, prefix+"state", &state)
	if err != nil {
		return stats, err
	}
	if found && state.Version != taskSummaryVersion {
		return stats, ErrProjectTaskSummaryCorrupt
	}
	if state.Ready {
		return stats, nil
	}
	canonical := ProjectTaskPrefix(account, project)
	if state.Cursor != "" && !strings.HasPrefix(state.Cursor, canonical) {
		return stats, ErrProjectTaskSummaryCorrupt
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(canonical), UpperBound: []byte(canonical + "\xff")})
	if err != nil {
		return stats, err
	}
	defer iter.Close()
	batch := s.store.db.NewBatch()
	defer batch.Close()
	mutation := &projectRealtimeMutation{accountScopeID: account, projectID: project}
	valid := iter.First()
	if state.Cursor != "" {
		valid = iter.SeekGE([]byte(state.Cursor))
		if valid && string(iter.Key()) == state.Cursor {
			valid = iter.Next()
		}
	}
	maxRecord := int64(256 << 20)
	if value := strings.TrimSpace(os.Getenv("SWARM_PROJECT_MEDIA_MAX_REQUEST_BYTES")); value != "" {
		configured, err := strconv.ParseInt(value, 10, 64)
		if err != nil || configured <= 0 {
			return stats, errors.New("SWARM_PROJECT_MEDIA_MAX_REQUEST_BYTES must be a positive byte count")
		}
		maxRecord = configured
	}
	for valid {
		if int64(len(iter.Value())) > maxRecord {
			return stats, fmt.Errorf("legacy task exceeds configured migration record limit of %d bytes", maxRecord)
		}
		if stats.BackfillRows >= taskSummaryBackfillRows || (stats.BackfillRows > 0 && stats.BackfillBytes+int64(len(iter.Value())) > taskSummaryBackfillBytes) {
			break
		}
		state.Cursor = string(iter.Key())
		mutation.putBytes(state.Cursor, append([]byte(nil), iter.Value()...))
		stats.BackfillRows++
		stats.BackfillBytes += int64(len(iter.Value()))
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return stats, err
	}
	if valid && stats.BackfillRows == 0 {
		return stats, fmt.Errorf("%w: legacy task exceeds migration byte budget", ErrProjectTaskSummariesNotReady)
	}
	if err := setTaskSummariesInBatch(batch, s.store.db, mutation); err != nil {
		return stats, err
	}
	// Recompute partition deltas from the same snapshot to persist cursor and
	// counts in one write, without reading a non-indexed batch.
	for key, raw := range mutation.writes {
		var oldKey string
		ok, err := getJSONFromReader(s.store.db, prefix+"ids/"+strings.TrimPrefix(key, canonical), &oldKey)
		if err != nil {
			return stats, err
		}
		if ok {
			for i := 0; i < 2; i++ {
				if strings.HasPrefix(oldKey, fmt.Sprintf("%srows/%d/", prefix, i)) {
					state.Counts[i]--
				}
			}
		}
		var task ProjectTaskRecord
		if err := json.Unmarshal(raw, &task); err != nil {
			return stats, err
		}
		state.Counts[taskSummaryPartition(task.Archived)]++
	}
	state.Version, state.Ready = taskSummaryVersion, !valid
	raw, err := json.Marshal(state)
	if err != nil {
		return stats, err
	}
	if err := batch.Set([]byte(prefix+"state"), raw, nil); err != nil {
		return stats, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return stats, err
	}
	if !state.Ready {
		return stats, ErrProjectTaskSummariesNotReady
	}
	return stats, nil
}

// ListProjectTaskSummaries reads only the selected compact partition. Records
// are read models, NEVER valid inputs to a whole-record task replacement.
func (s *SessionStore) ListProjectTaskSummaries(account, project string, archived bool) ([]ProjectTaskRecord, ProjectTaskReadStats, error) {
	rows, _, stats, err := s.ListProjectTaskSummariesWithRelated(account, project, archived)
	return rows, stats, err
}

func (s *SessionStore) ListProjectTaskSummariesWithRelated(account, project string, archived bool) ([]ProjectTaskRecord, map[string]ProjectTaskRelatedSummary, ProjectTaskReadStats, error) {
	return s.readProjectTaskSummaries(account, project, archived, nil)
}

func (s *SessionStore) readProjectTaskSummaries(account, project string, archived bool, consume func([]ProjectTaskRecord, *ProjectTaskBoardReader)) ([]ProjectTaskRecord, map[string]ProjectTaskRelatedSummary, ProjectTaskReadStats, error) {
	account, project = strings.TrimSpace(account), strings.TrimSpace(project)
	var stats ProjectTaskReadStats
	if account == "" || project == "" {
		return nil, nil, stats, ErrProjectInvalid
	}
	snapshot := s.store.db.NewSnapshot()
	defer snapshot.Close()
	start := time.Now()
	prefix := taskSummaryPrefix(account, project)
	var state taskSummaryState
	ok, err := getJSONFromReader(snapshot, prefix+"state", &state)
	if err != nil {
		return nil, nil, stats, err
	}
	if !ok {
		canonical := ProjectTaskPrefix(account, project)
		iter, err := snapshot.NewIter(&pebble.IterOptions{LowerBound: []byte(canonical), UpperBound: []byte(canonical + "\xff")})
		if err != nil {
			return nil, nil, stats, err
		}
		empty := !iter.First()
		err = iter.Error()
		iter.Close()
		if err != nil {
			return nil, nil, stats, err
		}
		if empty {
			state = taskSummaryState{Version: taskSummaryVersion, Ready: true}
		}
	}
	if !state.Ready || state.Version != taskSummaryVersion {
		return nil, nil, stats, ErrProjectTaskSummariesNotReady
	}
	partition := taskSummaryPartition(archived)
	rows := make([]ProjectTaskRecord, 0)
	err = scanRangeFromReader(snapshot, scanRangeOptions{Prefix: fmt.Sprintf("%srows/%d/", prefix, partition), Limit: 10001}, func(key string, raw []byte) (bool, error) {
		stats.ScannedRows++
		stats.DecodedBytes += int64(len(raw))
		if len(raw) > taskSummaryMaxBytes || stats.ScannedRows > 10000 || stats.DecodedBytes > 32<<20 {
			return false, ErrProjectTaskSummaryCorrupt
		}
		var row taskSummaryRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return false, err
		}
		if row.Version != taskSummaryVersion || row.Task.AccountID != account || row.Task.ProjectID != project || row.Task.Archived != archived || taskSummaryRowKey(row.Task) != key {
			return false, ErrProjectTaskSummaryCorrupt
		}
		rows = append(rows, row.Task)
		return true, nil
	})
	stats.ScanElapsed = time.Since(start)
	if err != nil {
		return nil, nil, stats, err
	}
	if len(rows) != state.Counts[partition] {
		return nil, nil, stats, ErrProjectTaskSummaryCorrupt
	}
	start = time.Now()
	reader := newProjectTaskBoardReader(snapshot, account, rows, &stats)
	reader.prepare(rows)
	if reader.err != nil {
		return nil, nil, stats, reader.err
	}
	if len(reader.missing) > 0 {
		return nil, nil, stats, ErrProjectTaskSummariesNotReady
	}
	for i := range rows {
		reader.hydrateAttempt(&rows[i])
	}
	if reader.err != nil {
		return nil, nil, stats, reader.err
	}
	if consume != nil {
		consume(rows, reader)
		if reader.err != nil {
			return nil, nil, stats, reader.err
		}
		if len(reader.missing) > 0 {
			return nil, nil, stats, ErrProjectTaskSummariesNotReady
		}
		return rows, nil, stats, nil
	}
	related, err := readProjectTaskRelated(snapshot, account, rows, &stats)
	stats.RelatedElapsed = time.Since(start)
	if err != nil {
		return nil, nil, stats, err
	}
	return rows, related, stats, nil
}
