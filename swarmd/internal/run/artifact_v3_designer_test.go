package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type artifactV3CoordinatorFake struct {
	prepared []tool.ArtifactV3PrepareTurnRequest
	failures []tool.ArtifactV3TurnFailure
}

func (f *artifactV3CoordinatorFake) MaterializeBase(context.Context, string, string, string) error { return nil }
func (f *artifactV3CoordinatorFake) SubmitProject(context.Context, tool.ArtifactV3SubmitRequest) (tool.ArtifactV3Revision, error) {
	return tool.ArtifactV3Revision{}, errors.New("not used")
}
func (f *artifactV3CoordinatorFake) PrepareArtifactV3Turn(_ context.Context, request tool.ArtifactV3PrepareTurnRequest) (tool.ArtifactV3AuthorGrant, error) {
	f.prepared = append(f.prepared, request)
	index := len(f.prepared)
	return tool.ArtifactV3AuthorGrant{
		ID: "grant-" + fmt.Sprint(index), ArtifactID: firstNonEmptyString(request.ArtifactID, "artifact-new-"+fmt.Sprint(index)), OwnerSessionID: request.OwnerSessionID,
		TurnID: "turn-" + fmt.Sprint(index), CandidateID: "candidate-" + fmt.Sprint(index), BaseCommitOID: request.BaseCommitOID, Initial: request.Initial,
		TargetPartIDs: append([]string(nil), request.TargetPartIDs...), AllowedActions: []string{"inspect_context", "list_files", "read_file", "create_file", "edit_file", "rename_file", "delete_file", "diff", "build_preview", "finish_turn"}, PolicyRevision: request.PolicyRevision, ExpiresAt: request.ExpiresAt,
	}, nil
}
func (f *artifactV3CoordinatorFake) FailArtifactV3Turn(_ context.Context, failure tool.ArtifactV3TurnFailure) error {
	f.failures = append(f.failures, failure)
	return nil
}

