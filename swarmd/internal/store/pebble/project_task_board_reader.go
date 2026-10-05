package pebblestore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cockroachdb/pebble"
	"swarm-refactor/swarmtui/pkg/environments"
)

// ProjectTaskBoardReader is request-local, read-only and valid only inside the
// ReadProjectTaskBoard callback. All records share the compact rows' snapshot.
// Session, plan and program records are durable compact derived projections.
type ProjectTaskBoardReader struct {
	reader     pebble.Reader
	account    string
	stats      *ProjectTaskReadStats
	cache      map[string]any
	err        error
	task       *ProjectTaskRecord
	missing    []string
	missingSet map[string]bool
}

func newProjectTaskBoardReader(reader pebble.Reader, account string, rows []ProjectTaskRecord, stats *ProjectTaskReadStats) *ProjectTaskBoardReader {
	return &ProjectTaskBoardReader{reader: reader, account: account, stats: stats, cache: make(map[string]any), missingSet: make(map[string]bool)}
}

func (s *SessionStore) ReadProjectTaskBoard(account, project string, archived bool, consume func([]ProjectTaskRecord, *ProjectTaskBoardReader)) ([]ProjectTaskRecord, ProjectTaskReadStats, error) {
	rows, _, stats, err := s.readProjectTaskSummaries(account, project, archived, consume)
	return rows, stats, err
}

func (r *ProjectTaskBoardReader) BindTask(task *ProjectTaskRecord) {
	if task != nil {
		task.EnvironmentAttachments = taskEnvironmentProjection(*task)
		for i := range task.EnvironmentAttachments {
			a := &task.EnvironmentAttachments[i]
			if a.State == "stale" {
				continue
			}
			if a.Source.DeploymentID != "" {
				dep, found, err := boardRead[environments.Deployment](r, KeyDeploymentForAccount(r.account, a.Source.WorkspaceID, a.Source.DeploymentID))
				if err != nil || !found || dep.AccountScopeID != r.account || dep.CreatedAt != a.Source.CreatedAt || dep.Runtime.ContainerID != a.Source.ContainerID {
					a.State = "stale"
					continue
				}
				switch dep.Status {
				case environments.DeploymentStatusFailed:
					a.State = "failed"
				case environments.DeploymentStatusStopped, environments.DeploymentStatusTerminated:
					a.State = "stopped"
				default:
					if dep.ReviewExpired(time.Now().UnixMilli()) || !a.Source.Matches(dep) {
						a.State = "stale"
					} else {
						a.State = "ready"
					}
				}
			} else if a.OperationID != "" {
				op, found, err := boardRead[environments.EnvironmentOperation](r, KeyEnvironmentOperationForAccount(r.account, a.Source.WorkspaceID, a.OperationID))
				if err != nil || !found || op.AccountScopeID != r.account || op.EnvironmentID != a.EnvironmentID {
					a.State = "stale"
					continue
				}
				if op.IsActive() {
					if op.Action == environments.OperationActionBuild {
						a.State = "building"
					} else {
						a.State = "preparing"
					}
				} else if op.Status == environments.OperationStatusSucceeded {
					a.State = "stale"
				} else {
					a.State = "failed"
				}
			}
		}
	}
	r.task = task
}

func boardRead[T any](r *ProjectTaskBoardReader, key string) (T, bool, error) {
	var zero T
	if r.err != nil {
		return zero, false, r.err
	}
	if cached, ok := r.cache[key]; ok {
		if cached == nil {
			return zero, false, nil
		}
		return cached.(T), true, nil
	}
	started := time.Now()
	defer func() { r.stats.RelatedElapsed += time.Since(started) }()
	r.stats.RelatedRecordReads++
	raw, closer, err := r.reader.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		r.cache[key] = nil
		return zero, false, nil
	}
	if err != nil {
		r.err = err
		return zero, false, err
	}
	defer closer.Close()
	r.stats.RelatedDecodedBytes += int64(len(raw))
	if r.stats.RelatedDecodedBytes > 32<<20 || r.stats.RelatedRecordReads > 100000 {
		r.err = errors.New("project task related records exceed read budget")
		return zero, false, r.err
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		r.err = err
		return zero, false, err
	}
	r.cache[key] = value
	return value, true, nil
}

func (r *ProjectTaskBoardReader) GetSession(id string) (SessionSnapshot, bool, error) {
	v, ok, err := boardRelatedRead[SessionSnapshot](r, KeySession(id))
	if err != nil || !ok || v.ID != id || v.AccountScopeID != r.account {
		return SessionSnapshot{}, false, err
	}
	v = compactBoardSession(v)
	lifecycle, found, err := boardRead[SessionLifecycleSnapshot](r, KeySessionLifecycle(id))
	if found && lifecycle.SessionID == id && lifecycle.AccountScopeID == r.account {
		if r.task == nil || r.task.SessionID != id || r.task.ExecutionRunID() == "" || lifecycle.RunID == r.task.ExecutionRunID() {
			v.Lifecycle = &lifecycle
		}
	}
	return v, err == nil, err
}

func (r *ProjectTaskBoardReader) GetV3SessionRunState(id string) (V3SessionRunState, bool, error) {
	v, ok, err := boardRead[V3SessionRunState](r, KeyV3SessionRunIntentActive(id))
	if err != nil || !ok || v.SessionID != id || v.AccountScopeID != r.account {
		return V3SessionRunState{}, false, err
	}
	// An old terminal run cannot conclude a new attempt; live owning-session
	// execution still outranks workflow labels as in the canonical policy.
	if r.task != nil && id == r.task.SessionID && r.task.ExecutionRunID() != "" && v.RunID != r.task.ExecutionRunID() && v.Status != V3RunIntentRunning {
		return V3SessionRunState{}, false, nil
	}
	return v, true, nil
}

