package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBackendArtifactCatalogRequiresAuthenticationAndHidesManagedStorage(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newArtifactSessionFixture(t, "workspace.txt", "workspace")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "api-contract-create", CollectionID: "api-contract-collection", CollectionName: "Alternatives",
		VariantID: "api-contract-variant", Filename: "design.txt", MediaType: "text/plain", Role: pebblestore.SessionArtifactRoleRenderOnly,
		Presentation: pebblestore.SessionArtifactPresentation{Kind: "text", Label: "Selected design", Previewable: true}, Body: []byte("private bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := httptest.NewRecorder()
	server.handleSessionsV3Artifacts(unauthenticated, httptest.NewRequest(http.MethodGet, "/v3/artifacts", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated catalog status = %d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	authenticated := httptest.NewRecorder()
	server.handleSessionsV3Artifacts(authenticated, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/artifacts?limit=25", nil)))
	if authenticated.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", authenticated.Code, authenticated.Body.String())
	}
	var catalog struct {
		Artifacts []sessionsV3ArtifactCatalogItem `json:"artifacts"`
	}
	if err := json.Unmarshal(authenticated.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range catalog.Artifacts {
		if item.ArtifactID == variant.ID {
			found = true
			if item.SessionID != variant.SessionID || item.CollectionID != variant.CollectionID || item.EventSeq != variant.EventSeq || item.Status != pebblestore.SessionArtifactStatusReady || item.Role != pebblestore.SessionArtifactRoleRenderOnly {
				t.Fatalf("managed catalog item = %#v", item)
			}
		}
	}
	if !found {
		t.Fatalf("managed artifact missing from catalog: %#v", catalog.Artifacts)
	}
	for _, forbidden := range []string{"private bytes", "private-data", "storage_path", "digest_sha256"} {
		if strings.Contains(authenticated.Body.String(), forbidden) {
			t.Fatalf("catalog leaked %q: %s", forbidden, authenticated.Body.String())
		}
	}
}

func TestBackendArtifactSelectionEndpointEnforcesOwnershipAndExactReadyEvent(t *testing.T) {
	server, sessionSvc, registry, _, _, _, _ := newArtifactSessionFixture(t, "workspace.txt", "workspace")
	principal := testPrincipal()
	authority := artifact.NewAuthority(registry, sessionSvc)
	variant, err := authority.Create(context.Background(), artifact.Principal{SessionID: "artifact-session", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, artifact.CreateInput{
		RequestID: "selection-contract-create", CollectionID: "selection-contract-collection", CollectionName: "Alternatives",
		VariantID: "selection-contract-variant", Filename: "design.txt", MediaType: "text/plain", Body: []byte("ready"),
	})
	if err != nil {
		t.Fatal(err)
	}
	post := func(requestID string, eventSeq uint64, principal identity.Principal) *httptest.ResponseRecorder {
		t.Helper()
		body := bytes.NewBufferString(`{"client_request_id":"` + requestID + `","event_seq":` + artifactContractJSONNumber(eventSeq) + `,"action":"select","artifact_chain_id":"` + variant.ArtifactChainID + `","artifact_step_id":"` + variant.ArtifactStepID + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+variant.SessionID+"/artifacts/"+variant.ID+"/selection", body)
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, req)
		return recorder
	}
	if stale := post("selection-stale", variant.EventSeq+1, principal); stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "event sequence") {
		t.Fatalf("stale selection status=%d body=%s", stale.Code, stale.Body.String())
	}
	wrong := principal
	wrong.AccountScopeID = "other-account"
	if foreign := post("selection-foreign", variant.EventSeq, wrong); foreign.Code == http.StatusOK {
		t.Fatalf("cross-account selection succeeded: %s", foreign.Body.String())
	}
	selected := post("selection-valid", variant.EventSeq, principal)
	if selected.Code != http.StatusOK || !strings.Contains(selected.Body.String(), `"action":"select"`) {
		t.Fatalf("valid selection status=%d body=%s", selected.Code, selected.Body.String())
	}
}

func TestBackendWorkspacePlanArtifactIsDirectlyViewableWithoutManagedImport(t *testing.T) {
	server, sessionSvc, _, plan, checkpoint, _, descriptor := newArtifactSessionFixture(t, "workspace-note.txt", "workspace handoff")
	principal := testPrincipal()
	doc := &pebblestore.SessionPlanDocument{ID: plan.ID, Title: "Workspace handoff", Checkpoints: []pebblestore.SessionPlanCheckpoint{checkpoint}}
	if _, _, err := sessionSvc.SavePlanWithMetadata(plan.SessionID, plan.ID, doc.Title, "", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: doc}); err != nil {
		t.Fatal(err)
	}

	resolved, found, err := server.resolveSessionV3Artifact(context.Background(), principal, plan.SessionID, descriptor.ID)
	if err != nil || !found || resolved.Managed != nil || resolved.Descriptor.ID != descriptor.ID {
		t.Fatalf("workspace resolve = found=%t resolved=%#v err=%v", found, resolved, err)
	}
	file, _, err := server.openSessionV3Artifact(context.Background(), mustSession(t, server, plan.SessionID), resolved)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(content) != "workspace handoff" {
		t.Fatalf("workspace handoff bytes = %q read=%v close=%v", content, readErr, closeErr)
	}
	collections, err := sessionSvc.ListAllSessionArtifactCollections(principal.AccountScopeID, plan.SessionID, "")
	if err != nil || len(collections) != 0 {
		t.Fatalf("workspace handoff read created managed collections: count=%d err=%v", len(collections), err)
	}
}

func TestBackendArtifactSessionDeletionCleansPrivateBytes(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("STATE_DIRECTORY", filepath.Join(workspace, "state"))
	_, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID: "cleanup-api-contract", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID,
		Title: "Cleanup", WorkspacePath: workspace, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := artifact.NewRegistry(sessionSvc, artifact.Limits{})
	authority := artifact.NewAuthority(registry, sessionSvc)
	_, err = authority.Create(context.Background(), artifact.Principal{SessionID: created.ID, AccountScopeID: created.AccountScopeID, UserID: created.UserID}, artifact.CreateInput{
		RequestID: "cleanup-create", CollectionID: "cleanup-collection", CollectionName: "Cleanup", VariantID: "cleanup-variant", Filename: "cleanup.txt", MediaType: "text/plain", Body: []byte("delete me"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionSvc.DeleteSession(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OwnedSession(created.ID, created.AccountScopeID, created.UserID); err == nil {
		t.Fatal("deleted session retained authenticated artifact-byte access")
	}
	if report, err := registry.RunMaintenance(10); err != nil || report.DeletedSessions != 1 {
		t.Fatalf("post-delete maintenance = %+v err=%v", report, err)
	}
}

func artifactContractJSONNumber(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
