package session

import (
	"os/exec"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type worktreeBranchAttacher interface {
	AttachBranch(workspacePath, sessionID, title string) (string, error)
}

// DetectCurrentBranch returns the named branch checked out in workspacePath.
// Git errors and detached HEAD are intentionally represented as no branch.
func DetectCurrentBranch(workspacePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return ""
	}
	output, err := exec.Command("git", "-C", workspacePath, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
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
