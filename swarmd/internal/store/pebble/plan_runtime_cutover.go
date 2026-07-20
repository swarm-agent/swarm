package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const planRuntimeCutoverMarkerKey = "v3/plan_runtime/v1/cutover_complete"

type PlanRuntimeAuthority struct {
	SchemaVersion      int    `json:"schema_version"`
	SessionID          string `json:"session_id"`
	PlanID             string `json:"plan_id"`
	DefinitionRevision uint64 `json:"definition_revision"`
	SourceHash         string `json:"source_hash,omitempty"`
	ImportedAt         int64  `json:"imported_at"`
}

type PlanRuntimeLegacyImportResult struct {
	Authority PlanRuntimeAuthority `json:"authority"`
	Imported  bool                 `json:"imported"`
}

func KeyPlanRuntimeAuthority(sessionID string) string {
	return fmt.Sprintf("%s/authority/%s", planRuntimeKeyspace, planRuntimeKeyPart(sessionID))
}

func KeyPlanRuntimeLegacyImportManifest(sessionID, planID, sourceHash string) string {
	return fmt.Sprintf("%s/import/%s/%s/%s", planRuntimeKeyspace, planRuntimeKeyPart(sessionID), planRuntimeKeyPart(planID), planRuntimeKeyPart(sourceHash))
}

func (s *SessionStore) PlanRuntimeCutoverComplete() (bool, error) {
	_, ok, err := s.store.GetBytes(planRuntimeCutoverMarkerKey)
	return ok, err
}

func (s *SessionStore) MarkPlanRuntimeCutoverComplete() error {
	return s.store.db.Set([]byte(planRuntimeCutoverMarkerKey), []byte("1"), pebble.Sync)
}

func (s *SessionStore) GetPlanRuntimeAuthority(sessionID string) (PlanRuntimeAuthority, bool, error) {
	var authority PlanRuntimeAuthority
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanRuntimeAuthority(sessionID), &authority)
	return authority, ok, err
}

