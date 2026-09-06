package run

import (
	"encoding/json"
	"swarm/packages/swarmd/internal/tool"
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

// Purpose: test the actual approval producer/consumer pair, not just the local
// projection helper; neither side may invent an explicit Designer workspace.
func TestTaskProgramDesignerApprovalRoundTripKeepsImplicitTarget(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	bindTaskInheritanceModelProfile(t, svc, parentID)
	call := tool.Call{Name: "task", Arguments: mustJSON(t, map[string]any{"prompt": "Create two requested design alternatives", "launches": []any{map[string]any{"subagent_type": "designer", "meta_prompt": "First static card"}, map[string]any{"subagent_type": "designer", "meta_prompt": "Second static card"}}})}
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := svc.buildTaskLaunchPermissionPayload(parentID, "auto", call)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range manifest.Launches {
		if row.TargetWorkspacePath != "" {
			t.Fatal("approval invented Designer target")
		}
	}
	raw, err := json.Marshal(manifest.ApprovedArguments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseApprovedTaskLaunchManifest(string(raw), parsed.Launches); err != nil {
		t.Fatal(err)
	}
	parsed.Launches[0].TargetWorkspacePath = t.TempDir()
	if _, err := parseApprovedTaskLaunchManifest(string(raw), parsed.Launches); err == nil {
		t.Fatal("changed Designer target accepted")
	}
}
