package api

import (
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/jobautomation"
)

// workflowCandidate is one workflow a reference may name: its id, name and
// state (active, disabled or proposed), from this machine's store or a
// linked machine's list.
type workflowCandidate struct{ ID, Name, State string }

func workflowCandidates(list []jobautomation.Automation) []workflowCandidate {
	out := make([]workflowCandidate, 0, len(list))
	for _, a := range list {
		out = append(out, workflowCandidate{ID: a.ID, Name: a.Name, State: jobAutomationView(a)["state"].(string)})
	}
	return out
}

// workflowReference is the reference a call names a workflow by: its id or
// its name, in id or name (id first).
func workflowReference(args map[string]any) string {
	if ref := controlMCPString(args, "id"); ref != "" {
		return ref
	}
	return controlMCPString(args, "name")
}

// resolveWorkflowReference is the one rule that turns a workflow's id or
// name into its id, everywhere a workflow is named (get, enable, disable,
// delete, export, history, restore, propose), here and on a linked machine.
// An exact id wins; then the exact name, then the name in any case. Among
// the workflows of that name the accepted one wins (a proposal is a pending
// change of it); several matches of one kind are ambiguous and refused,
// naming each match.
func resolveWorkflowReference(candidates []workflowCandidate, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", controlMCPToolFailure("name the workflow by its id or name (manage_workflows list shows them)")
	}
	for _, c := range candidates {
		if c.ID == ref {
			return c.ID, nil
		}
	}
	for _, same := range []func(string) bool{
		func(name string) bool { return name == ref },
		func(name string) bool { return strings.EqualFold(name, ref) },
	} {
		var accepted, proposed []workflowCandidate
		for _, c := range candidates {
			if !same(c.Name) {
				continue
			}
			if c.State == "proposed" {
				proposed = append(proposed, c)
			} else {
				accepted = append(accepted, c)
			}
		}
		for _, matches := range [][]workflowCandidate{accepted, proposed} {
			switch len(matches) {
			case 0:
				continue
			case 1:
				return matches[0].ID, nil
			}
			named := make([]string, 0, len(matches))
			for _, m := range matches {
				named = append(named, fmt.Sprintf("%s (%s, %s)", m.Name, m.ID, m.State))
			}
			return "", controlMCPToolFailure("%q names %d workflows: %s; pass the id of the one you mean", ref, len(matches), strings.Join(named, ", "))
		}
	}
	return "", workflowNotFound(ref)
}

// workflowNotFound refuses a reference no workflow has (404 on a route).
func workflowNotFound(ref string) error {
	return workflowNotFoundError{ref: ref}
}

type workflowNotFoundError struct{ ref string }

func (e workflowNotFoundError) Error() string {
	return fmt.Sprintf("no workflow has the id or name %q (manage_workflows list shows them; history also finds a deleted one)", e.ref)
}

func (e workflowNotFoundError) Unwrap() error { return jobautomation.ErrNotFound }

// resolveWorkflow reads the workflow a call names by id or name on this
// machine.
func (s *Server) resolveWorkflow(caller WorkflowCaller, ref string) (jobautomation.Automation, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return jobautomation.Automation{}, err
	}
	if strings.TrimSpace(ref) != "" {
		if a, err := store.Get(account, strings.TrimSpace(ref)); err == nil {
			return a, nil
		} else if !errors.Is(err, jobautomation.ErrNotFound) {
			return jobautomation.Automation{}, err
		}
	}
	list, err := store.List(account)
	if err != nil {
		return jobautomation.Automation{}, err
	}
	id, err := resolveWorkflowReference(workflowCandidates(list), ref)
	if err != nil {
		return jobautomation.Automation{}, err
	}
	return store.Get(account, id)
}

// workflowHistoryOf reads the retained history a call names by id or name,
// including a deleted workflow's: by id, by the name history keeps, then by
// a live workflow's name in any case. It returns the records and the id.
func (s *Server) workflowHistoryOf(caller WorkflowCaller, args map[string]any) ([]jobautomation.HistoryRecord, string, error) {
	store, account, err := s.workflowAccount(caller)
	if err != nil {
		return nil, "", err
	}
	ref := workflowReference(args)
	if ref == "" {
		return nil, "", controlMCPToolFailure("name the workflow by its id or name (manage_workflows list shows them)")
	}
	records, err := store.History(account, ref, "")
	if err == nil {
		return records, ref, nil
	}
	if !errors.Is(err, jobautomation.ErrNotFound) {
		return nil, "", err
	}
	if records, err = store.History(account, "", ref); err == nil {
		id := ref
		for _, r := range records {
			if r.Definition != nil {
				id = r.Definition.ID
				break
			}
		}
		return records, id, nil
	}
	if !errors.Is(err, jobautomation.ErrNotFound) {
		return nil, "", err
	}
	a, err := s.resolveWorkflow(caller, ref)
	if errors.Is(err, jobautomation.ErrNotFound) {
		return nil, "", workflowHistoryNotFound("workflow history was not found for %q (an id or a name)", ref)
	}
	if err != nil {
		return nil, "", err
	}
	records, err = store.History(account, a.ID, "")
	if errors.Is(err, jobautomation.ErrNotFound) {
		return nil, "", workflowHistoryNotFound("workflow history was not found for %q (an id or a name)", ref)
	}
	return records, a.ID, err
}
