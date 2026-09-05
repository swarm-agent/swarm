package run

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: viewer selections must reach both regular and swarm Designer
// allocation as exact native sources, never empty legacy references. Threat:
// implicit message binding previously bypassed parser coverage and routed V3
// into GetReference. Exercise the production binding and capability allocation
// boundary with no legacy authority; fake only the coordinator/provider edge.
func TestTaskNativeSelectionDispatch(t *testing.T) {
	ids := []string{"orbit", "legend"}
	selection := pebblestore.SessionArtifactSelectionReference{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, TargetPartIDs: &ids, Action: "use"}
	selection.RevisionRef = "revision-" + selection.CommitOID
	for _, mode := range []string{"regular", "swarm"} {
		for _, explicit := range []bool{false, true} {
			t.Run(mode+"/explicit="+map[bool]string{true: "yes", false: "no"}[explicit], func(t *testing.T) {
				args := map[string]any{"mode": mode, "prompt": "Revise selected Parts"}
				if mode == "swarm" {
					args["agent_type"], args["count"] = "designer", 2
				} else {
					args["launches"] = []map[string]any{{"subagent_type": "designer", "meta_prompt": "first"}, {"subagent_type": "designer", "meta_prompt": "second"}}
				}
				if explicit {
					args["artifact_v3_source"] = &taskArtifactV3Source{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, CommitOID: selection.CommitOID, ProjectionSeq: selection.ProjectionSeq, TargetPartIDs: ids}
				}
				body, _ := json.Marshal(args)
				parsed, err := parseTaskCallArguments(string(body))
				if err != nil {
					t.Fatal(err)
				}
				selected := latestTaskArtifactUseSelection([]pebblestore.MessageSnapshot{{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{selection}}})
				if err := bindTaskNativeArtifactSelection(&parsed, parsed.Launches, selected); err != nil {
					t.Fatal(err)
				}
				if parsed.SourceArtifact != nil {
					t.Fatal("native selection became legacy")
				}
				coordinator := &artifactV3CoordinatorFake{}
				runtime := tool.NewRuntime(1)
				runtime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), coordinator, nil, nil))
				svc := &Service{tools: runtime}
				contexts, err := svc.allocateManagedDesignerArtifactV3(context.Background(), pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "wave", parsed.Launches)
				if err != nil || len(contexts) != 2 {
					t.Fatalf("allocation: %+v %v", contexts, err)
				}
				for i, request := range coordinator.prepared {
					if request.Initial || request.ArtifactID != selection.ArtifactID || request.BaseCommitOID != selection.CommitOID || request.ProjectionSeq != 9 || !reflect.DeepEqual(request.TargetPartIDs, ids) || request.CandidateIndex != i+1 {
						t.Fatalf("source lost: %+v", request)
					}
					if parsed.Launches[i].SourceArtifact != nil {
						t.Fatal("legacy launch binding")
					}
				}
			})
		}
	}
}

// Requirement: conflicting exact identities or Part sets fail before any launch
// is changed; prior user attachments cannot hijack a new request. Owner/head
// authentication is separately exercised at the real runtime/Git boundary.
func TestTaskNativeSelectionRejectsMismatchWithoutMutation(t *testing.T) {
	ids := []string{"orbit"}
	selection := &pebblestore.SessionArtifactSelectionReference{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, TargetPartIDs: &ids, Action: "use"}
	selection.RevisionRef = "revision-" + selection.CommitOID
	for _, field := range []string{"session", "artifact", "commit", "projection", "parts", "legacy", "hint"} {
		t.Run(field, func(t *testing.T) {
			source := &taskArtifactV3Source{SessionID: "parent", ArtifactID: "artifact", CommitOID: selection.CommitOID, ProjectionSeq: 9, TargetPartIDs: []string{"orbit"}}
			launch := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, ArtifactV3Source: source}
			switch field {
			case "session":
				source.SessionID = "foreign"
			case "artifact":
				source.ArtifactID = "other"
			case "commit":
				source.CommitOID = strings.Repeat("b", 40)
			case "projection":
				source.ProjectionSeq++
			case "parts":
				source.TargetPartIDs = []string{"other"}
			case "legacy":
				launch.SourceArtifact = &pebblestore.SessionArtifactSelectionReference{CollectionID: "legacy"}
			case "hint":
				launch.SourceArguments = map[string]any{"section_target": &taskSwarmSectionTarget{ID: "other", Label: "Other", Kind: "semantic"}}
			}
			parsed := taskCallArguments{Launches: []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged}, launch}}
			before, _ := json.Marshal(parsed)
			if err := bindTaskNativeArtifactSelection(&parsed, parsed.Launches, selection); err == nil {
				t.Fatal("accepted mismatch")
			}
			after, _ := json.Marshal(parsed)
			if string(before) != string(after) {
				t.Fatal("rejected binding partially mutated launches")
			}
		})
	}
	messages := []pebblestore.MessageSnapshot{{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{*selection}}, {Role: "user", Content: "A different task"}}
	if latestTaskArtifactUseSelection(messages) != nil {
		t.Fatal("old selection leaked into latest request")
	}
}

