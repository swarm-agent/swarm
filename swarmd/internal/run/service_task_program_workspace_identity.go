package run

import (
    "errors"
    "fmt"
    "path/filepath"

    pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// canonicalRepositorySource validates a session grant against the current
// account-scoped workspace identity. No path-only grant can mint a lane.
func (p *taskProgramScheduler) canonicalRepositorySource(source string) (string, int64, error) {
    if p.service == nil || p.service.sessionWorkspaceCanonicalize == nil {
        return "", 0, errors.New("Task Program canonical workspace authority unavailable")
    }
    principal, err := principalForRunWorkspaceScope(p.parentSession, p.req.Principal)
    if err != nil { return "", 0, err }
    var id string
    var generation int64
    for _, grant := range p.parentSession.WorkspaceGrants {
        if grant.WorkspaceID == "" || grant.WorkspaceGeneration <= 0 { continue }
        if !sameTaskProgramPath(grant.Path, source) { continue }
        canonical, err := p.service.canonicalSessionWorkspace(principal, grant.WorkspaceID, grant.WorkspaceGeneration)
        if err != nil { return "", 0, err }
        if sameTaskProgramPath(canonical.SourceWorkspacePath, source) || sameTaskProgramPath(canonical.RuntimeWorkspacePath, source) {
            if id != "" && id != canonical.WorkspaceID { return "", 0, errors.New("Task Program source has ambiguous canonical workspace identity") }
            id, generation = canonical.WorkspaceID, canonical.WorkspaceGeneration
        }
    }
    if id == "" { return "", 0, fmt.Errorf("Task Program source %q lacks a canonical authorized workspace grant", filepath.Base(source)) }
    return id, generation, nil
}

func (p *taskProgramScheduler) validateRepositoryLaneSource(lane pebblestore.TaskProgramRepositoryLane) error {
    id, generation, err := p.canonicalRepositorySource(lane.SourcePath)
    if err != nil { return err }
    if id != lane.WorkspaceID || generation != lane.WorkspaceGeneration {
        return errors.New("Task Program repository lane canonical source identity changed")
    }
    return nil
}
