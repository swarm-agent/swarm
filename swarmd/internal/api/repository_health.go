package api

import (
	"log"
	"path/filepath"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
)

// A folder's repository health, as workflow run steps found it: the last
// object of the folder's repository a step's checkout could not read
// (corrupt bytes, or a file the step's lease user may not read), kept until
// a later checkout of that folder reads every object from it
// (jobautomation.Store.RecordRepositoryCheck). get_environment shows it as
// repository_health; the box summary (swarm_status, and a linked machine's
// machine_status) as one of its reasons.

// putRepositoryHealth adds root's repository_health to an environment view
// when a run step's checkout recorded a problem for it.
func (s *Server) putRepositoryHealth(view map[string]any, root string) {
	store, err := s.jobAutomationStore()
	if err != nil {
		return
	}
	h, found, err := store.RepositoryHealthOf(root)
	if err != nil {
		log.Printf("swarmd read folder repository health: %v", err)
		return
	}
	if !found {
		return
	}
	view["repository_health"] = repositoryHealthView(h)
}

func repositoryHealthView(h jobautomation.RepositoryHealth) map[string]any {
	view := map[string]any{"message": h.Message(), "object": h.Object, "problem": h.Problem, "at": h.At}
	if h.Detail != "" {
		view["detail"] = h.Detail
	}
	return view
}

// visibleRepositoryHealth is every recorded folder repository problem whose
// folder the principal can see (its account's known folders).
func (s *Server) visibleRepositoryHealth(p identity.Principal) []jobautomation.RepositoryHealth {
	store, err := s.jobAutomationStore()
	if err != nil {
		return nil
	}
	problems, err := store.RepositoryHealthProblems()
	if err != nil {
		log.Printf("swarmd read folder repository health: %v", err)
		return nil
	}
	if len(problems) == 0 || s.workspace == nil {
		return nil
	}
	entries, err := s.workspace.ListKnownForPrincipal(p, 100000)
	if err != nil {
		return nil
	}
	visible := make(map[string]bool, len(entries))
	for _, entry := range entries {
		visible[filepath.Clean(entry.Path)] = true
	}
	var out []jobautomation.RepositoryHealth
	for _, h := range problems {
		if visible[filepath.Clean(h.Folder)] {
			out = append(out, h)
		}
	}
	return out
}