func (r *ProjectTaskBoardReader) GetTaskProgram(session, id string) (TaskProgramRecord, bool, error) {
	if _, ok, err := r.GetSession(session); err != nil || !ok {
		return TaskProgramRecord{}, false, err
	}
	v, ok, err := boardRelatedRead[TaskProgramRecord](r, KeyTaskProgram(session, id))
	if err != nil || !ok || v.ParentSessionID != session || v.ProgramID != id {
		return TaskProgramRecord{}, false, err
	}
	return compactBoardProgram(v), true, nil
}

func (r *ProjectTaskBoardReader) GetPlan(session, id string) (SessionPlanSnapshot, bool, error) {
	if r.task == nil || r.task.PlanBinding == nil {
		return SessionPlanSnapshot{}, false, nil
	}
	b := r.task.PlanBinding
	if b.PlanID != id || session != r.task.SessionID || (b.SessionID != "" && b.SessionID != session) {
		return SessionPlanSnapshot{}, false, nil
	}
	v, ok, err := boardRelatedRead[SessionPlanSnapshot](r, KeySessionPlan(session, id))
	if err != nil || !ok || v.SessionID != session || v.ID != id || v.AccountScopeID != r.account {
		return SessionPlanSnapshot{}, false, err
	}
	if b.Receipt != "" && v.ApprovalState == "approved" && b.Receipt != v.AcceptedDefinitionReceipt {
		return SessionPlanSnapshot{}, false, nil
	}
	if v.ApprovalState != "approved" && b.DefinitionRevision > 0 && v.Version != b.DefinitionRevision {
		return SessionPlanSnapshot{}, false, nil
	}
	return compactBoardPlan(v), true, nil
}

func (r *ProjectTaskBoardReader) ListPlans(session string, limit int) ([]SessionPlanSnapshot, error) {
	// Board reconciliation is exact-binding only, never latest unrelated plan.
	if r.task == nil || r.task.PlanBinding == nil {
		return nil, nil
	}
	v, ok, err := r.GetPlan(session, r.task.PlanBinding.PlanID)
	if !ok || err != nil {
		return nil, err
	}
	return []SessionPlanSnapshot{v}, nil
}

func (r *ProjectTaskBoardReader) ListRunIntents(session string, limit int) ([]V3SessionRunIntent, error) {
	// Only the exact attempt is a valid fallback; do not scan run history.
	if r.task == nil || r.task.SessionID != session || r.task.ExecutionRunID() == "" {
		return nil, nil
	}
	v, ok, err := boardRead[V3SessionRunIntent](r, KeyV3SessionRunIntent(session, r.task.ExecutionRunID()))
	if err != nil || !ok || v.AccountScopeID != r.account || v.SessionID != session || v.RunID != r.task.ExecutionRunID() {
		return nil, err
	}
	return []V3SessionRunIntent{v}, nil
}

func (r *ProjectTaskBoardReader) CurrentTaskProgram(t *ProjectTaskRecord) (TaskProgramRecord, bool) {
	return CurrentProjectTaskProgram(r, t)
}
func (r *ProjectTaskBoardReader) ProjectTaskExecuting(t *ProjectTaskRecord) bool {
	return ProjectTaskExecutingFrom(r, t)
}
func (r *ProjectTaskBoardReader) ProjectTaskPlanUnfinished(t *ProjectTaskRecord) bool {
	return ProjectTaskPlanUnfinishedFrom(r, t)
}

// Hydrate only exact active-attempt evidence from the compact snapshot. Full
// assistant messages and terminal event payloads remain detail-only reads.
func (r *ProjectTaskBoardReader) hydrateAttempt(task *ProjectTaskRecord) {
	a := task.ActiveAttempt()
	if a == nil || a.SessionID != task.SessionID {
		return
	}
	if a.SummaryRunID != a.RunID {
		a.Summary, a.SummaryRunID = "", ""
	}
	r.BindTask(task)
	state, ok, err := r.GetV3SessionRunState(a.SessionID)
	if err != nil || !ok || state.AccountScopeID != task.AccountID {
		return
	}
	if a.RunID != "" && a.RunID != state.RunID {
		a.Summary, a.SummaryRunID = "", ""
		return
	}
	a.RunID = state.RunID
	if state.Active {
		a.Summary, a.SummaryRunID = "", ""
		return
	}
	session, ok, err := r.GetSession(a.SessionID)
	if err != nil || !ok {
		return
	}
	if task.PlanBinding != nil {
		if plan, ok, _ := r.GetPlan(a.SessionID, task.PlanBinding.PlanID); ok && plan.Document != nil {
			for _, cp := range plan.Document.Checkpoints {
				if cp.Status == "completed" && cp.SessionID == a.SessionID && cp.RunID == state.RunID && cp.Handoff != nil {
					a.Summary, a.SummaryRunID = cp.Handoff.Overview, state.RunID
				}
			}
		}
	}
	if summary, ok := r.completedRunSummary(state); ok {
		a.Summary, a.SummaryRunID = summary, state.RunID
		return
	}
	if session.Metadata["lifecycle_summary_run_id"] == state.RunID && session.Metadata["lifecycle_signal"] == "needs_review" {
		if summary, ok := session.Metadata["lifecycle_summary"].(string); ok {
			a.Summary, a.SummaryRunID = summary, state.RunID
		}
	}
}
