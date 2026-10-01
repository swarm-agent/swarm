package pebblestore

// CurrentTaskProgram rejects program evidence from another session or an older
// owning run. Program workflow state is not evidence that execution has stopped.
func (s *SessionStore) CurrentTaskProgram(t *ProjectTaskRecord) (TaskProgramRecord, bool) {
	if t == nil || t.SessionID == "" {
		return TaskProgramRecord{}, false
	}
	sess, found, err := s.GetSession(t.SessionID)
	if err != nil || !found || sess.AccountScopeID != t.AccountID {
		return TaskProgramRecord{}, false
	}
	id := t.TaskProgramID
	if id == "" && t.TaskProgram != nil {
		id = t.TaskProgram.ID
	}
	if id == "" {
		return TaskProgramRecord{}, false
	}
	prog, found, err := s.GetTaskProgram(t.SessionID, id)
	if err != nil || !found || prog.ParentSessionID != t.SessionID {
		return TaskProgramRecord{}, false
	}
	state, known, err := s.GetV3SessionRunState(t.SessionID)
	if err != nil || (known && state.AccountScopeID != t.AccountID) || (prog.ReservationRunID != "" && (!known || state.RunID != prog.ReservationRunID)) {
		return TaskProgramRecord{}, false
	}
	return prog, true
}

// ProjectTaskExecuting uses only canonical, account-bound current run evidence.
// Queue ownership, old job generations, and historical attempts are not execution.
func (s *SessionStore) ProjectTaskExecuting(t *ProjectTaskRecord) bool {
	if t == nil || t.Archived || t.Status == "rejected" || t.Status == "pending_approval" || t.Status == "planning" || (t.Status == "queued" && t.WorkerID == "") {
		return false
	}
	if a := t.ActiveAttempt(); a != nil && a.SessionID != t.SessionID {
		return false
	}
	running := func(sid, runID string) bool {
		if sid == "" {
			return false
		}
		sess, found, err := s.GetSession(sid)
		if err != nil || !found || sess.AccountScopeID != t.AccountID {
			return false
		}
		state, found, err := s.GetV3SessionRunState(sid)
		return err == nil && found && state.AccountScopeID == t.AccountID && state.Status == V3RunIntentRunning && (runID == "" || state.RunID == runID)
	}
	if running(t.SessionID, "") {
		return true
	}
	if prog, ok := s.CurrentTaskProgram(t); ok {
		for _, job := range prog.Jobs {
			sid := job.CurrentSessionID
			if sid == "" {
				sid = job.ChildSessionID
			}
			if running(sid, job.CurrentRunID) {
				return true
			}
		}
	}
	return false
}

// ProjectTaskPlanUnfinished prevents a nested program from completing its owning
// approved checkpoint plan. Missing plan hydration is not completion evidence.
func (s *SessionStore) ProjectTaskPlanUnfinished(t *ProjectTaskRecord) bool {
	if t.PlanBinding == nil || t.PlanBinding.PlanID == "" {
		return false
	}
	plan, found, err := s.GetPlan(t.SessionID, t.PlanBinding.PlanID)
	if err != nil || !found || plan.Document == nil || len(plan.Document.Checkpoints) == 0 {
		return true
	}
	for _, cp := range plan.Document.Checkpoints {
		if cp.Status != "completed" {
			return true
		}
	}
	return false
}
