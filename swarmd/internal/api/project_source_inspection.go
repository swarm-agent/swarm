package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// InspectProjectSources is read-only and deliberately independent of chat cwd.
// Catalog resolution precedes all filesystem inspection; membership alone grants nothing.
func (s *Server) InspectProjectSources(ctx context.Context, p identity.Principal, projectID, path, id string, generation int64, list bool) (map[string]any, error) {
	if !p.Valid() || p.Type != identity.PrincipalTypeUser {
		return nil, errors.New("project source inspection requires an authenticated user")
	}
	proj, found, err := s.sessions.Store().GetProject(p.AccountScopeID, projectID)
	if err != nil {
		return nil, err
	}
	if !found || proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if list {
		sources := []pebblestore.ProjectTaskSource{}
		unavailable := []map[string]string{}
		for _, ref := range proj.Workspaces {
			if strings.TrimSpace(ref.Path) == "" {
				unavailable = append(unavailable, map[string]string{"error": "project source binding has no canonical path"})
				continue
			}
			source, err := s.resolveProjectTaskSource(p, proj, ref.Path, ref.WorkspaceID, 0, false)
			if err != nil {
				unavailable = append(unavailable, map[string]string{"workspace_path": ref.Path, "error": err.Error()})
				continue
			}
			sources = append(sources, source)
		}
		return map[string]any{"sources": sources, "unavailable_sources": unavailable}, nil
	}
	source, err := s.resolveProjectTaskSource(p, proj, path, id, generation, false)
	if err != nil {
		return nil, err
	}
	state, err := s.workspace.InspectRepositoryForPrincipal(p, source.Path)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"source": source, "repository": state, "ready": false, "inspection_only": true}
	if state.State != workspace.RepositoryStateReady || state.Repository != source.Path {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, err := runBoundedGitCommand(ctx, source.Path, 64*1024, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("inspect source cleanliness: %w", err)
	}
	branch, err := runBoundedGitCommand(ctx, source.Path, 4096, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect source branch: %w", err)
	}
	result["clean"] = strings.TrimSpace(string(status)) == ""
	result["ready"] = result["clean"]
	result["branch"] = strings.TrimSpace(string(branch))
	result["admission_note"] = "Inspection is not a launch receipt. Deployment revalidates source readiness and allocates an isolated execution; prerequisite ancestry must still be verified."
	return result, nil
}
