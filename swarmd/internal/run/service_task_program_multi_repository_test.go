package run

import (
 "strings"
 "testing"

 pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: cross-repository dependency handoffs must never imply shared Git
// history; the scheduler must reject a missing source binding before touching
// another lane. This is the narrowest pure scheduler boundary for that threat.
func TestMultiRepositoryCoderSourceRequiresExplicitTarget(t *testing.T) {
 p := &taskProgramScheduler{record: pebblestore.TaskProgramRecord{RepositoryLanes: map[string]pebblestore.TaskProgramRepositoryLane{"repo": {SourcePath: "repo"}}}}
 _, err := p.coderSourceForJob(pebblestore.TaskProgramJobSpec{ID: "build"})
 if err == nil || !strings.Contains(err.Error(), "explicit workspace_path") { t.Fatalf("missing target err=%v", err) }
}

// Requirement: integration must never apply child A to an unrelated repository
// B merely because a stage contains both. Without a worktree authority, the
// barrier rejects before preparing or applying any integration.
func TestMultiRepositoryIntegrationRequiresWorktreeAuthority(t *testing.T) {
 p := &taskProgramScheduler{service: &Service{}, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}}}}
 if err := p.integrateMultiRepositoryStage(0); err == nil || !strings.Contains(err.Error(), "canonical task integration") { t.Fatalf("integration without authority err=%v", err) }
}
