package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// PlanDefinition is immutable, revisioned plan intent. Execution state is never
// stored in these records.
type PlanDefinition struct {
	SchemaVersion            int      `json:"schema_version"`
	SessionID                string   `json:"session_id"`
	PlanID                   string   `json:"plan_id"`
	DefinitionRevision       uint64   `json:"definition_revision"`
	ParentDefinitionRevision uint64   `json:"parent_definition_revision,omitempty"`
	ContentHash              string   `json:"content_hash"`
	Title                    string   `json:"title"`
	Goal                     string   `json:"goal,omitempty"`
	Scope                    string   `json:"scope,omitempty"`
	ContinuationDefault      string   `json:"continuation_default,omitempty"`
	CheckpointOrder          []string `json:"checkpoint_order"`
	CreatedAt                int64    `json:"created_at"`
	CreatedBy                string   `json:"created_by,omitempty"`
}

type PlanCriterionDefinition struct {
	CriterionID string `json:"criterion_id"`
	Text        string `json:"text"`
}

type CheckpointDefinition struct {
	SchemaVersion      int                       `json:"schema_version"`
	SessionID          string                    `json:"session_id"`
	PlanID             string                    `json:"plan_id"`
	DefinitionRevision uint64                    `json:"definition_revision"`
	CheckpointID       string                    `json:"checkpoint_id"`
	Order              int                       `json:"order"`
	Title              string                    `json:"title"`
	Objective          string                    `json:"objective,omitempty"`
	Notes              string                    `json:"notes,omitempty"`
	AcceptanceCriteria []PlanCriterionDefinition `json:"acceptance_criteria,omitempty"`
	SubtaskOrder       []string                  `json:"subtask_order,omitempty"`
	NextCheckpointID   string                    `json:"next_checkpoint_id,omitempty"`
}

type SubtaskDefinition struct {
	SchemaVersion      int    `json:"schema_version"`
	SessionID          string `json:"session_id"`
	PlanID             string `json:"plan_id"`
	DefinitionRevision uint64 `json:"definition_revision"`
	CheckpointID       string `json:"checkpoint_id"`
	SubtaskID          string `json:"subtask_id"`
	Order              int    `json:"order"`
	Title              string `json:"title"`
	Notes              string `json:"notes,omitempty"`
	NextSubtaskID      string `json:"next_subtask_id,omitempty"`
}

type PlanDefinitionWrite struct {
	Definition  PlanDefinition
	Checkpoints []CheckpointDefinition
	Subtasks    []SubtaskDefinition
}

func KeyPlanDefinition(sessionID, planID string, revision uint64) string {
	return fmt.Sprintf("%s/definition/%020d/header", planRuntimeBase(sessionID, planID), revision)
}
func KeyPlanCheckpointDefinition(sessionID, planID string, revision uint64, checkpointID string) string {
	return fmt.Sprintf("%s/definition/%020d/checkpoint/%s", planRuntimeBase(sessionID, planID), revision, planRuntimeKeyPart(checkpointID))
}
func KeyPlanSubtaskDefinition(sessionID, planID string, revision uint64, checkpointID, subtaskID string) string {
	return fmt.Sprintf("%s/definition/%020d/subtask/%s/%s", planRuntimeBase(sessionID, planID), revision, planRuntimeKeyPart(checkpointID), planRuntimeKeyPart(subtaskID))
}

func (s *SessionStore) GetPlanDefinition(sessionID, planID string, revision uint64) (PlanDefinition, bool, error) {
	var value PlanDefinition
	if s == nil || s.store == nil {
		return value, false, errors.New("session store is not configured")
	}
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanDefinition(sessionID, planID, revision), &value)
	return value, ok, err
}

func (s *SessionStore) GetPlanCheckpointDefinition(sessionID, planID string, revision uint64, checkpointID string) (CheckpointDefinition, bool, error) {
	var value CheckpointDefinition
	if s == nil || s.store == nil {
		return value, false, errors.New("session store is not configured")
	}
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanCheckpointDefinition(sessionID, planID, revision, checkpointID), &value)
	return value, ok, err
}

func (s *SessionStore) GetPlanSubtaskDefinition(sessionID, planID string, revision uint64, checkpointID, subtaskID string) (SubtaskDefinition, bool, error) {
	var value SubtaskDefinition
	if s == nil || s.store == nil {
		return value, false, errors.New("session store is not configured")
	}
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanSubtaskDefinition(sessionID, planID, revision, checkpointID, subtaskID), &value)
	return value, ok, err
}

