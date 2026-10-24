package permission

import "strings"

// Workflow review approvals are unattended, but only for the native contract's
// ordinary tools. Policy denials and all execution/worktree guards still apply.
func (s *Service) workflowReviewTool(input AuthorizationInput) (bool, error) {
	if s.sessions == nil || s.BypassBlocked() != "" {
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
	if task.Status != "in_progress" {
		return false, nil
	}
	fixer := task.ReviewFixes == "security"
	if (agent == "system-reviewer-fixer") != fixer {
		return false, nil
	}
	switch strings.TrimSpace(input.ToolName) {
	case "read", "search", "find", "list", "git_status", "git_diff", "websearch", "webfetch", "webdownload":
		return true, nil
	case "bash":
		// Match explainBuiltinAllow's effect guards, including backend
		// promotions: a model's read label alone cannot grant authority.
		// The effect assessor does not classify every Git subcommand (notably
		// push). Require the existing Git read-only classification as well.
		if cmd, simple := parseBashSimpleCommand(input.ToolArguments); simple && cmd.Words[0] == "git" && !bashReadOnlyGitArgs(cmd.Words[1:]) {
			return false, nil
		}
		effect := buildPolicyEvalContext(input.ToolName, input.ToolArguments).BashEffect
		return effect.Valid && effect.DeclaredCategory == BashEffectRead && effect.Category == BashEffectRead && !effect.Critical && !effect.Promoted, nil
	case "write", "edit", "git_add", "git_commit":
		return fixer, nil
	}
	return false, nil
}
