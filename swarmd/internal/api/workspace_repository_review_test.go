package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: authenticated review/baseline preparation is provider-free and
// separate from saving/hydration. Threat: unauthorized/stale consent or lost
// responses could commit content or duplicate catalog identity. Real handlers
// and temporary stores prove errors plus filesystem/catalog postconditions.
func TestWorkspaceRepositoryReviewBaselineAndSaveResume(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "project.txt"), []byte("project"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method  string
		handler http.HandlerFunc
	}{{http.MethodGet, server.handleWorkspaceRepositoryReview}, {http.MethodPost, server.handleWorkspaceRepositoryBaseline}} {
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(tc.method, "/", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected handler: %d", rec.Code)
		}
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request wrote metadata")
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	get := func() workspace.RepositoryReview {
		t.Helper()
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v1/workspace/repository/review?path="+url.QueryEscape(path), nil), "workspace-user", "workspace-account")
		rec := httptest.NewRecorder()
		server.handleWorkspaceRepositoryReview(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("review=%d %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Review workspace.RepositoryReview `json:"review"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Review
	}
	review := get()
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	reqBody := workspace.RepositoryBaselineRequest{Path: path, ExpectedResolvedPath: path, ReviewDigest: review.Digest, SelectedPaths: []string{"project.txt"}, ConfirmBaseline: true}
	post := func(body any, account string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/repository/baseline", bytes.NewReader(data)), "workspace-user", account)
		rec := httptest.NewRecorder()
		server.handleWorkspaceRepositoryBaseline(rec, req)
		return rec
	}
	if rec := post(reqBody, "foreign-account"); rec.Code == http.StatusOK {
		t.Fatal("foreign consent accepted")
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Fatal("foreign rejection mutated project")
	}
	var head string
	for i := 0; i < 2; i++ {
		rec := post(reqBody, "workspace-account")
		if rec.Code != http.StatusOK {
			t.Fatalf("baseline=%d %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Repository workspace.RepositoryState `json:"repository"`
		}
		json.Unmarshal(rec.Body.Bytes(), &payload)
		if i > 0 && head != payload.Repository.HeadCommit {
			t.Fatal("retry changed HEAD")
		}
		head = payload.Repository.HeadCommit
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	// Saving requires explicit acknowledgement when existing index/uncommitted
	// data differ from HEAD; repeated acknowledgement preserves the catalog ID.
	var result map[string]any
	if rec := postWorkspaceAdd(t, server, path, "project", &result); rec.Code != http.StatusConflict || result["code"] != "workspace_content_review_required" {
		t.Fatalf("silent omission %d %#v", rec.Code, result)
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	id := ""
	for i := 0; i < 2; i++ {
		data, _ := json.Marshal(map[string]any{"path": path, "name": "project", "make_current": false, "confirm_committed_only": true})
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/add", bytes.NewReader(data)), "workspace-user", "workspace-account")
		rec := httptest.NewRecorder()
		server.handleWorkspaceAdd(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save=%d %s", rec.Code, rec.Body.String())
		}
		json.Unmarshal(rec.Body.Bytes(), &result)
		current, _ := result["workspace_id"].(string)
		if current == "" || i > 0 && id != current {
			t.Fatal("retry duplicated identity")
		}
		id = current
	}
	entries, err := server.workspace.ListKnownForPrincipal(workspaceAddSelfBindingPrincipal(), 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog=%+v %v", entries, err)
	}
	hydrated, err := server.workspace.ResolveForPrincipal(workspaceAddSelfBindingPrincipal(), path)
	if err != nil || hydrated.WorkspaceID != id {
		t.Fatalf("hydrate=%+v %v", hydrated, err)
	}
	review = get()
	if review.Repository.HeadCommit != head {
		t.Fatal("save/hydration changed repository")
	}
}

// Requirement: route registration must retain the product-identity middleware.
// Exercise DesktopHandler rather than trusting direct handler guards; rejected
// requests must neither initialize Git nor bootstrap product/catalog authority.
func TestWorkspaceRepositoryRoutesRequireProductIdentity(t *testing.T) {
	server, store, ids := newProtectedIdentityGuardTestServer(t, false)
	path := t.TempDir()
	for _, tc := range []struct{ method, route string }{{http.MethodGet, "/v1/workspace/repository?path=" + url.QueryEscape(path)}, {http.MethodGet, "/v1/workspace/repository/review?path=" + url.QueryEscape(path)}, {http.MethodPost, "/v1/workspace/repository/baseline"}} {
		rec := httptest.NewRecorder()
		server.DesktopHandler().ServeHTTP(rec, newProtectedJSONRequest(t, tc.method, tc.route, map[string]any{"path": path, "expected_resolved_path": path, "confirm_baseline": true}, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("route=%s code=%d", tc.route, rec.Code)
		}
	}
	counts, err := ids.IdentityCounts()
	if err != nil || counts != (pebblestore.IdentityCounts{}) {
		t.Fatalf("identity mutation %+v %v", counts, err)
	}
	entries, err := pebblestore.NewWorkspaceStore(store).ListForAccount("workspace-account", 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("catalog mutation %+v %v", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Fatal("unauthenticated route mutated Git")
	}
}
