package api

import (
	"fmt"
	"strings"
	"swarm/packages/swarmd/internal/jobautomation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Resolve only within the workflow account, refusing ambiguous or inactive names.
func (s *Server) workflowWorker(account, name string) (string, error) {
	if jobautomation.IsBuiltinAgent(name) {
		return name, nil
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		return "", jobautomation.InvalidTrigger("worker authority unavailable")
	}
	ws := s.sessions.Store().WorkerStore()
	w, found, err := ws.GetWorker(account, name)
	if err != nil {
		return "", err
	}
	if found {
		return w.ID, nil
	}
	cursor, id, count := "", "", 0
	for page := 0; page < 50; page++ {
		list, err := ws.ListWorkers(account, store.ListWorkersQuery{Limit: 200, After: cursor})
		if err != nil {
			return "", err
		}
		for _, candidate := range list.Workers {
			if candidate.Name == name && candidate.LifecycleState == store.WorkerLifecycleStateActive {
				id = candidate.ID
				count++
			}
		}
		if list.NextCursor == "" {
			if count != 1 {
				return "", jobautomation.InvalidTrigger(fmt.Sprintf("worker name %q has %d active matches; exactly one is required", name, count))
			}
			return id, nil
		}
		if list.NextCursor == cursor {
			break
		}
		cursor = list.NextCursor
	}
	return "", jobautomation.InvalidTrigger("too many workers to resolve name safely")
}

func (s *Server) resolveWorkflowWorkers(a *jobautomation.Automation) error {
	if a.Scope.Worker != "" {
		id, err := s.workflowWorker(a.Account, a.Scope.Worker)
		if err != nil {
			return err
		}
		a.Scope.Worker = id
	}
	for i := range a.Steps {
		if a.Steps[i].Type != jobautomation.StepAgent {
			continue
		}
		id, err := s.workflowWorker(a.Account, a.Steps[i].Worker)
		if err != nil {
			return err
		}
		if a.Steps[i].ForEach != nil {
			if jobautomation.IsBuiltinAgent(id) {
				return jobautomation.InvalidTrigger("for_each requires a deployed worker")
			}
			worker, found, err := s.sessions.Store().WorkerStore().GetWorker(a.Account, id)
			if err != nil {
				return err
			}
			if !found || worker.LifecycleState != store.WorkerLifecycleStateActive {
				return jobautomation.InvalidTrigger("for_each requires an active worker")
			}
			if err := validateTriggerWorkerRole(worker.Role); err != nil {
				return err
			}
		}
		a.Steps[i].Worker = id
	}
	if a.On != nil {
		id, err := s.workflowStartWorker(a.Account, a.On.Start.Worker)
		if err != nil {
			return err
		}
		a.On.Start.Worker = id
	}
	return nil
}

// Existing saved workflows may use the legacy preset or case-insensitive name
// spelling. Preserve those aliases at execution, but never use them to bypass
// the active/unique check for an exact portable name.
func (s *Server) workflowRuntimeWorker(c *controlMCPCall, account, ref string) (string, error) {
	workers, err := c.listWorkers()
	if err != nil {
		return "", err
	}
	for _, worker := range workers {
		if worker["id"] == ref || worker["name"] == ref {
			return s.workflowWorker(account, ref)
		}
	}
	id, count := "", 0
	for _, worker := range workers {
		name, _ := worker["name"].(string)
		if strings.EqualFold(name, ref) || controlMCPWorkerPreset(worker) == strings.ToLower(ref) {
			id, _ = worker["id"].(string)
			count++
		}
	}
	if count == 1 && id != "" {
		return s.workflowWorker(account, id)
	}
	if count > 1 {
		return "", jobautomation.InvalidTrigger(fmt.Sprintf("legacy worker reference %q is ambiguous", ref))
	}
	return s.workflowWorker(account, ref)
}