// Requirement: permission replay pins the native source, not merely output mode
// or legacy nil identity. Threat: source/Part substitution after approval. The
// signed-manifest comparison is the narrowest production boundary proving it.
func TestTaskNativeSelectionApprovedManifest(t *testing.T) {
	source := &taskArtifactV3Source{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, TargetPartIDs: []string{"orbit"}}
	spec := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, ArtifactV3Source: cloneTaskArtifactV3Source(source)}
	manifest := taskLaunchManifest{Launches: []taskLaunchManifestRow{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, ArtifactV3Source: cloneTaskArtifactV3Source(source), ProfileSnapshot: &pebblestore.AgentProfile{}, ResolvedTools: &taskLaunchResolvedToolSummary{AllowedTools: []string{"artifact_v3_author"}, DisabledTools: []string{"write", "edit"}}, DisabledTools: []string{"write", "edit"}}}}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = digest
	body, _ := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	if _, err := parseApprovedTaskLaunchManifest(string(body), []taskLaunchSpec{spec}); err != nil {
		t.Fatal(err)
	}
	spec.ArtifactV3Source.TargetPartIDs = []string{"other"}
	if _, err := parseApprovedTaskLaunchManifest(string(body), []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "native source artifact mismatch") {
		t.Fatalf("substitution accepted: %v", err)
	}
}

// Requirement: the real permission builder and replay path preserve native
// source fields for regular and swarm launches. No legacy artifact authority is
// needed to construct the approval; coordinator authentication follows at run.
func TestTaskNativeSelectionPermissionPayload(t *testing.T) {
	svc, parent, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	for _, mode := range []string{"regular", "swarm"} {
		t.Run(mode, func(t *testing.T) {
			args := map[string]any{"mode": mode, "prompt": "revise", "artifact_v3_source": map[string]any{"session_id": parent, "artifact_id": "artifact", "commit_oid": strings.Repeat("a", 40), "projection_seq": 9, "target_part_ids": []string{"orbit"}}, "section_target": map[string]any{"id": "orbit", "label": "Orbit", "kind": "selector"}}
			if mode == "regular" {
				args["subagent_type"], args["meta_prompt"] = "designer", "revise orbit"
			} else {
				args["agent_type"], args["count"] = "designer", 2
			}
			body, _ := json.Marshal(args)
			call := tool.Call{Name: "task", Arguments: string(body)}
			manifest, err := svc.buildTaskLaunchPermissionPayload(parent, "auto", call)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseTaskCallArguments(call.Arguments)
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range manifest.Launches {
				if row.SourceArtifact != nil || !reflect.DeepEqual(row.ArtifactV3Source, parsed.Launches[i].ArtifactV3Source) {
					t.Fatalf("approval source lost: %+v", row)
				}
				parsed.Launches[i].TargetWorkspacePath = row.TargetWorkspacePath
				ids, err := artifactV3TargetPartIDs(parsed.Launches[i])
				if err != nil || !reflect.DeepEqual(ids, []string{"orbit"}) {
					t.Fatalf("native hint allocation: %v %v", ids, err)
				}
			}
			approved, _ := json.Marshal(manifest.ApprovedArguments)
			if _, err := parseApprovedTaskLaunchManifest(string(approved), parsed.Launches); err != nil {
				t.Fatal(err)
			}
		})
	}
}
