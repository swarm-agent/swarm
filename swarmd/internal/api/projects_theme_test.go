package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// Requirement: project theme selections reference the authenticated account's
// existing builtin/custom catalog. Threat: foreign/unknown IDs or malformed
// PATCHes must not persist metadata or publish a project mutation. The HTTP
// handler and Pebble project store are the narrowest layer proving this path.
func TestProjectsThemeSelectionAccountBoundary(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := pebblestore.NewSessionStore(db)
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	themes := uisettings.NewService(pebblestore.NewUISettingsStore(db))
	s := &Server{sessions: sessionruntime.NewService(ss, events)}
	s.SetUISettingsService(themes)
	settings, err := themes.GetForAccount("owner")
	if err != nil {
		t.Fatal(err)
	}
	settings.Theme.CustomThemes = []uisettings.ThemeCustomTheme{{ID: "owner-palette", Name: "Owner", Palette: settings.Theme.BuiltinThemes[0].Palette}}
	if _, err := themes.SetForAccount("owner", settings); err != nil {
		t.Fatal(err)
	}
	foreign, err := themes.GetForAccount("foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreign.Theme.CustomThemes = []uisettings.ThemeCustomTheme{{ID: "foreign-palette", Name: "Foreign", Palette: foreign.Theme.BuiltinThemes[0].Palette}}
	if _, err := themes.SetForAccount("foreign", foreign); err != nil {
		t.Fatal(err)
	}
	call := func(account, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, ProjectsPath+path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "user", AccountScopeID: account}))
		w := httptest.NewRecorder()
		s.apiMux().ServeHTTP(w, req)
		return w
	}
	for _, value := range []string{`"foreign-palette"`, `"missing"`, `null`, `42`} {
		w := call("owner", http.MethodPost, "", `{"name":"Rejected","theme_id":`+value+`}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("create %s: %d %s", value, w.Code, w.Body.String())
		}
	}
	projects, err := ss.ListProjects("owner", 10)
	if err != nil || len(projects) != 0 {
		t.Fatalf("rejected create persisted project: %v %v", projects, err)
	}
	w := call("owner", http.MethodPost, "", `{"name":"Kept","theme_id":"owner-palette","description":"metadata"}`)
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
	if response.Project.ThemeID != "owner-palette" {
		t.Fatalf("created theme: %+v", response.Project)
	}
	for _, value := range []string{`"foreign-palette"`, `"missing"`, `null`, `42`} {
		w = call("owner", http.MethodPatch, "/"+id, `{"name":"Tampered","theme_id":`+value+`}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("patch %s: %d %s", value, w.Code, w.Body.String())
		}
		stored, found, err := ss.GetProject("owner", id)
		if err != nil || !found || stored.Name != "Kept" || stored.ThemeID != "owner-palette" {
			t.Fatalf("rejected patch mutated project: %+v %v", stored, err)
		}
	}
	w = call("foreign", http.MethodPatch, "/"+id, `{"theme_id":"foreign-palette"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign project patch: %d", w.Code)
	}
	w = call("owner", http.MethodPatch, "/"+id, `{"theme_id":"","description":"preserved"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	stored, _, err := ss.GetProject("owner", id)
	if err != nil || stored.ThemeID != "" || stored.Description != "preserved" {
		t.Fatalf("clear/preserve: %+v %v", stored, err)
	}
	w = call("owner", http.MethodPatch, "/"+id, `{"theme_id":"nord"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("builtin: %d %s", w.Code, w.Body.String())
	}
	stored, _, err = ss.GetProject("owner", id)
	if err != nil || stored.ThemeID != "nord" {
		t.Fatalf("builtin persisted: %+v %v", stored, err)
	}
}
