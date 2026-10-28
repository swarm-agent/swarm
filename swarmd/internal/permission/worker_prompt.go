package permission

import (
	"fmt"
	"strings"
)

// WorkerNonInteractiveSource is the authorization source of a call refused
// because an unattended run would otherwise need an interactive prompt.
const WorkerNonInteractiveSource = "worker_noninteractive"

type workerPromptDeniedError struct{ tool, requirement, reason string }

func (e *workerPromptDeniedError) Error() string {
	if e.reason != "" {
		return e.reason
	}
	return fmt.Sprintf("%s denied: unattended worker jobs cannot request interactive permission (%s). This operation is outside the job's approved authority; use an authorized workspace path and stay within the write lane. Host-side tools cannot see sandbox temp paths: use bash inside the sandbox. For network or other restricted operations, report the missing authority instead of retrying for approval", e.tool, e.requirement)
}

// WorkerRunSession reports whether sessionID is a worker job's session, by its
// canonical worker run receipt. It does not depend on how the current run was
// started (first run, owner follow-up or workflow reopen).
func (s *Service) WorkerRunSession(sessionID string) (bool, error) {
	if s == nil || s.sessions == nil || s.store == nil {
		return false, nil
	}
	snapshot, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	_, found, err := s.store.WorkerRunForSession(snapshot.AccountScopeID, snapshot.ID)
	if err != nil {
		return false, err
	}
	return found, nil
}

func (s *Service) workerIdentity(sessionID string) (worker, run string) {
	if s == nil || s.sessions == nil {
		return "", ""
	}
	snapshot, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		return "", ""
	}
	receipt, found, err := s.store.WorkerRunForSession(snapshot.AccountScopeID, snapshot.ID)
	if err != nil || !found {
		return "", ""
	}
	return receipt.WorkerID, receipt.ID
}

// All permission producers cross CreatePending, including gates that deliberately
// bypass ordinary authorization (workspace expansion and sensitive reads).
// A missing gate is not evidence that a sandbox is active.
func (s *Service) rejectWorkerPrompt(input CreateInput) error {
	if s == nil {
		return nil
	}
	if s.sessions == nil {
		return nil
	}
	snapshot, found, err := s.sessions.GetSession(strings.TrimSpace(input.SessionID))
	if err != nil || !found {
		gate := s.bypassGate.Load()
		if gate == nil {
			return nil
		}
		if active, _ := (*gate)(); !active {
			return nil
		}
		return fmt.Errorf("cannot verify session identity for unattended permission safety; retry after session storage is available")
	}
	task, review, err := s.store.WorkflowReviewTaskForSession(snapshot)
	if err != nil {
		return fmt.Errorf("cannot verify workflow review for unattended permission safety: %w", err)
	}
	if review && task.Status == "in_progress" {
		reason := "unattended workflow review cannot request interactive permission; this operation requires authority outside its approved contract"
		if blocked := s.BypassBlocked(); blocked != "" {
			reason = "unattended workflow review permission temporarily unavailable: " + blocked + "; retry the tool call after sandbox readiness is restored"
		}
		return &workerPromptDeniedError{tool: input.ToolName, requirement: input.Requirement, reason: reason}
	}
	gate := s.bypassGate.Load()
	if gate == nil {
		return nil
	}
	if active, _ := (*gate)(); !active {
		return nil
	}
	_, worker, err := s.store.WorkerRunForSession(snapshot.AccountScopeID, snapshot.ID)
	if err != nil {
		return fmt.Errorf("cannot verify worker receipt for unattended permission safety: %w", err)
	}
	if !worker {
		return nil
	}
	return &workerPromptDeniedError{tool: input.ToolName, requirement: input.Requirement}
}