// ImportLegacyActivePlan atomically installs one immutable definition, a compact
// import event/projection, its manifest, and the session authority pointer. The
// legacy plan record remains historical and is never rewritten by this path.
func (s *SessionStore) ImportLegacyActivePlan(plan SessionPlanSnapshot, now int64) (PlanRuntimeLegacyImportResult, error) {
	if s == nil || s.store == nil {
		return PlanRuntimeLegacyImportResult{}, errors.New("session store is not configured")
	}
	if plan.Document == nil {
		return PlanRuntimeLegacyImportResult{}, errors.New("legacy active plan has no structured document")
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	rawSource, err := json.Marshal(plan)
	if err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	sourceSum := sha256.Sum256(rawSource)
	sourceHash := hex.EncodeToString(sourceSum[:])
	if authority, ok, err := s.GetPlanRuntimeAuthority(plan.SessionID); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	} else if ok {
		if authority.PlanID != plan.ID || authority.SourceHash != sourceHash {
			return PlanRuntimeLegacyImportResult{}, errors.New("session already has a different writable plan runtime authority")
		}
		return PlanRuntimeLegacyImportResult{Authority: authority}, nil
	}

	unlock := s.store.sessionMutations.lockSessions(plan.SessionID)
	defer unlock()
	if authority, ok, err := s.GetPlanRuntimeAuthority(plan.SessionID); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	} else if ok {
		if authority.PlanID != plan.ID || authority.SourceHash != sourceHash {
			return PlanRuntimeLegacyImportResult{}, errors.New("session already has a different writable plan runtime authority")
		}
		return PlanRuntimeLegacyImportResult{Authority: authority}, nil
	}

	definition, checkpoints, subtasks, summary, checkpointRows, subtaskRows, err := legacyPlanRuntimeRows(plan, now)
	if err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	authority := PlanRuntimeAuthority{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, SourceHash: sourceHash, ImportedAt: now}
	delta := PlanExecutionDelta{SummaryChange: summary, NextAction: legacyImportNextAction(summary)}
	event := PlanExecutionEvent{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, ExecutionSeq: 1, EventID: fmt.Sprintf("planexec_%s_%020d", shortPlanRuntimeID(plan.ID), 1), EventType: "plan.legacy_execution_imported", DefinitionRevision: 1, ClientRequestID: "legacy-import-" + sourceHash, PayloadHash: sourceHash, ResultDelta: delta, OccurredAt: now}
	result := PlanExecutionMutationResult{SessionID: plan.SessionID, PlanID: plan.ID, ExecutionSeq: 1, EventID: event.EventID, EventType: event.EventType, SummaryChange: summary, NextAction: delta.NextAction}
	outbox := PlanExecutionRealtimeOutboxRecord{Protocol: PlanExecutionRealtimeProtocol, ProtocolVersion: PlanRuntimeSchemaVersion, Kind: PlanExecutionRealtimeKindDelta, SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, ExecutionSeq: 1, EventID: event.EventID, EventType: event.EventType, SummaryChange: summary, NextAction: delta.NextAction, CreatedAt: now}
	idem := planExecutionIdempotencyRecord{PayloadHash: sourceHash, Result: result, CreatedAt: now}

	batch := s.store.NewBatch()
	defer batch.Close()
	setJSON := func(key string, value any) error {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		return batch.Set([]byte(key), raw, nil)
	}
	if err := setJSON(KeyPlanDefinition(plan.SessionID, plan.ID, 1), definition); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	for _, cp := range checkpoints {
		if err := setJSON(KeyPlanCheckpointDefinition(plan.SessionID, plan.ID, 1, cp.CheckpointID), cp); err != nil {
			return PlanRuntimeLegacyImportResult{}, err
		}
	}
	for _, st := range subtasks {
		if err := setJSON(KeyPlanSubtaskDefinition(plan.SessionID, plan.ID, 1, st.CheckpointID, st.SubtaskID), st); err != nil {
			return PlanRuntimeLegacyImportResult{}, err
		}
	}
	if err := setJSON(KeyPlanExecutionEvent(plan.SessionID, plan.ID, 1), event); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	if err := setJSON(KeyPlanExecutionSummary(plan.SessionID, plan.ID), summary); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	for _, cp := range checkpointRows {
		if err := setJSON(KeyPlanCheckpointExecution(plan.SessionID, plan.ID, cp.CheckpointID), cp); err != nil {
			return PlanRuntimeLegacyImportResult{}, err
		}
	}
	for _, st := range subtaskRows {
		if err := setJSON(KeyPlanSubtaskExecution(plan.SessionID, plan.ID, st.CheckpointID, st.SubtaskID), st); err != nil {
			return PlanRuntimeLegacyImportResult{}, err
		}
	}
	if err := setJSON(KeyPlanExecutionIdempotency(plan.AccountScopeID, plan.SessionID, plan.ID, event.ClientRequestID), idem); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	if err := setJSON(KeyPlanExecutionOutbox(plan.SessionID, plan.ID, 1), outbox); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	if err := setJSON(KeyPlanRuntimeLegacyImportManifest(plan.SessionID, plan.ID, sourceHash), authority); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	if err := setJSON(KeyPlanRuntimeAuthority(plan.SessionID), authority); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return PlanRuntimeLegacyImportResult{}, err
	}
	return PlanRuntimeLegacyImportResult{Authority: authority, Imported: true}, nil
}