// Requirement: managed Designer creation and source-bound follow-up allocate V3
// whole-project turns, preserving exact commit/projection and target intent.
// Threat: routing could fall back to V2 part allocation or lose exact source CAS.
func TestAllocateManagedDesignerArtifactV3InitialAndFollowup(t *testing.T) {
	coordinator := &artifactV3CoordinatorFake{}
	runtime := tool.NewRuntime(1)
	runtime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), coordinator, nil, nil))
	svc := &Service{tools: runtime}
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}

	contexts, err := svc.allocateManagedDesignerArtifactV3(context.Background(), parent, "call", []taskLaunchSpec{
		{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "create"},
		{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "edit", ArtifactV3Source: &taskArtifactV3Source{SessionID: "parent", ArtifactID: "artifact-old", CommitOID: "commit-old", ProjectionSeq: 41, TargetPartIDs: []string{"pricing"}}, SourceArguments: map[string]any{"section_target": &taskSwarmSectionTarget{ID: "hero", Label: "Hero"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 2 || contexts[0] == nil || contexts[1] == nil {
		t.Fatalf("contexts=%#v", contexts)
	}
	if !coordinator.prepared[0].Initial || coordinator.prepared[0].ArtifactID != "" || coordinator.prepared[0].BaseCommitOID != "" {
		t.Fatalf("initial=%#v", coordinator.prepared[0])
	}
	followup := coordinator.prepared[1]
	if followup.Initial || followup.OwnerSessionID != "parent" || followup.ArtifactID != "artifact-old" || followup.BaseCommitOID != "commit-old" || followup.ProjectionSeq != 41 {
		t.Fatalf("followup=%#v", followup)
	}
	if len(followup.TargetPartIDs) != 2 || followup.TargetPartIDs[0] != "hero" || followup.TargetPartIDs[1] != "pricing" { t.Fatalf("targets=%v", followup.TargetPartIDs) }
	if runtime.ArtifactV2AuthorService() != nil {
		t.Fatal("V2 author service unexpectedly configured")
	}
	boundFailure := tool.BindArtifactV3AuthorRunContext(contexts[0], "run-failed")
	if err := runtime.ArtifactV3AuthorService().MarkFailed(context.Background(), boundFailure.Grant, "child_run_failed", "safe diagnostic"); err != nil {
		t.Fatal(err)
	}
	if len(coordinator.failures) != 1 || coordinator.failures[0].ProducerRunID != "run-failed" { t.Fatalf("failures=%#v", coordinator.failures) }

	parsed, err := parseTaskCallArguments(`{"mode":"regular","prompt":"edit pricing","artifact_v3_source":{"session_id":"parent","artifact_id":"artifact-old","commit_oid":"commit-old","projection_seq":41,"target_part_ids":["pricing"]},"section_target":{"id":"pricing","label":"Pricing","kind":"semantic"},"subagent_type":"designer","meta_prompt":"edit","output_mode":"managed"}`)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ArtifactV3Source == nil || parsed.ArtifactV3Source.SessionID != "parent" || parsed.ArtifactV3Source.CommitOID != "commit-old" || parsed.Launches[0].ArtifactV3Source == nil { t.Fatalf("parsed=%#v", parsed) }
	manifestRow := taskLaunchManifestRow{ArtifactV3Source: cloneTaskArtifactV3Source(parsed.Launches[0].ArtifactV3Source)}
	if manifestRow.ArtifactV3Source == nil || manifestRow.ArtifactV3Source.ProjectionSeq != 41 { t.Fatalf("manifest row=%#v", manifestRow) }
	if _, err := parseTaskCallArguments(`{"mode":"regular","prompt":"edit","artifact_v3_source":{"session_id":"parent","artifact_id":"a","commit_oid":"c","projection_seq":1},"source_artifact":{"session_id":"s","collection_id":"c","variant_id":"v","event_seq":1},"subagent_type":"designer","meta_prompt":"edit","output_mode":"managed"}`); err == nil || !strings.Contains(err.Error(), "not both") { t.Fatalf("mixed source err=%v", err) }
	legacy := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "legacy", SourceArtifact: &pebblestore.SessionArtifactSelectionReference{SessionID: "s", CollectionID: "c", VariantID: "v", EventSeq: 1}}
	if _, err := svc.allocateManagedDesignerArtifactV3(context.Background(), parent, "legacy", []taskLaunchSpec{legacy}); err == nil || !strings.Contains(err.Error(), "requires artifact_v3_source") { t.Fatalf("legacy route err=%v", err) }
}

func TestArtifactV3LaunchBindingAndPrompt(t *testing.T) {
	grant := tool.ArtifactV3AuthorGrant{ID: "grant", ArtifactID: "artifact", OwnerSessionID: "parent", TurnID: "turn", CandidateID: "candidate", BaseCommitOID: "base", TargetPartIDs: []string{"pricing"}, LockedPaths: []string{"brand/logo.svg"}}
	profile := pebblestore.AgentProfile{Name: "designer", Protected: true, Mode: "subagent"}
	sessionStore, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sessionStore.Close()
	store := pebblestore.NewSessionStore(sessionStore)
	sessions := sessionruntime.NewService(store, nil)
	if err := store.CreateSession(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user", WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	parent, _, _ := sessions.GetSession("parent")
	prepared, err := (&Service{sessions: sessions}).prepareDelegatedSubagentLaunchWithProfile(parent, "auto", taskLaunchPrepared{RequestedSubagent: "designer", MetaPrompt: "edit", OutputMode: taskOutputModeManaged, ArtifactV3AuthorContext: &tool.ArtifactV3AuthorRunContext{Grant: grant}}, "edit", "", &profile, "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ArtifactV3AuthorContext.Grant.ProducerSessionID != prepared.ChildSession.ID { t.Fatalf("producer=%q child=%q", prepared.ArtifactV3AuthorContext.Grant.ProducerSessionID, prepared.ChildSession.ID) }
	bound := tool.BindArtifactV3AuthorRunContext(prepared.ArtifactV3AuthorContext, "run-1")
	if bound == nil || bound.Grant.ProducerRunID != "run-1" || prepared.ArtifactV3AuthorContext.Grant.ProducerRunID != "" { t.Fatalf("run binding mutated source: bound=%#v source=%#v", bound, prepared.ArtifactV3AuthorContext) }
	if prepared.ChildSession.Metadata["artifact_v3_artifact_id"] != "artifact" || prepared.ChildSession.Metadata["artifact_v3_base_commit_oid"] != "base" { t.Fatalf("metadata=%#v", prepared.ChildSession.Metadata) }
	if _, legacy := prepared.ChildSession.Metadata["artifact_v2_managed_output"]; legacy { t.Fatalf("V2 metadata leaked: %#v", prepared.ChildSession.Metadata) }
	prompt := buildTaskDelegationPrompt(taskDelegationPromptConfig{RequestedSubagent: "designer", OutputMode: taskOutputModeManaged, ArtifactV3AuthorContext: prepared.ArtifactV3AuthorContext})
	for _, required := range []string{"managed Artifact V3", "complete conventional project tree", "finish_turn exactly once", "pricing", "brand/logo.svg"} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(required)) { t.Fatalf("prompt missing %q:\n%s", required, prompt) }
	}
	for _, forbidden := range []string{"write parts strictly one at a time", "managed Artifact V2"} {
		if strings.Contains(strings.ToLower(prompt), strings.ToLower(forbidden)) { t.Fatalf("prompt retained %q:\n%s", forbidden, prompt) }
	}
}

