package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestManagedDesignerArtifactContextAutoAcceptsRegularButNotSwarmOutputs(t *testing.T) {
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}
	regular := managedDesignerArtifactContext(parent, "regular-call", taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged}, 1)
	if regular == nil || !regular.AutoAccept {
		t.Fatalf("regular managed context = %+v, want trusted auto-accept", regular)
	}

	swarm := managedDesignerArtifactContext(parent, "swarm-call", taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, SwarmMode: true}, 1)
	if swarm == nil || swarm.AutoAccept {
		t.Fatalf("swarm managed context = %+v, want explicit multi-variant selection", swarm)
	}
}

func TestDirectManageArtifactCreateBecomesAcceptedHeadForFocusedDesignerSwarm(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "artifact-direct-source", UserID: "user-1", AccountScopeID: "account-1",
		Title: "Artifact source", WorkspacePath: workspace, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	authority := artifact.NewAuthority(artifact.NewRegistry(sessions, artifact.Limits{}), sessions)
	runtime := tool.NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := tool.WorkspaceScope{
		PrimaryPath: workspace, SessionID: parent.ID,
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: parent.UserID, AccountScopeID: parent.AccountScopeID, SessionID: parent.ID},
	}
	ctx := tool.WithArtifactRunContext(context.Background(), tool.ArtifactRunContext{SessionID: parent.ID, RunID: "run-direct"})
	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{
		CallID: "direct-create", Name: "manage_artifact",
		Arguments: `{"action":"create","collection_name":"Animation","filename":"animation.html","media_type":"text/html","content":"<html>ready</html>"}`,
	})
	if err != nil {
		t.Fatalf("direct manage_artifact create: %v", err)
	}
	var payload struct {
		Reference pebblestore.SessionArtifactSelectionReference `json:"reference"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode direct create: %v", err)
	}
	ref := payload.Reference
	if ref.SessionID != parent.ID || ref.CollectionID == "" || ref.VariantID == "" || ref.EventSeq == 0 {
		t.Fatalf("direct create reference = %+v", ref)
	}

	ready, ok, err := sessions.GetSessionArtifactVariant(parent.AccountScopeID, parent.ID, ref.CollectionID, ref.VariantID)
	if err != nil || !ok {
		t.Fatalf("load ready artifact: ok=%t err=%v", ok, err)
	}
	projected, chain, err := sessions.ProjectSessionArtifactVariantChain(parent.AccountScopeID, parent.UserID, ready)
	if err != nil {
		t.Fatalf("project accepted chain: %v", err)
	}
	if !ready.AutoAccept || projected.Status != pebblestore.SessionArtifactStatusReady || chain.Head.SessionID != ref.SessionID || chain.Head.CollectionID != ref.CollectionID || chain.Head.VariantID != ref.VariantID || chain.Head.EventSeq != ref.EventSeq {
		t.Fatalf("direct artifact was not the accepted ready head: ready=%+v chain=%+v ref=%+v", ready, chain, ref)
	}

	source := &pebblestore.SessionArtifactSelectionReference{SessionID: ref.SessionID, CollectionID: ref.CollectionID, VariantID: ref.VariantID, EventSeq: ref.EventSeq}
	sectionTarget := map[string]any{"id": "bloom", "label": "Bloom", "kind": "temporal", "start_ms": float64(4000), "end_ms": float64(8000)}
	specs := make([]taskLaunchSpec, 3)
	launches := make([]taskLaunchPrepared, 3)
	for index := range specs {
		specs[index] = taskLaunchSpec{
			RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, SwarmMode: true,
			SourceArtifact: source, SourceArguments: map[string]any{"swarm_index": index + 1, "section_target": sectionTarget},
		}
		run := managedDesignerArtifactContext(parent, "focused-swarm", specs[index], index+1)
		if run == nil || run.AutoAccept {
			t.Fatalf("focused swarm context %d = %+v", index+1, run)
		}
		run.ChildSessionID = "designer-child-" + string(rune('1'+index))
		launches[index] = taskLaunchPrepared{LaunchIndex: index + 1, ChildSession: pebblestore.SessionSnapshot{ID: run.ChildSessionID}, ArtifactRunContext: run, SourceArtifact: source}
	}

	service := &Service{sessions: sessions}
	if _, err := service.ensureManagedDesignerArtifactCollection(parent, "focused-swarm", specs, nil); err != nil {
		t.Fatalf("allocate focused swarm collection: %v", err)
	}
	if err := service.ensureManagedDesignerArtifactPlaceholders(parent, launches, nil); err != nil {
		t.Fatalf("allocate focused swarm placeholders from direct accepted head: %v", err)
	}
	for _, launch := range launches {
		placeholder, ok, err := sessions.GetSessionArtifactVariant(parent.AccountScopeID, parent.ID, launch.ArtifactRunContext.CollectionID, launch.ArtifactRunContext.VariantID)
		if err != nil || !ok || placeholder.Status != pebblestore.SessionArtifactStatusStaging || placeholder.AutoAccept {
			t.Fatalf("focused swarm placeholder = %+v ok=%t err=%v", placeholder, ok, err)
		}
	}
}
