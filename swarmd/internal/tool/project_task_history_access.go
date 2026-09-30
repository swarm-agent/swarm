package tool

import (
	"errors"
	"strings"
)

// TaskHistoryBinding resolves authority from retained session and task records,
// never from model arguments or injected history previews.
func (r *Runtime) TaskHistoryBinding(scope WorkspaceScope) (string, string, error) {
	if r == nil || r.sessions == nil || r.projects == nil || scope.SessionID == "" || !scope.Principal.Valid() || scope.Principal.Type != "user" || scope.Principal.UserID == "" {
		return "", "", errors.New("task history requires an authenticated linked session")
	}
	s, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil {
		return "", "", err
	}
	if !found || s.AccountScopeID != scope.Principal.AccountScopeID || s.UserID != scope.Principal.UserID || s.Mode != "auto" ||
		s.Metadata["resolved_agent_name"] != "swarm" || s.Metadata["agent_mode"] != "primary" || s.Metadata["role"] != "project_task" {
		return "", "", errors.New("task history session is not an Auto Swarm task follow-up")
	}
	projectID, _ := s.Metadata["project_id"].(string)
	taskID, _ := s.Metadata["task_id"].(string)
	attemptID, _ := s.Metadata["task_attempt_id"].(string)
	if projectID == "" || taskID == "" || attemptID == "" || attemptID == "initial" {
		return "", "", errors.New("task history follow-up link is missing")
	}
	t, found, err := r.projects.GetProjectTask(scope.Principal.AccountScopeID, projectID, taskID)
	if err != nil {
		return "", "", err
	}
	if found && t != nil {
		for _, a := range t.Attempts {
			if a.ID == attemptID && a.SessionID == s.ID && a.Role == "swarm" && a.UserID == s.UserID {
				return projectID, taskID, nil
			}
		}
	}
	return "", "", errors.New("task history session is not associated with retained attempt")
}

func (r *Runtime) authorizeTaskHistoryCall(scope WorkspaceScope, args map[string]any) error {
	projectID, taskID, err := r.TaskHistoryBinding(scope)
	if err != nil {
		return err
	}
	if strings.TrimSpace(asString(args["action"])) != "get_task" || strings.TrimSpace(asString(args["project_id"])) != projectID || strings.TrimSpace(asString(args["task_id"])) != taskID {
		return errors.New("task follow-up permits only get_task for its associated project and task")
	}
	return nil
}
