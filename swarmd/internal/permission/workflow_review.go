package permission

import "strings"

// Workflow review approvals are unattended, but only for the native contract's
// ordinary tools. Policy denials and all execution/worktree guards still apply.
func (s *Service) workflowReviewTool(input AuthorizationInput) (bool, error) {
	if s.sessions == nil {
		return false, nil
	}
	session, found, err := s.sessions.GetSession(input.SessionID)
	if err != nil || !found {
		return false, err
	}
	if session.AccountScopeID != input.AccountScopeID {
		return false, nil
	}
	agent, _ := session.Metadata["agent_name"].(string)
	if agent != "system-reviewer" && agent != "system-reviewer-fixer" {
		return false, nil
	}
	task, found, err := s.store.WorkflowReviewTaskForSession(session)
	if err != nil || !found {
		return false, err
	}
	fixer := task.ReviewFixes == "security"
	if (agent == "system-reviewer-fixer") != fixer {
		return false, nil
	}
	switch strings.TrimSpace(input.ToolName) {
	case "read", "search", "find", "list", "git_status", "git_diff", "bash", "websearch", "webfetch", "webdownload":
		return true, nil
	case "write", "edit", "git_add", "git_commit":
		return fixer, nil
	}
	return false, nil
}
