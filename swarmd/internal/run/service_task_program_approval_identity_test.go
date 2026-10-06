package run

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the approval producer, cohort slicer and launch consumer must retain
// a canonical repository selector while resolving source -> owned parent ->
// repository-specific lane. Real Git/Pebble fixtures are the narrowest proof of
// these boundaries and child base/isolation; no provider execution is needed.
// Two unrelated repositories, Finder reads and managed Designer's implicit root
// prevent a fix that accidentally routes every job through the primary lane.
func TestTaskProgramApprovalCanonicalIdentityAcrossRepositoryLanes(t *testing.T) {
	p, sources, bases := multiRepoProgramFixture(t, false)
	parent := p.parentSession
	parent.Metadata["swarm_v3_worktree_owner_session_id"] = parent.ID
	available := true
	for i := range parent.WorkspaceGrants {
		parent.WorkspaceGrants[i].Kind = pebblestore.WorkspaceGrantAdditional
		parent.WorkspaceGrants[i].Available = &available
	}
	if err := p.service.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: parent.ID, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID,
		Kind: sessionruntime.SessionMutationUpdateSettings, Session: &parent,
		ClientRequestID: "approval-parent", IdempotencyKey: "approval-parent", PayloadHash: "approval-parent", RequestHash: "approval-parent",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: parent.WorktreeRootPath, SourcePath: sources[0], OwnerSessionID: parent.ID, Branch: parent.WorktreeBranch, AllocatedRuntimeRoot: true},
	}); err != nil {
		t.Fatal(err)
	}
	jobs := []any{}
	for i, source := range sources {
		for _, agent := range []string{"coder", "finder"} {
			id := agent + []string{"-a", "-b"}[i]
			job := map[string]any{"id": id, "stage_id": "build", "agent_type": agent, "title": id, "meta_prompt": "Inspect source.txt", "deliverable": "report", "acceptance_criteria": []string{"done"}, "dependency_evidence": "ready", "workspace_path": source}
			if agent == "coder" {
				job["owned_scope"] = []string{"source.txt"}
			}
			jobs = append(jobs, job)
		}
	}
	jobs = append(jobs, map[string]any{"id": "design", "stage_id": "build", "agent_type": "designer", "title": "Design", "meta_prompt": "Create a static card", "deliverable": "artifact", "acceptance_criteria": []string{"done"}, "dependency_evidence": "ready", "output_mode": "managed"})
	call := tool.Call{Name: "task", Arguments: mustJSON(t, map[string]any{"action": "start", "prompt": "Implement and inspect each repository independently", "program": map[string]any{"id": "approval-identity", "stages": []any{map[string]any{"id": "build", "dependency_evidence": "ready"}}, "jobs": jobs}})}
	manifest, err := p.service.buildTaskLaunchPermissionPayload(parent.ID, "auto", call)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTaskCallArguments(call.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	for i := range parsed.Launches {
		launch := &parsed.Launches[i]
		target, _, err := p.service.resolveTaskTargetWorkspace(parent, identity.Principal{}, launch)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && target != parent.WorktreeRootPath {
			t.Fatalf("source did not resolve through parent: %q", target)
		}
		retainTaskResolvedWorkspace(parent, launch, parsed.Program, i, target)
		want := ""
		if i < 4 {
			want = sources[i/2]
		}
		if launch.TargetWorkspacePath != want || parsed.Program.Jobs[i].TargetWorkspacePath != want || manifest.Launches[i].TargetWorkspacePath != want {
			t.Fatalf("job %d lost immutable selector: launch=%q definition=%q approval=%q want=%q", i, launch.TargetWorkspacePath, parsed.Program.Jobs[i].TargetWorkspacePath, manifest.Launches[i].TargetWorkspacePath, want)
		}
	}
	// Defaults and the exact owned-runtime alias retain the same source for a
	// program, while ordinary non-program launches keep their runtime target.
	for _, selector := range []string{"", ".", sources[0], parent.WorktreeRootPath} {
		for _, agent := range []string{"coder", "finder"} {
			spec := taskLaunchSpec{RequestedSubagentType: agent, TargetWorkspacePath: selector}
			target, _, err := p.service.resolveTaskTargetWorkspace(parent, identity.Principal{}, &spec)
			if err != nil {
				t.Fatal(err)
			}
			program := &taskProgramSpec{Jobs: []taskProgramJob{{RequestedSubagentType: agent}}}
			retainTaskResolvedWorkspace(parent, &spec, program, 0, target)
			if spec.TargetWorkspacePath != sources[0] || program.Jobs[0].TargetWorkspacePath != sources[0] {
				t.Fatalf("%s selector %q lost captured source", agent, selector)
			}
			retainTaskResolvedWorkspace(parent, &spec, nil, 0, target)
			if spec.TargetWorkspacePath != parent.WorktreeRootPath {
				t.Fatal("regular launch runtime projection changed")
			}
		}
	}
	raw, err := json.Marshal(manifest.ApprovedArguments)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseApprovedTaskLaunchManifest(string(raw), parsed.Launches); err != nil {
		t.Fatalf("initial approved admission: %v", err)
	}
	for i, spec := range parsed.Launches {
		// Match scheduler lane selection, then ordinary cohort admission.
		if i < 4 {
			lane := p.record.RepositoryLanes[sources[i/2]]
			spec.ProgramRepositoryLane = &lane
		}
		approved, err := taskProgramApprovedCohort(string(raw), parsed.Launches, []taskLaunchSpec{spec})
		if err != nil {
			t.Fatal(err)
		}
		target, _, err := p.service.resolveTaskTargetWorkspace(parent, identity.Principal{}, &spec)
		if err != nil {
			t.Fatal(err)
		}
		retainTaskResolvedWorkspace(parent, &spec, nil, 0, target)
		cohort, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec})
		if err != nil {
			t.Fatalf("job %d approved cohort: %v", i, err)
		}
		if i >= 4 {
			if spec.TargetWorkspacePath != "" || target != parent.WorktreeRootPath {
				t.Fatal("managed Designer acquired explicit repository authority")
			}
			continue
		}
		lane := p.record.RepositoryLanes[sources[i/2]]
		if target != lane.WorkspacePath || target == parent.WorktreeRootPath || cohort.Launches[0].TargetWorkspacePath != lane.SourcePath {
			t.Fatalf("job %d lost repository lane: %q %+v", i, target, lane)
		}
		base, err := p.service.worktrees.ResolveTaskBase(target)
		if err != nil || base.BaseCommit != bases[i/2] {
			t.Fatalf("wrong captured base: %+v %v", base, err)
		}
		if spec.RequestedSubagentType == "coder" {
			row := cohort.Launches[0]
			child, err := p.service.prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{RequestedSubagent: "coder", MetaPrompt: spec.MetaPrompt, TargetWorkspacePath: target, TaskBase: &base, OwnedScope: spec.OwnedScope, VirtualTarget: row.ParentCopy, LogicalTaskID: "approval-" + mapString(spec.SourceArguments, "program_job_id")}, "scoped work", "", row.ProfileSnapshot, row.SourceAgentName, nil)
			if err != nil {
				t.Fatal(err)
			}
			if child.ChildWorkspacePath == target || programFixtureGit(t, child.ChildWorkspacePath, "rev-parse", "HEAD") != base.BaseCommit || programFixtureGit(t, child.ChildWorkspacePath, "rev-parse", "--path-format=absolute", "--git-common-dir") != programFixtureGit(t, lane.SourcePath, "rev-parse", "--path-format=absolute", "--git-common-dir") {
				t.Fatal("child lost isolated repository or exact lane base")
			}
		}
	}
	for i, source := range sources {
		if programFixtureGit(t, source, "rev-parse", "HEAD") != bases[i] || programFixtureGit(t, source, "status", "--porcelain") != "" || programFixtureGit(t, p.record.RepositoryLanes[source].WorkspacePath, "rev-parse", "HEAD") != bases[i] {
			t.Fatal("approval/preparation changed captured source or lane head")
		}
	}
}

