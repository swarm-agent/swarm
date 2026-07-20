package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PlanRuntimeActionActivate          = "activate"
	PlanRuntimeActionStartCheckpoint   = "start_checkpoint"
	PlanRuntimeActionFocusSubtask      = "focus_subtask"
	PlanRuntimeActionCompleteSubtasks  = "complete_subtasks"
	PlanRuntimeActionCheckpointOutcome = "checkpoint_outcome"
)

type PlanRuntimeExecutionInput struct {
	Action               string
	SessionID            string
	PlanID               string
	AccountScopeID       string
	ActorID              string
	DefinitionRevision   uint64
	ExpectedExecutionSeq uint64
	ClientRequestID      string
	CheckpointID         string
	SubtaskIDs           []string
	CompleteCheckpoint   bool
	NextSubtaskID        string
	AttemptID            string
	Outcome              string
	EvidenceRef          string
	NextAction           string
	RunID                string
	EpochID              string
	RunSessionID         string
	ParentSessionID      string
	NowUnixMs            int64
}

// PlanRuntimeExecutionReceipt is deliberately bounded. It is safe for tool
// output and durable provider tool history and cannot carry a definition or a
// complete execution projection.
type PlanRuntimeExecutionReceipt struct {
	PlanID             string                                 `json:"plan_id"`
	ExecutionSeq       uint64                                 `json:"execution_seq"`
	HighWaterMark      uint64                                 `json:"high_water_mark"`
	EventID            string                                 `json:"event_id"`
	EventType          string                                 `json:"event_type"`
	Replayed           bool                                   `json:"replayed,omitempty"`
	PlanStatus         string                                 `json:"plan_status"`
	ActiveCheckpointID string                                 `json:"active_checkpoint_id,omitempty"`
	NextCheckpointID   string                                 `json:"next_checkpoint_id,omitempty"`
	CheckpointID       string                                 `json:"checkpoint_id,omitempty"`
	CheckpointStatus   string                                 `json:"checkpoint_status,omitempty"`
	ChangedSubtasks    []PlanRuntimeSubtaskReceipt            `json:"changed_subtasks,omitempty"`
	RunEpochLink       *pebblestore.PlanExecutionRunEpochLink `json:"run_epoch_link,omitempty"`
	NextAction         string                                 `json:"next_action,omitempty"`
}

type PlanRuntimeSubtaskReceipt struct {
	SubtaskID string `json:"subtask_id"`
	Status    string `json:"status"`
}

type PlanRuntimeCommandService struct{ store *pebblestore.SessionStore }

func NewPlanRuntimeCommandService(store *pebblestore.SessionStore) *PlanRuntimeCommandService {
	return &PlanRuntimeCommandService{store: store}
}

