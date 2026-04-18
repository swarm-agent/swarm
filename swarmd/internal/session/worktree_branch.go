package session

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type worktreeBranchAttacher interface {
	AttachBranch(workspacePath, sessionID, title string) (string, error)
}

func AttachCreatedWorktreeBranch(svc *Service, attacher worktreeBranchAttacher, session pebblestore.SessionSnapshot) (pebblestore.SessionSnapshot, *pebblestore.EventEnvelope, error) {
	if svc == nil || attacher == nil {
		return session, nil, nil
	}
	if !session.WorktreeEnabled || strings.TrimSpace(session.WorkspacePath) == "" {
		return session, nil, nil
	}
	if strings.TrimSpace(session.WorktreeBranch) != "" {
		return session, nil, nil
	}

	branch, err := attacher.AttachBranch(session.WorkspacePath, session.ID, session.Title)
	if err != nil {
		return session, nil, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return session, nil, nil
	}
	return svc.SetWorktreeBranch(session.ID, branch)
}