// PutPlanDefinition writes a normalized immutable definition revision. It is a
// definition-authoring path, not an execution-progress mutation.
func (s *SessionStore) PutPlanDefinition(input PlanDefinitionWrite) (PlanDefinition, error) {
	if s == nil || s.store == nil {
		return PlanDefinition{}, errors.New("session store is not configured")
	}
	definition := input.Definition
	definition.SessionID = strings.TrimSpace(definition.SessionID)
	definition.PlanID = strings.TrimSpace(definition.PlanID)
	if err := validatePlanRuntimeID("session id", definition.SessionID); err != nil {
		return PlanDefinition{}, err
	}
	if err := validatePlanRuntimeID("plan id", definition.PlanID); err != nil {
		return PlanDefinition{}, err
	}
	if definition.DefinitionRevision == 0 {
		return PlanDefinition{}, errors.New("definition revision is required")
	}
	definition.SchemaVersion = PlanRuntimeSchemaVersion
	seenCheckpoints := make(map[string]struct{}, len(input.Checkpoints))
	for i := range input.Checkpoints {
		cp := &input.Checkpoints[i]
		cp.CheckpointID = strings.TrimSpace(cp.CheckpointID)
		if err := validatePlanRuntimeID("checkpoint id", cp.CheckpointID); err != nil {
			return PlanDefinition{}, err
		}
		if _, exists := seenCheckpoints[cp.CheckpointID]; exists {
			return PlanDefinition{}, fmt.Errorf("duplicate checkpoint definition %q", cp.CheckpointID)
		}
		seenCheckpoints[cp.CheckpointID] = struct{}{}
		cp.SchemaVersion, cp.SessionID, cp.PlanID, cp.DefinitionRevision = PlanRuntimeSchemaVersion, definition.SessionID, definition.PlanID, definition.DefinitionRevision
	}
	seenSubtasks := make(map[string]struct{}, len(input.Subtasks))
	for i := range input.Subtasks {
		st := &input.Subtasks[i]
		st.CheckpointID, st.SubtaskID = strings.TrimSpace(st.CheckpointID), strings.TrimSpace(st.SubtaskID)
		if _, exists := seenCheckpoints[st.CheckpointID]; !exists {
			return PlanDefinition{}, fmt.Errorf("subtask %q references unknown checkpoint %q", st.SubtaskID, st.CheckpointID)
		}
		if err := validatePlanRuntimeID("subtask id", st.SubtaskID); err != nil {
			return PlanDefinition{}, err
		}
		key := st.CheckpointID + "\x00" + st.SubtaskID
		if _, exists := seenSubtasks[key]; exists {
			return PlanDefinition{}, fmt.Errorf("duplicate subtask definition %q", st.SubtaskID)
		}
		seenSubtasks[key] = struct{}{}
		st.SchemaVersion, st.SessionID, st.PlanID, st.DefinitionRevision = PlanRuntimeSchemaVersion, definition.SessionID, definition.PlanID, definition.DefinitionRevision
	}
	if len(definition.CheckpointOrder) == 0 {
		definition.CheckpointOrder = make([]string, len(input.Checkpoints))
		for i := range input.Checkpoints {
			definition.CheckpointOrder[i] = input.Checkpoints[i].CheckpointID
		}
	}
	definition.ContentHash = ""
	hashInput := struct {
		Definition  PlanDefinition         `json:"definition"`
		Checkpoints []CheckpointDefinition `json:"checkpoints"`
		Subtasks    []SubtaskDefinition    `json:"subtasks"`
	}{definition, input.Checkpoints, input.Subtasks}
	rawHashInput, err := json.Marshal(hashInput)
	if err != nil {
		return PlanDefinition{}, err
	}
	sum := sha256.Sum256(rawHashInput)
	definition.ContentHash = hex.EncodeToString(sum[:])
	if existing, ok, err := s.GetPlanDefinition(definition.SessionID, definition.PlanID, definition.DefinitionRevision); err != nil {
		return PlanDefinition{}, err
	} else if ok {
		if existing.ContentHash == definition.ContentHash {
			return existing, nil
		}
		return PlanDefinition{}, errors.New("plan definition revision is immutable")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	setJSON := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return batch.Set([]byte(key), raw, nil)
	}
	if err := setJSON(KeyPlanDefinition(definition.SessionID, definition.PlanID, definition.DefinitionRevision), definition); err != nil {
		return PlanDefinition{}, err
	}
	for _, cp := range input.Checkpoints {
		if err := setJSON(KeyPlanCheckpointDefinition(definition.SessionID, definition.PlanID, definition.DefinitionRevision, cp.CheckpointID), cp); err != nil {
			return PlanDefinition{}, err
		}
	}
	for _, st := range input.Subtasks {
		if err := setJSON(KeyPlanSubtaskDefinition(definition.SessionID, definition.PlanID, definition.DefinitionRevision, st.CheckpointID, st.SubtaskID), st); err != nil {
			return PlanDefinition{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return PlanDefinition{}, err
	}
	return definition, nil
}
