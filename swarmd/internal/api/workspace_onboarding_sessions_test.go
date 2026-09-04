package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: provider-configured users can start exactly one durable,
// pre-admission assistant session for an unsaved non-repository folder with
// existing files. Threat: reusing ordinary workspace routing would create a
// binding/worktree before HEAD or leak broad Swarm capabilities. This endpoint
// test is the narrowest layer proving validation, model selection, V3 mutation,
// scope metadata, and no workspace admission.
func TestWorkspaceOnboardingSessionCreatesPreAdmissionV3Authority(t *testing.T) {
	server, sessions, principal := newRoutedSessionAtomicityServer(t, &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"unused"}`}}, false, false)
	server.v3SessionExecutor = nil
	folder := filepath.Join(t.TempDir(), "existing-folder")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "README.md"), []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{
		"path": folder, "expected_resolved_path": folder, "client_request_id": "onboarding-create",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded workspaceOnboardingSessionStartResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.OK || decoded.SessionID == "" || decoded.Repository.State != "needs_assisted_setup" || decoded.FirstMessage.ID == "" || decoded.Projection.LastEventSeq != 1 {
		t.Fatalf("response=%+v", decoded)
	}
	stored, ok, err := sessions.GetSession(decoded.SessionID)
	if err != nil || !ok {
		t.Fatalf("stored=%+v ok=%t err=%v", stored, ok, err)
	}
	if stored.WorktreeEnabled || len(stored.WorkspaceGrants) != 0 || len(stored.TemporaryWorkspaceRoots) != 0 || stored.WorkspacePath != folder || stored.Metadata["workspace_onboarding"] != true || stored.Metadata["pre_admission"] != true || stored.Metadata["agent_name"] != agentruntime.WorkspaceOnboardingAgentID || stored.Metadata["workspace_binding_id"] != nil {
		t.Fatalf("pre-admission session=%+v metadata=%+v", stored, stored.Metadata)
	}
	if stored.ModelProfile == nil || stored.ModelProfile.Source != pebblestore.SessionModelProfileSourceSwarmSettings || !stored.ModelProfile.UseAccountDefault || stored.ModelProfile.Action.Model != "action-model" || stored.Preference.Model != "action-model" {
		t.Fatalf("Action model snapshot=%+v preference=%+v", stored.ModelProfile, stored.Preference)
	}
	entries, err := server.workspace.ListKnownForPrincipal(principal, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace was prematurely admitted: entries=%+v err=%v", entries, err)
	}
	replay := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{"path": folder, "expected_resolved_path": folder, "client_request_id": "onboarding-create"})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayed workspaceOnboardingSessionStartResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil || !replayed.Replayed || replayed.SessionID != decoded.SessionID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	if err := os.Rename(folder, folder+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(folder+"-moved", folder); err != nil {
		t.Fatal(err)
	}
	staleReplay := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{"path": folder, "expected_resolved_path": folder, "client_request_id": "onboarding-create"})
	if staleReplay.Code == http.StatusOK {
		t.Fatalf("stale replay returned success: %s", staleReplay.Body.String())
	}
}

// Requirement: every rejected onboarding start leaves no V3 or workspace
// authority. Threat: stale, symlinked, cross-account, empty, ready, saved, or
// provider-unavailable inputs could widen pre-admission filesystem access.
func TestWorkspaceOnboardingSessionRejectsInvalidAuthorityWithoutPartialState(t *testing.T) {
	newServer := func(t *testing.T) (*Server, identity.Principal) {
		t.Helper()
		server, _, principal := newRoutedSessionAtomicityServer(t, &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"unused"}`}}, false, false)
		server.v3SessionExecutor = nil
		return server, principal
	}
	makeExisting := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "folder")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("denied principal", func(t *testing.T) {
		server, _ := newServer(t)
		path := makeExisting(t)
		response := postWorkspaceOnboardingRequest(t, server, identity.Principal{}, map[string]any{"path": path, "expected_resolved_path": path, "client_request_id": "unauth"})
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("capability escalation fields", func(t *testing.T) {
		server, principal := newServer(t)
		path := makeExisting(t)
		response := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{
			"path": path, "expected_resolved_path": path, "client_request_id": "escalation",
			"agent_name": "swarm", "workspace_binding_id": "spoofed", "metadata": map[string]any{"task": true},
		})
		if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "unknown field") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if sessions, err := server.sessions.ListSessionsForAccount(principal.AccountScopeID, 10); err != nil || len(sessions) != 0 {
			t.Fatalf("escalation published sessions=%+v err=%v", sessions, err)
		}
	})
	t.Run("stale expected path", func(t *testing.T) {
		server, principal := newServer(t)
		path := makeExisting(t)
		assertWorkspaceOnboardingRejectedClean(t, server, principal, path, filepath.Join(filepath.Dir(path), "other"), "stale")
	})
	t.Run("symlink", func(t *testing.T) {
		server, principal := newServer(t)
		target := makeExisting(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		assertWorkspaceOnboardingRejectedClean(t, server, principal, link, target, "symlink")
	})
	t.Run("cross account saved folder", func(t *testing.T) {
		server, principal := newServer(t)
		path := t.TempDir()
		foreign := principal
		foreign.AccountScopeID = "foreign-account"
		if _, err := server.workspace.SetupRepositoryForPrincipal(foreign, path, path); err != nil {
			t.Fatal(err)
		}
		if _, err := server.workspace.CreateCatalogEntryForPrincipal(foreign, path, "Foreign", ""); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(path, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A foreign account record does not authorize this principal and must not
		// be exposed as a reference. The principal gets an independent, unsaved
		// pre-admission session only; the foreign catalog remains unchanged.
		response := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{"path": path, "expected_resolved_path": path, "client_request_id": "cross-account"})
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		foreignEntries, _ := server.workspace.ListKnownForPrincipal(foreign, 10)
		principalEntries, _ := server.workspace.ListKnownForPrincipal(principal, 10)
		if len(foreignEntries) != 1 || len(principalEntries) != 0 {
			t.Fatalf("catalog scopes changed: foreign=%+v principal=%+v", foreignEntries, principalEntries)
		}
	})
	t.Run("empty folder", func(t *testing.T) {
		server, principal := newServer(t)
		path := t.TempDir()
		assertWorkspaceOnboardingRejectedClean(t, server, principal, path, path, "empty")
	})
	t.Run("saved folder", func(t *testing.T) {
		server, principal := newServer(t)
		path := t.TempDir()
		if _, err := server.workspace.SetupRepositoryForPrincipal(principal, path, path); err != nil {
			t.Fatal(err)
		}
		if _, err := server.workspace.CreateCatalogEntryForPrincipal(principal, path, "Saved", ""); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(path, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		response := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{"path": path, "expected_resolved_path": path, "client_request_id": "saved"})
		if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "already saved") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("provider unavailable", func(t *testing.T) {
		server, principal := newServer(t)
		path := makeExisting(t)
		server.providers = nil
		assertWorkspaceOnboardingRejectedClean(t, server, principal, path, path, "provider")
	})
}

func assertWorkspaceOnboardingRejectedClean(t *testing.T, server *Server, principal identity.Principal, path, expected, requestID string) {
	t.Helper()
	before, _ := server.sessions.ListSessionsForAccount(principal.AccountScopeID, 100)
	response := postWorkspaceOnboardingRequest(t, server, principal, map[string]any{"path": path, "expected_resolved_path": expected, "client_request_id": requestID})
	if response.Code == http.StatusOK {
		t.Fatalf("rejected start returned success: %s", response.Body.String())
	}
	after, err := server.sessions.ListSessionsForAccount(principal.AccountScopeID, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("failed start published session authority: before=%d after=%d err=%v", len(before), len(after), err)
	}
	entries, err := server.workspace.ListKnownForPrincipal(principal, 100)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed start published workspace authority: entries=%+v err=%v", entries, err)
	}
}

func postWorkspaceOnboardingRequest(t *testing.T, server *Server, principal identity.Principal, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, WorkspaceOnboardingSessionsPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if principal.Valid() {
		request = request.WithContext(identity.ContextWithPrincipal(context.Background(), principal))
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