// Purpose: retaining a source selector must not turn approval into same-repo
// path equivalence. parseApprovedTaskLaunchManifest rejects source substitution;
// resolveTaskTargetWorkspace and repositoryLaneForSource reject unrelated,
// unowned, stale or cross-account authority without Git/program mutations.
func TestTaskProgramApprovalIdentityRejectsRetargeting(t *testing.T) {
	p, sources, _ := multiRepoProgramFixture(t, false)
	lane := p.record.RepositoryLanes[sources[0]]
	profile := pebblestore.AgentProfile{Name: "coder"}
	manifest := taskLaunchManifest{Program: &taskProgramSpec{ID: "approved"}, Launches: []taskLaunchManifestRow{{RequestedSubagentType: "coder", TargetWorkspacePath: sources[0], ProfileSnapshot: &profile}}}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = digest
	approved := mustJSON(t, map[string]any{"manifest_hash": digest, "manifest": manifest})
	inventory := [2]string{programFixtureGit(t, sources[0], "worktree", "list", "--porcelain"), programFixtureGit(t, sources[1], "worktree", "list", "--porcelain")}
	unowned := t.TempDir()
	programFixtureGit(t, sources[0], "worktree", "add", "-b", "unowned", unowned)
	inventory[0] = programFixtureGit(t, sources[0], "worktree", "list", "--porcelain")
	before, ok, err := p.service.sessions.GetTaskProgram(p.parentSession.ID, p.record.ProgramID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	for _, target := range []string{sources[1], p.record.RepositoryLanes[sources[1]].WorkspacePath, p.parentSession.WorktreeRootPath, unowned} {
		spec := taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: target, ProgramRepositoryLane: &lane}
		if _, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "workspace target mismatch") {
			t.Fatalf("accepted unrelated approved target %q: %v", target, err)
		}
	}
	badLane := lane
	badLane.SourcePath = sources[1]
	spec := taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: lane.WorkspacePath, ProgramRepositoryLane: &badLane}
	if _, err := parseApprovedTaskLaunchManifest(approved, []taskLaunchSpec{spec}); err == nil {
		t.Fatal("accepted different approved lane source")
	}
	if _, _, err := p.service.resolveTaskTargetWorkspace(p.parentSession, identity.Principal{}, &spec); err == nil {
		t.Fatal("accepted cross-repository lane binding")
	}
	for _, target := range []string{lane.WorkspacePath, unowned, t.TempDir()} {
		if _, _, err := p.service.resolveTaskTargetWorkspace(p.parentSession, identity.Principal{}, &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: target}); err == nil {
			t.Fatal("path-only selector granted unrelated worktree/root authority")
		}
	}
	stale := p.parentSession
	stale.WorktreeBranch = "agent/unrelated"
	if _, _, err := p.service.resolveTaskTargetWorkspace(stale, identity.Principal{}, &taskLaunchSpec{RequestedSubagentType: "coder", TargetWorkspacePath: sources[0]}); err == nil {
		t.Fatal("stale parent source translation accepted")
	}
	p.service.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		if input.Principal.AccountScopeID != p.parentSession.AccountScopeID || input.Principal.UserID != p.parentSession.UserID {
			return SessionWorkspaceCanonicalization{}, errors.New("foreign workspace principal")
		}
		return SessionWorkspaceCanonicalization{}, errors.New("unexpected canonical lookup")
	})
	p.req.Principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: p.parentSession.UserID, AccountScopeID: "foreign"}
	if _, err := p.repositoryLaneForSource(sources[0], true); err == nil || !strings.Contains(err.Error(), "foreign workspace principal") {
		t.Fatalf("cross-account lane accepted: %v", err)
	}
	p.req.Principal = identity.Principal{}
	p.service.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		return SessionWorkspaceCanonicalization{WorkspaceID: input.WorkspaceID, WorkspaceGeneration: 2, WorkspaceState: "active", WorkspaceName: "repo", SourceWorkspacePath: sources[0], RuntimeWorkspacePath: sources[0], WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
	})
	if _, err := p.repositoryLaneForSource(sources[0], true); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale catalog generation accepted: %v", err)
	}
	after, ok, err := p.service.sessions.GetTaskProgram(p.parentSession.ID, p.record.ProgramID)
	if err != nil || !ok || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected identity changed durable program")
	}
	for i, source := range sources {
		if programFixtureGit(t, source, "worktree", "list", "--porcelain") != inventory[i] || programFixtureGit(t, source, "status", "--porcelain") != "" {
			t.Fatal("rejected identity changed repository state")
		}
	}
}
