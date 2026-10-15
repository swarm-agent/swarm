package run

import (
	"errors"

	worktree "swarm/packages/swarmd/internal/worktree"
)

type failSecondLaneIntegration struct {
	*worktree.Service
	failPath string
	attempts map[string]int
}

func (w *failSecondLaneIntegration) ApplyTaskIntegration(path string, plan worktree.TaskIntegrationPlan) (worktree.TaskIntegrationResult, error) {
	w.attempts[path]++
	if path == w.failPath && w.attempts[path] == 1 {
		return worktree.TaskIntegrationResult{}, errors.New("injected second-lane integration failure")
	}
	return w.Service.ApplyTaskIntegration(path, plan)
}
