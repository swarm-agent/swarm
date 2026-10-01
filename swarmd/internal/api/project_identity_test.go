package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func projectIdentityPNG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func projectIdentityRequest(t *testing.T, s *Server, p identity.Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, ProjectsPath+path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{
		AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"projects:read", "projects:write"},
	})
	w := httptest.NewRecorder()
	s.handleProjects(w, r.WithContext(ctx))
	return w
}

// Purpose: handleProjects must expose icon create/read/list/patch semantics without
// accidental clearing or cross-account writes. HTTP-to-store assertions prove the
// request contract at its narrowest boundary, including rejected payload postconditions.
func TestProjectIconHTTPContract(t *testing.T) {
	s, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	p := testPrincipal()
	icon := projectIdentityPNG(t)
	w := projectIdentityRequest(t, s, p, http.MethodPost, "", map[string]any{"name": "Icon", "icon_png_data_url": icon})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Project pebblestore.ProjectRecord `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Project.ID
	if id == "" || response.Project.IconPNGDataURL != icon {
		t.Fatal("create response lost icon")
	}
	for _, body := range []map[string]any{{"name": "Renamed"}, {"icon_png_data_url": icon}} {
		w = projectIdentityRequest(t, s, p, http.MethodPatch, "/"+id, body)
		if w.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", w.Code, w.Body.String())
		}
		got, found, err := s.sessions.Store().GetProject(p.AccountScopeID, id)
		if err != nil || !found || got.IconPNGDataURL != icon {
			t.Fatalf("patch lost icon: %+v %v", got, err)
		}
	}
	for _, value := range []any{nil, 5, "https://example.invalid/image.png", "data:image/png;base64,broken"} {
		w = projectIdentityRequest(t, s, p, http.MethodPatch, "/"+id, map[string]any{"icon_png_data_url": value, "name": "Must not persist"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch: %d", w.Code)
		}
		w = projectIdentityRequest(t, s, p, http.MethodPost, "", map[string]any{"name": "Invalid", "icon_png_data_url": value})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid create: %d", w.Code)
		}
	}
	foreign := p
	foreign.AccountScopeID = "foreign-account"
	w = projectIdentityRequest(t, s, foreign, http.MethodPatch, "/"+id, map[string]any{"icon_png_data_url": ""})
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign patch: %d", w.Code)
	}
	w = projectIdentityRequest(t, s, p, http.MethodGet, "/"+id, nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Project.IconPNGDataURL != icon || response.Project.Name != "Renamed" {
		t.Fatalf("read/postconditions: %s", w.Body.String())
	}
	w = projectIdentityRequest(t, s, p, http.MethodGet, "", nil)
	var list struct {
		Projects []pebblestore.ProjectRecord `json:"projects"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Projects) != 1 || list.Projects[0].IconPNGDataURL != icon {
		t.Fatalf("list: %s", w.Body.String())
	}
	w = projectIdentityRequest(t, s, p, http.MethodPatch, "/"+id, map[string]any{"icon_png_data_url": ""})
	got, found, err := s.sessions.Store().GetProject(p.AccountScopeID, id)
	if w.Code != http.StatusOK || err != nil || !found || got.IconPNGDataURL != "" {
		t.Fatalf("clear: %d %+v %v", w.Code, got, err)
	}
}
