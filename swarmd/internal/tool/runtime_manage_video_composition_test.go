package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videocomposition"
	"swarm/packages/swarmd/internal/videoproject"
)

func TestManageVideoCompositionProposalPreservesPlanAndProviderContract(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "manage-video-composition.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const workspaceID = "composition-workspace"
	principal := identity.Principal{Type: identity.PrincipalTypeUser, SessionID: "studio-composition", UserID: "user-1", AccountScopeID: "account-1"}
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{
		ID: principal.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: "/ws", Mode: "auto", Metadata: map[string]any{"lineage_kind": "video_project", "workspace_id": workspaceID},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(1)
	runtime.sessions = sessionruntime.NewService(sessionStore, events)
	runtime.videoProjects = videoproject.NewService(sessionStore)
	ctx := WithVideoRunContext(context.Background(), VideoRunContext{SessionID: principal.SessionID, RunID: "run-composition"})
	scope := WorkspaceScope{SessionID: principal.SessionID, Principal: principal}

	capabilities, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "capabilities", Name: "manage_video", Arguments: `{"action":"capabilities"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"inspect_composition"`, `"update_composition"`} {
		if !strings.Contains(capabilities, required) {
			t.Fatalf("provider-visible capabilities lack %s: %s", required, capabilities)
		}
	}
	properties := manageVideoDefinition().Parameters["properties"].(map[string]any)
	if properties["expected_revision_id"] == nil {
		t.Fatal("provider-visible manage_video schema lacks expected_revision_id")
	}

	created, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create", Name: "manage_video", Arguments: `{"action":"create_project","title":"Spatial proposal"}`})
	if err != nil {
		t.Fatal(err)
	}
	var create struct {
		ProjectID  string `json:"project_id"`
		RevisionID string `json:"revision_id"`
	}
	if err := json.Unmarshal([]byte(created), &create); err != nil {
		t.Fatal(err)
	}

	collection := pebblestore.SessionArtifactCollection{Version: pebblestore.SessionArtifactVersion, ID: "fallbacks", AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusReady, Name: "Fallbacks", VariantCount: 1, ReadyCount: 1, EventSeq: 10}
	variant := pebblestore.SessionArtifactVariant{Version: pebblestore.SessionArtifactVersion, ID: "fallback", CollectionID: collection.ID, AccountScopeID: principal.AccountScopeID, SessionID: principal.SessionID, Status: pebblestore.SessionArtifactStatusReady, Filename: "fallback.png", MediaType: "image/png", EventSeq: 10}
	if err := store.PutJSON(pebblestore.KeySessionArtifactCollection(principal.AccountScopeID, principal.SessionID, collection.ID), collection); err != nil {
		t.Fatal(err)
	}
	if err := store.PutJSON(pebblestore.KeySessionArtifactVariant(principal.AccountScopeID, principal.SessionID, collection.ID, variant.ID), variant); err != nil {
		t.Fatal(err)
	}

	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "source.mp4")
	if err := os.WriteFile(sourcePath, []byte("00000000ftypisom-spatial-composition"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sessionStore.PutVideoSourceRecord(pebblestore.VideoSourceRecord{
		AccountScopeID: principal.AccountScopeID, WorkspaceID: workspaceID, RootPath: sourceRoot,
		RelativePath: "source.mp4", DisplayName: "source.mp4", MIMEType: "video/mp4",
		SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	visual := map[string]any{"session_id": principal.SessionID, "collection_id": collection.ID, "variant_id": variant.ID, "event_seq": variant.EventSeq}
	sourceBinding := map[string]any{
		"source_ref": source.Ref, "media_type": "video/mp4", "source_start_ms": 0, "source_end_ms": 1000,
		"timeline_start_ms": 0, "timeline_end_ms": 1000, "audio_policy": "mute",
	}
	plan := map[string]any{
		"kind": "initial",
		"composition_catalog": map[string]any{"schema_version": 1, "layouts": []map[string]any{{
			"id": "split", "slots": []map[string]any{{
				"id": "main", "requirement": "Primary source", "geometry": map[string]any{"x": 0.0, "y": 0.0, "width": 1.0, "height": 1.0},
				"z_index": 1, "fit": "cover", "alignment_x": 0.5, "alignment_y": 0.5, "mask": map[string]any{"kind": "none"}, "source": sourceBinding,
			}},
		}}},
		"parts": []map[string]any{{"id": "part-1", "title": "Spatial shot", "duration_ms": 1000, "visual": visual, "composition": map[string]any{"layout_id": "split"}}},
	}
	proposalArgs, err := json.Marshal(map[string]any{"action": "propose_plan", "project_id": create.ProjectID, "base_revision_id": create.RevisionID, "plan": plan})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "proposal", Name: "manage_video", Arguments: string(proposalArgs)})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Proposal struct {
			ID                string                         `json:"id"`
			WorkingRevisionID string                         `json:"working_revision_id"`
			Plan              *pebblestore.VideoPlanProposal `json:"plan"`
		} `json:"proposal"`
	}
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		t.Fatal(err)
	}
	if response.Proposal.Plan == nil || response.Proposal.Plan.CompositionCatalog == nil || len(response.Proposal.Plan.Parts) != 1 || response.Proposal.Plan.Parts[0].Composition == nil {
		t.Fatalf("proposal lost spatial composition data: %s", payload)
	}
	resolved, err := videocomposition.Resolve(response.Proposal.Plan.CompositionCatalog, response.Proposal.Plan.Parts[0].Composition, 1920, 1080, 1000)
	if err != nil || len(resolved) != 1 || resolved[0].Source == nil || resolved[0].Source.SourceRef != source.Ref {
		t.Fatalf("proposal lost registered source binding: resolved=%#v err=%v", resolved, err)
	}

	stored, ok, err := runtime.videoProjects.GetEditProposal(principal, principal.SessionID, create.ProjectID, response.Proposal.ID)
	if err != nil || !ok || !reflect.DeepEqual(stored.Plan, response.Proposal.Plan) {
		t.Fatalf("stored proposal differs from response: stored=%#v response=%#v ok=%v err=%v", stored.Plan, response.Proposal.Plan, ok, err)
	}
	working, ok, err := runtime.videoProjects.GetRevision(principal, principal.SessionID, create.ProjectID, response.Proposal.WorkingRevisionID)
	if err != nil || !ok || working.Timeline.CompositionCatalog == nil {
		t.Fatalf("working revision lost composition catalog: revision=%#v ok=%v err=%v", working, ok, err)
	}
	acceptedBytes, err := json.Marshal(working.Timeline.Metadata["accepted_video_plan"])
	if err != nil {
		t.Fatal(err)
	}
	var accepted pebblestore.VideoPlanProposal
	if err := json.Unmarshal(acceptedBytes, &accepted); err != nil || accepted.CompositionCatalog == nil || len(accepted.Parts) != 1 || accepted.Parts[0].Composition == nil {
		t.Fatalf("working revision metadata lost spatial plan: value=%#v err=%v", working.Timeline.Metadata["accepted_video_plan"], err)
	}

	inspectArgs, _ := json.Marshal(map[string]any{"action": "inspect_composition", "project_id": create.ProjectID, "proposal_id": response.Proposal.ID, "expected_revision_id": response.Proposal.WorkingRevisionID})
	inspected, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "inspect", Name: "manage_video", Arguments: string(inspectArgs)})
	if err != nil || !strings.Contains(inspected, source.Ref) || !strings.Contains(inspected, `"width":1920`) || !strings.Contains(inspected, `"height":1080`) {
		t.Fatalf("inspect_composition did not expose the preserved source and resolved geometry: payload=%s err=%v", inspected, err)
	}

	// Provider-authored composition updates carry exact visual references but not
	// the server-resolved visual_media_type. UpdateComposition must hydrate that
	// internal field just as initial proposal creation does.
	updatedPlan := *response.Proposal.Plan
	updatedPlan.Parts = append([]pebblestore.VideoPlanPart(nil), response.Proposal.Plan.Parts...)
	updatedPlan.Parts[0].VisualMediaType = ""
	updateArgs, err := json.Marshal(map[string]any{
		"action":               "update_composition",
		"project_id":           create.ProjectID,
		"proposal_id":          response.Proposal.ID,
		"expected_revision_id": response.Proposal.WorkingRevisionID,
		"plan":                 updatedPlan,
	})
	if err != nil {
		t.Fatal(err)
	}
	updatedPayload, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "update", Name: "manage_video", Arguments: string(updateArgs)})
	if err != nil || !strings.Contains(updatedPayload, `"visual_media_type":"image/png"`) {
		t.Fatalf("update_composition did not rehydrate the exact ready fallback media type: payload=%s err=%v", updatedPayload, err)
	}
}

func TestParseVideoPlanProposalRejectsUnknownSpatialFields(t *testing.T) {
	_, err := parseVideoPlanProposal(map[string]any{
		"kind":                "initial",
		"composition_catalog": map[string]any{"schema_version": 1, "layouts": []any{}},
		"parts": []map[string]any{{
			"id": "part-1", "title": "Part", "duration_ms": 1000,
			"visual":      map[string]any{"session_id": "session", "collection_id": "collection", "variant_id": "variant", "event_seq": 1},
			"composition": map[string]any{"layout_id": "layout", "unknown_spatial_field": true},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown spatial plan fields must fail closed, got %v", err)
	}
}
