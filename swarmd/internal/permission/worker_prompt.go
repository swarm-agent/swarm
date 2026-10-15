package permission

import (
	"fmt"
	"strings"
)

type workerPromptDeniedError struct{ tool, requirement string }

func (e *workerPromptDeniedError) Error() string {
	return fmt.Sprintf("%s denied: unattended worker jobs cannot request interactive permission (%s). This operation is outside the job's approved authority; use an authorized workspace path and stay within the write lane. Host-side tools cannot see sandbox temp paths: use bash inside the sandbox. For network or other restricted operations, report the missing authority instead of retrying for approval", e.tool, e.requirement)
}

func (s *Service) workerIdentity(sessionID string) (worker, run string) {
	if s == nil || s.sessions == nil {
		return "", ""
	}
	snapshot, ok, err := s.sessions.GetSession(strings.TrimSpace(sessionID))
	if err != nil || !ok {
		return "", ""
	}
	worker, _ = snapshot.Metadata["worker_id"].(string)
	run, _ = snapshot.Metadata["worker_execution_run_id"].(string)
	return strings.TrimSpace(worker), strings.TrimSpace(run)
}

// All permission producers cross CreatePending, including gates that deliberately
// bypass ordinary authorization (workspace expansion and sensitive reads).
// A missing gate is not evidence that a sandbox is active.
func (s *Service) rejectWorkerPrompt(input CreateInput) error {
	if s == nil {
		return nil
	}
	gate := s.bypassGate.Load()
	if gate == nil {
		return nil
	}
	if active, _ := (*gate)(); !active {
		return nil
	}
	if s.sessions == nil {
		return nil
	}
	snapshot, found, err := s.sessions.GetSession(strings.TrimSpace(input.SessionID))
	if err != nil || !found {
		return fmt.Errorf("cannot verify session identity for unattended permission safety; retry after session storage is available")
	}
	worker, _ := snapshot.Metadata["worker_id"].(string)
	run, _ := snapshot.Metadata["worker_execution_run_id"].(string)
	if strings.TrimSpace(worker) == "" || strings.TrimSpace(run) == "" {
		return nil
	}
	return &workerPromptDeniedError{tool: input.ToolName, requirement: input.Requirement}
}
