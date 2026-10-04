package pebblestore

import (
	"encoding/json"
	"errors"

	"github.com/cockroachdb/pebble"
)

// ProjectTaskPlanSummary deliberately decodes only workflow/review identity, not
// plan prose, definitions, jobs, artifacts or checkpoint task bodies.
type ProjectTaskPlanSummary struct {
	ID                        string                          `json:"id"`
	SessionID                 string                          `json:"session_id"`
	AccountScopeID            string                          `json:"account_scope_id"`
	Version                   int                             `json:"version"`
	Status                    string                          `json:"status"`
	ApprovalState             string                          `json:"approval_state"`
	AcceptedDefinitionReceipt string                          `json:"accepted_definition_receipt"`
	Document                  *ProjectTaskPlanDocumentSummary `json:"document"`
}

type ProjectTaskPlanDocumentSummary struct {
	ActiveCheckpointID string                           `json:"active_checkpoint_id"`
	ExecutionState     *ProjectTaskPlanExecutionSummary `json:"execution_state,omitempty"`
	Checkpoints        []ProjectTaskCheckpointSummary   `json:"checkpoints"`
}
type ProjectTaskPlanExecutionSummary struct {
	Status string `json:"status"`
}
type ProjectTaskCheckpointSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func SummarizeProjectTaskPlan(plan SessionPlanSnapshot) ProjectTaskPlanSummary {
	out := ProjectTaskPlanSummary{ID: plan.ID, SessionID: plan.SessionID, AccountScopeID: plan.AccountScopeID, Version: plan.Version, Status: plan.Status, ApprovalState: plan.ApprovalState, AcceptedDefinitionReceipt: plan.AcceptedDefinitionReceipt}
	if plan.Document != nil {
		out.Document = &ProjectTaskPlanDocumentSummary{ActiveCheckpointID: plan.Document.ActiveCheckpointID}
		if plan.Document.ExecutionState != nil {
			out.Document.ExecutionState = &ProjectTaskPlanExecutionSummary{Status: plan.Document.ExecutionState.Status}
		}
		for _, cp := range plan.Document.Checkpoints {
			out.Document.Checkpoints = append(out.Document.Checkpoints, ProjectTaskCheckpointSummary{ID: cp.ID, Status: cp.Status})
		}
	}
	return out
}

// Related summaries are keyed by task ID. Missing records remain nil; a stale
// binding never attaches another active plan. The API owns lifecycle policy.
type ProjectTaskRelatedSummary struct {
	RunState         *V3SessionRunState
	Plan             *ProjectTaskPlanSummary
	PlanBindingStale bool
}

func readProjectTaskRelated(reader pebble.Reader, account string, rows []ProjectTaskRecord, stats *ProjectTaskReadStats) (map[string]ProjectTaskRelatedSummary, error) {
	out := make(map[string]ProjectTaskRelatedSummary, len(rows))
	runs := make(map[string]*V3SessionRunState)
	plans := make(map[string]*ProjectTaskPlanSummary)
	read := func(key string, target any) (bool, error) {
		stats.RelatedRecordReads++
		raw, closer, err := reader.Get([]byte(key))
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		defer closer.Close()
		stats.RelatedDecodedBytes += int64(len(raw))
		if stats.RelatedDecodedBytes > 32<<20 {
			return false, errors.New("project task related records exceed read budget")
		}
		return true, json.Unmarshal(raw, target)
	}
	for _, task := range rows {
		var related ProjectTaskRelatedSummary
		if task.SessionID != "" {
			key := KeyV3SessionRunIntentActive(task.SessionID)
			run, cached := runs[key]
			if !cached {
				var state V3SessionRunState
				ok, err := read(key, &state)
				if err != nil {
					return nil, err
				}
				if ok && state.AccountScopeID == account && state.SessionID == task.SessionID {
					run = &state
				}
				runs[key] = run
			}
			related.RunState = run
		}
		if binding := task.PlanBinding; binding != nil && binding.PlanID != "" {
			session := binding.SessionID
			if session == "" {
				session = task.SessionID
			}
			related.PlanBindingStale = true
			if session != "" && (task.SessionID == "" || session == task.SessionID) {
				key := taskRelatedKey(KeySessionPlan(session, binding.PlanID))
				plan, cached := plans[key]
				if !cached {
					var summary ProjectTaskPlanSummary
					ok, err := read(key, &summary)
					if err != nil {
						return nil, err
					}
					if ok && summary.AccountScopeID == account && summary.SessionID == session && summary.ID == binding.PlanID {
						plan = &summary
					}
					plans[key] = plan
				}
				if plan != nil {
					valid := true
					if plan.ApprovalState == "approved" {
						valid = binding.Receipt == "" || binding.Receipt == plan.AcceptedDefinitionReceipt
					} else if binding.DefinitionRevision > 0 {
						valid = plan.Version == binding.DefinitionRevision
					}
					if valid {
						related.Plan, related.PlanBindingStale = plan, false
					}
				}
			}
		}
		out[task.ID] = related
	}
	return out, nil
}
