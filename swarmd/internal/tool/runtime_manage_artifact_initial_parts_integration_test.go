package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExecuteManageArtifactCreatesReadyAuthoritativeInitialCompositionAndResolvesPart(t *testing.T) {
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
	createdSession, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "real-parts-source", UserID: "user-1", AccountScopeID: "account-1", Title: "Real parts",
		WorkspacePath: workspace, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test-model", Thinking: "high"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	authority := artifact.NewAuthority(artifact.NewRegistry(sessions, artifact.Limits{}), sessions)
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	scope := WorkspaceScope{PrimaryPath: workspace, SessionID: createdSession.ID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: createdSession.UserID, AccountScopeID: createdSession.AccountScopeID, SessionID: createdSession.ID}}
	ctx := WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: createdSession.ID, RunID: "run-parts"})

	output, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: "create-real-parts", Name: "manage_artifact", Arguments: `{"action":"create","collection_name":"Composed","filename":"composed.txt","media_type":"text/plain","initial_parts":[{"id":"hero","label":"Hero","description":"Hero bytes","media_type":"text/plain","content":"hero","locator":{"kind":"semantic"}},{"id":"footer","label":"Footer","media_type":"text/plain","content":"footer"}]}`})
	if err != nil {
		t.Fatalf("execute manage_artifact initial parts: %v", err)
	}
	var response struct {
		Artifact  pebblestore.SessionArtifactVariant            `json:"artifact"`
		Reference pebblestore.SessionArtifactSelectionReference `json:"reference"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	variant := response.Artifact
	if variant.Status != pebblestore.SessionArtifactStatusReady || variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || variant.Composition == nil || len(variant.Composition.Parts) != 2 || len(variant.PartDefinitions) != 2 {
		t.Fatalf("authoritative ready response = %#v", response)
	}
	if variant.DigestSHA256 == "" || variant.Size <= 0 || response.Reference.EventSeq == 0 {
		t.Fatalf("ready exact reference or deterministic projection metadata missing: %#v", response)
	}
	wantBodies := map[string]string{"hero": "hero", "footer": "footer"}
	for _, slot := range variant.Composition.Parts {
		want := wantBodies[slot.PartID]
		digest := sha256.Sum256([]byte(want))
		if slot.Revision.DigestSHA256 != hex.EncodeToString(digest[:]) || slot.Revision.Size != int64(len(want)) || slot.Revision.MediaType != "text/plain" {
			t.Fatalf("exact part revision %q = %#v", slot.PartID, slot.Revision)
		}
		body, _, err := authority.ReadPartRevision(context.Background(), artifact.Principal{SessionID: createdSession.ID, AccountScopeID: createdSession.AccountScopeID, UserID: createdSession.UserID}, slot.Revision, 64)
		if err != nil || string(body) != want {
			t.Fatalf("read independent part %q = %q err=%v", slot.PartID, body, err)
		}
	}
	if variant.ArtifactChainID != variant.Composition.ArtifactChainID || variant.Composition.OwnerSessionID != variant.SessionID {
		t.Fatalf("returned composition identity is inconsistent: variant=%#v composition=%#v", variant, variant.Composition)
	}
	principal := artifact.Principal{SessionID: createdSession.ID, AccountScopeID: createdSession.AccountScopeID, UserID: createdSession.UserID}
	resolved, composition, definition, revision, err := authority.ResolvePartTarget(principal, response.Reference, "hero")
	if err != nil || resolved.ID != variant.ID || composition.ID != variant.Composition.ID || definition.ID != "hero" || revision != variant.Composition.Parts[0].Revision {
		t.Fatalf("resolve focused part = variant:%#v composition:%#v definition:%#v revision:%#v err=%v", resolved, composition, definition, revision, err)
	}
	replacement, err := authority.PublishPartReplacement(context.Background(), principal, artifact.PublishPartReplacementInput{
		RequestID: "replace-hero", CollectionID: response.Reference.CollectionID, VariantID: "hero-candidate", ArtifactStepID: "replace-hero-step", CandidateIndex: 1, AutoAccept: true,
		SourceArtifact: response.Reference, SourceComposition: composition, PartDefinition: definition, SourcePartRevision: revision,
		Filename: "composed.txt", MediaType: "text/plain", Body: []byte("new hero"),
	})
	if err != nil {
		t.Fatalf("publish replacement from created composition: %v", err)
	}
	if replacement.Status != pebblestore.SessionArtifactStatusReady || replacement.Composition == nil || len(replacement.Composition.Parts) != len(composition.Parts) || replacement.Composition.Parts[0].Revision == revision || replacement.Composition.Parts[1] != composition.Parts[1] {
		t.Fatalf("replacement did not preserve untouched exact references: source=%#v replacement=%#v", composition, replacement)
	}
	if len(variant.Parts) != 0 {
		t.Fatalf("locator-only parts masqueraded as authoritative parts: %#v", variant.Parts)
	}
}