func (s *PlanRuntimeCommandService) Execute(input PlanRuntimeExecutionInput) (PlanRuntimeExecutionReceipt, error) {
	if s == nil || s.store == nil {
		return PlanRuntimeExecutionReceipt{}, errors.New("plan runtime command service is not configured")
	}
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.SessionID, input.PlanID = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.PlanID)
	input.CheckpointID, input.AttemptID = strings.TrimSpace(input.CheckpointID), strings.TrimSpace(input.AttemptID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	if input.SessionID == "" || input.PlanID == "" || input.ClientRequestID == "" {
		return PlanRuntimeExecutionReceipt{}, errors.New("session_id, plan_id, and client_request_id are required")
	}
	if input.DefinitionRevision == 0 {
		return PlanRuntimeExecutionReceipt{}, errors.New("definition_revision is required")
	}
	definition, ok, err := s.store.GetPlanDefinition(input.SessionID, input.PlanID, input.DefinitionRevision)
	if err != nil {
		return PlanRuntimeExecutionReceipt{}, err
	}
	if !ok {
		return PlanRuntimeExecutionReceipt{}, fmt.Errorf("plan definition %s revision %d not found", input.PlanID, input.DefinitionRevision)
	}
	current, currentExists, err := s.store.GetPlanExecutionSummary(input.SessionID, input.PlanID)
	if err != nil {
		return PlanRuntimeExecutionReceipt{}, err
	}
	if currentExists && current.DefinitionRevision != input.DefinitionRevision {
		return PlanRuntimeExecutionReceipt{}, errors.New("execution references a different immutable definition revision")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	next := current
	command := pebblestore.PlanExecutionCommand{SessionID: input.SessionID, PlanID: input.PlanID, AccountScopeID: input.AccountScopeID, ExpectedExecutionSeq: input.ExpectedExecutionSeq, ClientRequestID: input.ClientRequestID, ActorID: input.ActorID, DefinitionRevision: input.DefinitionRevision, CheckpointID: input.CheckpointID, NextAction: strings.TrimSpace(input.NextAction), NowUnixMs: now}
	var checkpoint *pebblestore.CheckpointExecution
	var subtasks []pebblestore.SubtaskExecution

	switch input.Action {
	case PlanRuntimeActionActivate:
		if currentExists {
			return PlanRuntimeExecutionReceipt{}, errors.New("plan execution is already activated")
		}
		if input.ExpectedExecutionSeq != 0 {
			return PlanRuntimeExecutionReceipt{}, errors.New("plan activation requires expected_execution_seq 0")
		}
		next = pebblestore.PlanExecutionSummary{Status: "idle", NextCheckpointID: firstRuntimeID(definition.CheckpointOrder), ContinuationMode: definition.ContinuationDefault}
		command.EventType = "plan.execution_activated"
		if command.NextAction == "" {
			command.NextAction = "start_checkpoint"
		}
	case PlanRuntimeActionStartCheckpoint:
		cpDef, err := s.requireCheckpoint(input)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		if current.Status != "idle" && current.Status != "paused" {
			return PlanRuntimeExecutionReceipt{}, fmt.Errorf("cannot start checkpoint while execution is %q", current.Status)
		}
		if current.NextCheckpointID != "" && current.NextCheckpointID != input.CheckpointID {
			return PlanRuntimeExecutionReceipt{}, fmt.Errorf("checkpoint %q is not the named next checkpoint", input.CheckpointID)
		}
		if input.AttemptID == "" || strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.EpochID) == "" {
			return PlanRuntimeExecutionReceipt{}, errors.New("start_checkpoint requires attempt_id, run_id, and an epoch_id created by the execution-epoch authority")
		}
		if epoch, ok, err := s.store.GetExecutionEpoch(input.SessionID, input.EpochID); err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		} else if !ok {
			return PlanRuntimeExecutionReceipt{}, fmt.Errorf("execution epoch %q not found", input.EpochID)
		} else if epoch.Boundary.PlanID != input.PlanID || epoch.Boundary.CheckpointID != input.CheckpointID || epoch.Boundary.AttemptID != input.AttemptID || epoch.Boundary.RunID != input.RunID {
			return PlanRuntimeExecutionReceipt{}, errors.New("execution epoch linkage does not match the named plan/checkpoint/attempt/run")
		}
		runIntent, ok, err := s.store.GetV3SessionRunIntent(input.SessionID, input.RunID)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		if !ok || runIntent.EpochID != input.EpochID || runIntent.PlanID != input.PlanID || runIntent.CheckpointID != input.CheckpointID || runIntent.AttemptID != input.AttemptID {
			return PlanRuntimeExecutionReceipt{}, errors.New("run intent authority does not match the named epoch/plan/checkpoint/attempt")
		}
		attemptNumber := uint64(1)
		if prior, ok, err := s.store.GetPlanCheckpointExecution(input.SessionID, input.PlanID, input.CheckpointID); err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		} else if ok {
			attemptNumber = prior.AttemptNumber + 1
		}
		checkpoint = &pebblestore.CheckpointExecution{CheckpointID: input.CheckpointID, Status: "in_progress", AttemptNumber: attemptNumber, ActiveAttemptID: input.AttemptID, NextSubtaskID: firstRuntimeID(cpDef.SubtaskOrder), RunID: input.RunID, EpochID: input.EpochID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID, StartedAt: now}
		next.Status, next.ActiveCheckpointID, next.ActiveAttemptID, next.NextCheckpointID = "in_progress", input.CheckpointID, input.AttemptID, ""
		command.EventType = "plan.checkpoint_started"
		command.RunEpochLink = &pebblestore.PlanExecutionRunEpochLink{CheckpointID: input.CheckpointID, AttemptID: input.AttemptID, RunID: input.RunID, EpochID: input.EpochID, RunSessionID: input.RunSessionID, ParentSessionID: input.ParentSessionID}
	case PlanRuntimeActionFocusSubtask:
		cp, err := s.requireActiveCheckpoint(input, current)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		if len(input.SubtaskIDs) != 1 {
			return PlanRuntimeExecutionReceipt{}, errors.New("focus_subtask requires exactly one subtask_id")
		}
		if _, err := s.requireSubtask(input, input.SubtaskIDs[0]); err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		st, ok, err := s.store.GetPlanSubtaskExecution(input.SessionID, input.PlanID, input.CheckpointID, input.SubtaskIDs[0])
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		if ok && st.Status == "completed" {
			return PlanRuntimeExecutionReceipt{}, errors.New("completed subtask cannot be focused")
		}
		st = pebblestore.SubtaskExecution{CheckpointID: input.CheckpointID, SubtaskID: input.SubtaskIDs[0], Status: "in_progress", AttemptID: cp.ActiveAttemptID, StartedAt: now}
		subtasks = []pebblestore.SubtaskExecution{st}
		cp.ActiveSubtaskID = st.SubtaskID
		cp.NextSubtaskID = st.SubtaskID
		checkpoint = &cp
		command.EventType = "plan.subtask_focused"
	case PlanRuntimeActionCompleteSubtasks:
		cp, err := s.requireActiveCheckpoint(input, current)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		cpDef, err := s.requireCheckpoint(input)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		if len(input.SubtaskIDs) == 0 || len(input.SubtaskIDs) > pebblestore.PlanRuntimeMaxSubtaskChanges {
			return PlanRuntimeExecutionReceipt{}, fmt.Errorf("complete_subtasks requires 1-%d named subtask ids", pebblestore.PlanRuntimeMaxSubtaskChanges)
		}
		seen := make(map[string]struct{}, len(input.SubtaskIDs))
		for _, id := range input.SubtaskIDs {
			id = strings.TrimSpace(id)
			if _, duplicate := seen[id]; duplicate {
				return PlanRuntimeExecutionReceipt{}, fmt.Errorf("duplicate subtask target %q", id)
			}
			seen[id] = struct{}{}
			if _, err := s.requireSubtask(input, id); err != nil {
				return PlanRuntimeExecutionReceipt{}, err
			}
			prior, ok, err := s.store.GetPlanSubtaskExecution(input.SessionID, input.PlanID, input.CheckpointID, id)
			if err != nil {
				return PlanRuntimeExecutionReceipt{}, err
			}
			if ok && prior.Status == "completed" {
				return PlanRuntimeExecutionReceipt{}, fmt.Errorf("subtask %q is already completed", id)
			}
			subtasks = append(subtasks, pebblestore.SubtaskExecution{CheckpointID: input.CheckpointID, SubtaskID: id, Status: "completed", AttemptID: cp.ActiveAttemptID, StartedAt: prior.StartedAt, CompletedAt: now})
		}
		if input.CompleteCheckpoint {
			for _, id := range cpDef.SubtaskOrder {
				if _, completedNow := seen[id]; completedNow {
					continue
				}
				prior, ok, getErr := s.store.GetPlanSubtaskExecution(input.SessionID, input.PlanID, input.CheckpointID, id)
				if getErr != nil {
					return PlanRuntimeExecutionReceipt{}, getErr
				}
				if !ok || prior.Status != "completed" {
					return PlanRuntimeExecutionReceipt{}, fmt.Errorf("checkpoint %q cannot complete while subtask %q is incomplete", input.CheckpointID, id)
				}
			}
			cp.Status, cp.TerminalAt, cp.OutcomeCode, cp.ActiveSubtaskID, cp.NextSubtaskID = "completed", now, "completed", "", ""
			next.CompletedCheckpointCount++
			next.ActiveCheckpointID, next.ActiveAttemptID = "", ""
			next.NextCheckpointID = cpDef.NextCheckpointID
			if next.NextCheckpointID == "" {
				next.Status = "completed"
			} else {
				next.Status = "idle"
			}
			command.NextAction = strings.TrimSpace(input.NextAction)
			if command.NextAction == "" && next.NextCheckpointID != "" {
				command.NextAction = "start_checkpoint"
			}
		} else if input.NextSubtaskID != "" {
			if _, err := s.requireSubtask(input, input.NextSubtaskID); err != nil {
				return PlanRuntimeExecutionReceipt{}, err
			}
			subtasks = append(subtasks, pebblestore.SubtaskExecution{CheckpointID: input.CheckpointID, SubtaskID: input.NextSubtaskID, Status: "in_progress", AttemptID: cp.ActiveAttemptID, StartedAt: now})
			cp.ActiveSubtaskID, cp.NextSubtaskID = input.NextSubtaskID, input.NextSubtaskID
		} else {
			cp.ActiveSubtaskID = ""
		}
		checkpoint = &cp
		command.EventType = "plan.subtasks_completed"
	case PlanRuntimeActionCheckpointOutcome:
		cp, err := s.requireActiveCheckpoint(input, current)
		if err != nil {
			return PlanRuntimeExecutionReceipt{}, err
		}
		outcome := strings.ToLower(strings.TrimSpace(input.Outcome))
		eventType := "plan.checkpoint_outcome_recorded"
		switch outcome {
		case "completed":
			cp.Status, cp.TerminalAt, cp.OutcomeCode, cp.ActiveSubtaskID = "completed", now, outcome, ""
			next.CompletedCheckpointCount++
			next.ActiveCheckpointID, next.ActiveAttemptID = "", ""
			next.NextCheckpointID = cpDefNext(s.store, input)
			if next.NextCheckpointID == "" {
				next.Status = "completed"
			} else {
				next.Status = "idle"
			}
		case "paused":
			cp.Status, cp.TerminalAt, cp.OutcomeCode, next.Status = "paused", now, outcome, "paused"
		case "blocked":
			cp.Status, cp.TerminalAt, cp.OutcomeCode, next.Status, next.BlockedReasonCode = "blocked", now, outcome, "blocked", strings.TrimSpace(input.EvidenceRef)
		case "failed":
			cp.Status, cp.TerminalAt, cp.OutcomeCode, next.Status = "failed", now, outcome, "failed"
		case "needs_review":
			cp.Status, cp.TerminalAt, cp.OutcomeCode, cp.ReviewState, next.Status, eventType = "needs_review", now, outcome, "pending", "waiting_review", "plan.checkpoint_review_requested"
		default:
			return PlanRuntimeExecutionReceipt{}, fmt.Errorf("unsupported checkpoint outcome %q", outcome)
		}
		cp.EvidenceRef = strings.TrimSpace(input.EvidenceRef)
		checkpoint = &cp
		command.EventType = eventType
	default:
		return PlanRuntimeExecutionReceipt{}, fmt.Errorf("unsupported plan runtime execution action %q", input.Action)
	}
	command.NextSummary, command.CheckpointChange, command.SubtaskChanges = next, checkpoint, subtasks
	hashBody := input
	hashBody.AccountScopeID, hashBody.ActorID = "", ""
	raw, err := json.Marshal(hashBody)
	if err != nil {
		return PlanRuntimeExecutionReceipt{}, err
	}
	sum := sha256.Sum256(raw)
	command.PayloadHash = hex.EncodeToString(sum[:])
	result, err := s.store.AppendPlanExecution(command)
	if err != nil {
		return PlanRuntimeExecutionReceipt{}, err
	}
	return compactPlanRuntimeReceipt(result), nil
}

