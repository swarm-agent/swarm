package run

import (
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeTaskTool's resolved workspace projection must remain valid on
// the scheduler's second admission pass. An implicit Designer read root cannot
// become a prohibited explicit selector. The narrow projection/resolver test
// proves both read-root reuse and rejection of caller-authored Designer targets.
func TestTaskProgramDesignerImplicitRootSurvivesCohortAdmission(t *testing.T) {
	root := t.TempDir()
	parent := pebblestore.SessionSnapshot{WorkspacePath: root, WorktreeRootPath: root}
	svc := &Service{}
	launch := taskLaunchSpec{RequestedSubagentType: "designer"}
	program := &taskProgramSpec{Jobs: []taskProgramJob{{RequestedSubagentType: "designer"}}}
	target, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, launch)
	if err != nil || target != root {
		t.Fatalf("first admission: %q %v", target, err)
	}
	retainTaskResolvedWorkspace(&launch, program, 0, target)
	if launch.TargetWorkspacePath != "" || program.Jobs[0].TargetWorkspacePath != "" {
		t.Fatal("implicit root persisted as explicit target")
	}
	target, _, err = svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, launch)
	if err != nil || target != root {
		t.Fatalf("cohort admission: %q %v", target, err)
	}
	launch.TargetWorkspacePath = root
	if _, _, err := svc.resolveTaskTargetWorkspace(parent, identity.Principal{}, launch); err == nil {
		t.Fatal("explicit Designer workspace target accepted")
	}
	for _, agent := range []string{"coder", "finder"} {
		repositoryLaunch := taskLaunchSpec{RequestedSubagentType: agent}
		repositoryProgram := &taskProgramSpec{Jobs: []taskProgramJob{{RequestedSubagentType: agent}}}
		retainTaskResolvedWorkspace(&repositoryLaunch, repositoryProgram, 0, root)
		if repositoryLaunch.TargetWorkspacePath != root || repositoryProgram.Jobs[0].TargetWorkspacePath != root {
			t.Fatalf("%s target lost", agent)
		}
	}
}
