package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

func TestDeployContainerPackageSuggestUsesAccountScopedWorkspace(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "package.json"), []byte(`{"scripts":{"test":"true"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	if _, err := workspaceStore.SaveForAccount(testAccountScopeID, workspaceDir, "workspace", "", true); err != nil {
		t.Fatalf("save account workspace: %v", err)
	}

	server := &Server{workspace: workspace.NewService(workspaceStore)}
	body := strings.NewReader(`{"workspace_paths":[` + strconvQuote(workspaceDir) + `]}`)
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/deploy/container/package/suggest", body))
	rec := httptest.NewRecorder()

	server.handleDeployContainerPackageSuggest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Packages []deployContainerPackageSuggestion `json:"packages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !hasSuggestedPackage(resp.Packages, "nodejs") || !hasSuggestedPackage(resp.Packages, "npm") {
		t.Fatalf("suggestions = %+v, want nodejs and npm", resp.Packages)
	}
}

func TestDeployContainerPackageSuggestRejectsMissingPrincipal(t *testing.T) {
	server := &Server{workspace: workspace.NewService(nil)}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/package/suggest", strings.NewReader(`{"workspace_paths":["/tmp/example"]}`))
	rec := httptest.NewRecorder()

	server.handleDeployContainerPackageSuggest(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func hasSuggestedPackage(packages []deployContainerPackageSuggestion, name string) bool {
	for _, pkg := range packages {
		if pkg.Name == name {
			return true
		}
	}
	return false
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
