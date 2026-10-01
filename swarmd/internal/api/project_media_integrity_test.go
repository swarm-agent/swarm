package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: handleProjects must never append the original media reference when
// source conversion or PutSessionMediaAsset retention fails. Handler/store tests
// are the narrowest layer proving error responses and unchanged durable shelves,
// while successful image/document retention remains readable and account-scoped.
func TestProjectMediaUploadIntegrity(t *testing.T) {
	s, _, _ := newWorkspaceOverviewTopologyTestServer(t)
	p := testPrincipal()
	project := &pebblestore.ProjectRecord{Name: "Media"}
	if err := s.sessions.Store().PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	path := "/" + project.ID + "/media"
	for _, item := range []pebblestore.ProjectTaskMediaRef{
		{URL: "data:image/png;base64,!!!!"},
		{URL: "data:image/png;base64,"},
		{URL: "data:image/png;base64,aGVsbG8="}, // decodes, but retention rejects MIME mismatch
		{ID: "stg_missing"},
		{URL: "/v3/media-staging/stg_missing"},
	} {
		w := projectIdentityRequest(t, s, p, http.MethodPost, path, item)
		if w.Code < 400 {
			t.Fatalf("failed conversion reported success: %d %s", w.Code, w.Body.String())
		}
		got, found, err := s.sessions.Store().GetProject(p.AccountScopeID, project.ID)
		if err != nil || !found || len(got.UploadedMedia) != 0 {
			t.Fatalf("failed upload appended media: %+v %v", got, err)
		}
	}
	for _, item := range []pebblestore.ProjectTaskMediaRef{
		{URL: projectIdentityPNG(t), Filename: "icon.png"},
		{Data: "Plain text document", Kind: "doc", MediaType: "text/plain", Filename: "notes.txt"},
		{URL: "data:text/plain;base64,ZW5jb2RlZCBkb2N1bWVudA==", Filename: "encoded.txt"},
	} {
		w := projectIdentityRequest(t, s, p, http.MethodPost, path, item)
		var response struct {
			Media pebblestore.ProjectTaskMediaRef `json:"media"`
		}
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("upload: %d %s", w.Code, w.Body.String())
		}
		media := response.Media
		if media.Data != "" || media.DigestSHA256 == "" || media.SizeBytes == 0 || !strings.HasPrefix(media.URL, "/v3/sessions/"+project.ID+"/media/") {
			t.Fatalf("not retained: %+v", media)
		}
		_, payload, err := s.sessions.ReadSessionMediaAsset(p.AccountScopeID, project.ID, media.ID)
		if err != nil || len(payload) != int(media.SizeBytes) {
			t.Fatalf("retained asset unreadable: %v", err)
		}
		if _, _, err := s.sessions.ReadSessionMediaAsset("foreign-account", project.ID, media.ID); err == nil {
			t.Fatal("foreign account read retained bytes")
		}
		// Existing durable references are reusable without reconversion.
		w = projectIdentityRequest(t, s, p, http.MethodPost, path, media)
		if w.Code != http.StatusCreated {
			t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
		}
	}
	got, found, err := s.sessions.Store().GetProject(p.AccountScopeID, project.ID)
	if err != nil || !found || len(got.UploadedMedia) != 6 {
		t.Fatalf("durable shelf: %+v %v", got, err)
	}
}
