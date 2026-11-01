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
//
// The daemon's object store permission repair (jobautomation.
// RepairObjectStore: at startup, every ObjectStoreRepairInterval and before each
// environment step) shows there too, as repository_health.permissions
// when its last pass made entries readable or left some it could not
// repair (a later pass that finds every entry readable clears it); the
// latter is also a box summary reason.
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
	repair, repaired, err := store.ObjectStoreRepairOf(root)
	if err != nil {
		log.Printf("swarmd read folder object store repair: %v", err)
		repaired = false
	}
	if !found && !repaired {
		return
	}
	health := map[string]any{}
	if found {
		health = repositoryHealthView(h)
	}
	if repaired {
		health["permissions"] = objectStoreRepairView(repair)
		if !found && repair.Problem() {
			health["message"] = repair.Message()
		}
	}
	view["repository_health"] = health
}

func repositoryHealthView(h jobautomation.RepositoryHealth) map[string]any {
	view := map[string]any{"message": h.Message(), "problem": h.Problem, "at": h.At}
	if h.Object != "" {
		view["object"] = h.Object
	}
	if h.Detail != "" {
		view["detail"] = h.Detail
	}
	if h.Command != "" {
		view["git_command"] = h.Command
	}
	return view
}

func objectStoreRepairView(r jobautomation.ObjectStoreRepair) map[string]any {
	view := map[string]any{"checked": r.Checked, "repaired": r.Repaired, "unrepaired": r.Unrepaired, "at": r.At}
	if r.SetgidRefused > 0 {
		view["setgid_refused"] = r.SetgidRefused
	}
	if r.Sample != "" {
		view["sample"] = r.Sample
	}
	if r.Error != "" {
		view["error"] = r.Error
	}
	return view
}

// visibleObjectStoreRepairProblems is every folder whose object store
// repair left entries run steps cannot read, for the folders the principal
// can see.
func (s *Server) visibleObjectStoreRepairProblems(p identity.Principal) []jobautomation.ObjectStoreRepair {
	store, err := s.jobAutomationStore()
	if err != nil {
		return nil
	}
	problems, err := store.ObjectStoreRepairProblems()
	if err != nil {
		log.Printf("swarmd read folder object store repair: %v", err)
		return nil
	}
	if len(problems) == 0 {
		return nil
	}
	visible := s.visibleFolders(p)
	var out []jobautomation.ObjectStoreRepair
	for _, r := range problems {
		if visible[filepath.Clean(r.Folder)] {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) visibleFolders(p identity.Principal) map[string]bool {
	if s.workspace == nil {
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
	return visible
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