func (s *PlanRuntimeCommandService) requireCheckpoint(input PlanRuntimeExecutionInput) (pebblestore.CheckpointDefinition, error) {
	if input.CheckpointID == "" {
		return pebblestore.CheckpointDefinition{}, errors.New("checkpoint_id is required")
	}
	cp, ok, err := s.store.GetPlanCheckpointDefinition(input.SessionID, input.PlanID, input.DefinitionRevision, input.CheckpointID)
	if err != nil {
		return cp, err
	}
	if !ok {
		return cp, fmt.Errorf("checkpoint %q not found in immutable definition revision %d", input.CheckpointID, input.DefinitionRevision)
	}
	return cp, nil
}
func (s *PlanRuntimeCommandService) requireSubtask(input PlanRuntimeExecutionInput, subtaskID string) (pebblestore.SubtaskDefinition, error) {
	st, ok, err := s.store.GetPlanSubtaskDefinition(input.SessionID, input.PlanID, input.DefinitionRevision, input.CheckpointID, strings.TrimSpace(subtaskID))
	if err != nil {
		return st, err
	}
	if !ok {
		return st, fmt.Errorf("subtask %q not found in checkpoint %q definition", subtaskID, input.CheckpointID)
	}
	return st, nil
}
func (s *PlanRuntimeCommandService) requireActiveCheckpoint(input PlanRuntimeExecutionInput, summary pebblestore.PlanExecutionSummary) (pebblestore.CheckpointExecution, error) {
	if summary.Status != "in_progress" || summary.ActiveCheckpointID != input.CheckpointID {
		return pebblestore.CheckpointExecution{}, fmt.Errorf("checkpoint %q is not the active in-progress checkpoint", input.CheckpointID)
	}
	cp, ok, err := s.store.GetPlanCheckpointExecution(input.SessionID, input.PlanID, input.CheckpointID)
	if err != nil {
		return cp, err
	}
	if !ok {
		return cp, fmt.Errorf("checkpoint execution %q not found", input.CheckpointID)
	}
	return cp, nil
}
func cpDefNext(store *pebblestore.SessionStore, input PlanRuntimeExecutionInput) string {
	cp, ok, _ := store.GetPlanCheckpointDefinition(input.SessionID, input.PlanID, input.DefinitionRevision, input.CheckpointID)
	if !ok {
		return ""
	}
	return cp.NextCheckpointID
}
func firstRuntimeID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return strings.TrimSpace(ids[0])
}
func compactPlanRuntimeReceipt(result pebblestore.PlanExecutionMutationResult) PlanRuntimeExecutionReceipt {
	receipt := PlanRuntimeExecutionReceipt{PlanID: result.PlanID, ExecutionSeq: result.ExecutionSeq, HighWaterMark: result.ExecutionSeq, EventID: result.EventID, EventType: result.EventType, Replayed: result.Replayed, PlanStatus: result.SummaryChange.Status, ActiveCheckpointID: result.SummaryChange.ActiveCheckpointID, NextCheckpointID: result.SummaryChange.NextCheckpointID, RunEpochLink: result.RunEpochLink, NextAction: result.NextAction}
	if result.CheckpointChange != nil {
		receipt.CheckpointID, receipt.CheckpointStatus = result.CheckpointChange.CheckpointID, result.CheckpointChange.Status
	}
	for _, st := range result.SubtaskChanges {
		receipt.ChangedSubtasks = append(receipt.ChangedSubtasks, PlanRuntimeSubtaskReceipt{SubtaskID: st.SubtaskID, Status: st.Status})
	}
	return receipt
}