func legacyPlanRuntimeRows(plan SessionPlanSnapshot, now int64) (PlanDefinition, []CheckpointDefinition, []SubtaskDefinition, PlanExecutionSummary, []CheckpointExecution, []SubtaskExecution, error) {
	doc := plan.Document
	definition := PlanDefinition{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, Title: plan.Title, Goal: doc.Info.Goal, Scope: doc.Info.Scope, ContinuationDefault: doc.ExecutionPolicy.Mode, CreatedAt: now}
	checkpoints := make([]CheckpointDefinition, 0, len(doc.Checkpoints))
	var subtasks []SubtaskDefinition
	var checkpointRows []CheckpointExecution
	var subtaskRows []SubtaskExecution
	for i, legacy := range doc.Checkpoints {
		checkpointID := strings.TrimSpace(legacy.ID)
		if checkpointID == "" {
			return definition, nil, nil, PlanExecutionSummary{}, nil, nil, fmt.Errorf("legacy checkpoint %d has no stable id", i+1)
		}
		definition.CheckpointOrder = append(definition.CheckpointOrder, checkpointID)
		cp := CheckpointDefinition{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, CheckpointID: checkpointID, Order: i + 1, Title: legacy.Title, Objective: legacy.Objective, Notes: legacy.Notes}
		if i+1 < len(doc.Checkpoints) {
			cp.NextCheckpointID = strings.TrimSpace(doc.Checkpoints[i+1].ID)
		}
		for j, criterion := range legacy.AcceptanceCriteria {
			cp.AcceptanceCriteria = append(cp.AcceptanceCriteria, PlanCriterionDefinition{CriterionID: fmt.Sprintf("criterion-%d", j+1), Text: criterion})
		}
		if len(legacy.Subtasks) == 0 && len(legacy.Tasks) > 0 {
			return definition, nil, nil, PlanExecutionSummary{}, nil, nil, fmt.Errorf("legacy checkpoint %q still relies on positional tasks and cannot be cut over", checkpointID)
		}
		for j, legacySubtask := range legacy.Subtasks {
			subtaskID := strings.TrimSpace(legacySubtask.ID)
			if subtaskID == "" {
				return definition, nil, nil, PlanExecutionSummary{}, nil, nil, fmt.Errorf("legacy checkpoint %q subtask %d has no stable id", checkpointID, j+1)
			}
			cp.SubtaskOrder = append(cp.SubtaskOrder, subtaskID)
			st := SubtaskDefinition{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, CheckpointID: checkpointID, SubtaskID: subtaskID, Order: j + 1, Title: legacySubtask.Title, Notes: legacySubtask.Notes}
			if j+1 < len(legacy.Subtasks) {
				st.NextSubtaskID = strings.TrimSpace(legacy.Subtasks[j+1].ID)
			}
			subtasks = append(subtasks, st)
			if status := strings.TrimSpace(legacySubtask.Status); status != "" && status != "pending" {
				subtaskRows = append(subtaskRows, SubtaskExecution{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, CheckpointID: checkpointID, SubtaskID: subtaskID, ExecutionSeq: 1, Status: status, AttemptID: legacy.AttemptID, StartedAt: legacySubtask.StartedAt, CompletedAt: legacySubtask.CompletedAt})
			}
		}
		checkpoints = append(checkpoints, cp)
		if status := strings.TrimSpace(legacy.Status); status != "" && status != "pending" {
			checkpointRows = append(checkpointRows, CheckpointExecution{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, CheckpointID: checkpointID, ExecutionSeq: 1, Status: status, ActiveAttemptID: legacy.AttemptID, ActiveSubtaskID: legacy.ActiveSubtaskID, RunID: legacy.RunID, RunSessionID: legacy.SessionID, StartedAt: legacy.StartedAt, TerminalAt: legacy.CompletedAt})
		}
	}
	status := "idle"
	if doc.ExecutionState != nil && strings.TrimSpace(doc.ExecutionState.Status) != "" {
		status = strings.TrimSpace(doc.ExecutionState.Status)
	}
	summary := PlanExecutionSummary{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: plan.SessionID, PlanID: plan.ID, DefinitionRevision: 1, ExecutionSeq: 1, Status: status, ActiveCheckpointID: strings.TrimSpace(doc.ActiveCheckpointID), ContinuationMode: doc.ExecutionPolicy.Mode, UpdatedAt: now}
	for _, cp := range doc.Checkpoints {
		if strings.TrimSpace(cp.Status) == "completed" {
			summary.CompletedCheckpointCount++
		}
		if summary.NextCheckpointID == "" && strings.TrimSpace(cp.Status) == "pending" {
			summary.NextCheckpointID = strings.TrimSpace(cp.ID)
		}
	}
	raw, err := json.Marshal(struct {
		Definition  PlanDefinition
		Checkpoints []CheckpointDefinition
		Subtasks    []SubtaskDefinition
	}{definition, checkpoints, subtasks})
	if err != nil {
		return definition, nil, nil, summary, nil, nil, err
	}
	hash := sha256.Sum256(raw)
	definition.ContentHash = hex.EncodeToString(hash[:])
	return definition, checkpoints, subtasks, summary, checkpointRows, subtaskRows, nil
}

func legacyImportNextAction(summary PlanExecutionSummary) string {
	switch summary.Status {
	case "idle":
		return "start_checkpoint"
	case "waiting_review":
		return "await_review"
	case "blocked":
		return "resolve_block"
	case "failed":
		return "retry"
	default:
		return "none"
	}
}
