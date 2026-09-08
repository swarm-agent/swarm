package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/tool"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: canonical Task Program filenames must survive parsing, durable job
// storage, child creation, and buildTaskDelegationPrompt without broadening the
// resolveRunWorkspaceScope/ScopeExpansionForCall mutation boundary. Real temp
// Git plus the V3 session store is the narrowest hermetic fixture for this path;
// no provider or delegated implementation is launched.
func TestTaskProgramOwnedScopeDeliveredWithoutBroadeningWrites(t *testing.T) {
	svc, parentID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, ok, err := svc.sessions.GetSession(parentID)
	if err != nil || !ok {
		t.Fatalf("load parent: ok=%v err=%v", ok, err)
	}
	want := []string{"src/assigned.go", "src/assigned_test.go"}
	args := taskProgramFixture(nil)
	program := args["program"].(map[string]any)
	jobs := program["jobs"].([]any)
	program["stages"] = program["stages"].([]any)[:1]
	program["jobs"] = jobs[:1]
	jobs[0].(map[string]any)["owned_scope"] = []any{" src/assigned.go ", "src/assigned_test.go"}
	parsed, err := parseTaskCallArguments(mustJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	record, err := taskProgramInitialRecord(parentID, "scope-run", "scope-call", parsed.Program)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.Definition.Jobs[0].OwnedScope, want) || !slices.Equal(parsed.Launches[0].OwnedScope, want) {
		t.Fatalf("canonical definition/launch mismatch: definition=%v launch=%v", record.Definition.Jobs[0].OwnedScope, parsed.Launches[0].OwnedScope)
	}
	repository := programFixtureRepo(t)
	childPath := filepath.Join(t.TempDir(), "child")
	programFixtureGit(t, repository, "worktree", "add", "-b", "agent/scope", childPath, "HEAD")
	stub := &taskLaunchWorktreeStub{allocation: worktreeruntime.Allocation{WorkspacePath: childPath, RepoRoot: repository, BaseBranch: "dev", BranchName: "agent/scope", WorkspaceID: "scope-workspace"}}
	svc.SetWorktreeService(stub)
	profile, virtual, source, err := svc.resolveTaskLaunchProfile(parent, "coder")
	if err != nil {
		t.Fatal(err)
	}
	base, err := stub.ResolveTaskBase(parent.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	spec := parsed.Launches[0]
	launch, err := svc.prepareDelegatedSubagentLaunchWithProfile(parent, sessionruntime.ModeAuto, taskLaunchPrepared{
		LaunchIndex: 1, RequestedSubagent: spec.RequestedSubagentType, MetaPrompt: spec.MetaPrompt,
		OwnedScope: append([]string(nil), spec.OwnedScope...), VirtualTarget: virtual, TaskBase: &base,
		LogicalTaskID: "scope-task", ProgramID: record.ProgramID, ProgramJobID: record.Jobs[0].JobID,
	}, parsed.Description, "", &profile, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, ok, err := svc.sessions.GetSession(launch.ChildSession.ID)
	if err != nil || !ok {
		t.Fatalf("load durable child: ok=%v err=%v", ok, err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: parent.ID, AccountScopeSource: identity.AccountScopeSourceSession}
	scope, err := svc.resolveRunWorkspaceScope(child, principal)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scope.MutationScopes, want) || len(stub.allocatedScopes) != 1 || !slices.Equal(stub.allocatedScopes[0], want) {
		t.Fatalf("write ownership changed: runtime=%v allocation=%v", scope.MutationScopes, stub.allocatedScopes)
	}
	for _, path := range append(append([]string(nil), want...), "src/unassigned.go", "src/assigned.go.bak", "src/unassigned/new.go") {
		_, expansion, err := tool.ScopeExpansionForCall(scope, tool.Call{Name: "write", Arguments: mustJSON(t, map[string]any{"path": filepath.Join(childPath, path), "content": "candidate"})})
		if slices.Contains(want, path) {
			if err != nil || expansion {
				t.Fatalf("exact owned file rejected %q: expansion=%v err=%v", path, expansion, err)
			}
		} else if err == nil || expansion {
			t.Fatalf("unowned file must fail, not request expansion %q: expansion=%v err=%v", path, expansion, err)
		}
		if _, err := os.Stat(filepath.Join(childPath, path)); !os.IsNotExist(err) {
			t.Fatalf("scope check created a file %q: %v", path, err)
		}
	}
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{
		Description:       parsed.Description,
		Prompt:            taskChildAssignmentPrompt(launch.MetaPrompt, parsed.Prompt, launch.ProgramID),
		RequestedSubagent: launch.RequestedSubagent, OwnedScope: launch.OwnedScope,
	})
	assertDeliveredOwnedScope(t, prompt, want)
	if strings.Contains(prompt, parsed.Prompt) || !strings.Contains(prompt, spec.MetaPrompt) {
		t.Fatal("scope delivery changed the program's child assignment boundary")
	}
	if !slices.Equal(launch.OwnedScope, want) || !slices.Equal(scope.MutationScopes, want) {
		t.Fatal("prompt construction mutated owned scope")
	}
}

// Purpose: buildTaskDelegationPrompt must expose exact canonical scope data to
// both repository agent types, preserving directory syntax and regular Coder's
// canonical whole-worktree default. It must not invent a scope when absent or
// turn Finder research scope into write permission. A prompt-boundary unit test
// isolates this omission from provider behavior and never launches an agent.
func TestTaskDelegationPromptExactOwnedScope(t *testing.T) {
	for _, agent := range []string{"coder", "finder"} {
		for _, scopes := range [][]string{{"src/one.go", "src/two_test.go"}, {"src/components/**"}, {"."}, nil} {
			t.Run(agent+"/"+strings.Join(scopes, ","), func(t *testing.T) {
				prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{RequestedSubagent: agent, OwnedScope: scopes, Prompt: "Perform only this assignment."})
				if len(scopes) == 0 {
					if strings.Contains(prompt, "- exact owned_scope") {
						t.Fatal("prompt invented missing scope")
					}
					return
				}
				assertDeliveredOwnedScope(t, prompt, scopes)
				if agent == "finder" && !strings.Contains(prompt, "research scope, not write permission") {
					t.Fatal("Finder scope lacks read-only boundary")
				}
			})
		}
	}
}

func assertDeliveredOwnedScope(t *testing.T, prompt string, want []string) {
	t.Helper()
	const prefix = "- exact owned_scope (backend supplied; workspace-relative JSON paths): "
	for _, line := range strings.Split(prompt, "\n") {
		if raw, ok := strings.CutPrefix(line, prefix); ok {
			var got []string
			if err := json.Unmarshal([]byte(raw), &got); err != nil || !slices.Equal(got, want) {
				t.Fatalf("child scope=%v want=%v err=%v", got, want, err)
			}
			return
		}
	}
	t.Fatal("child delegation prompt omitted exact canonical owned_scope")
}
