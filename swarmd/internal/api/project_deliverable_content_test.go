package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: task collection refresh must not transfer inline media, but exact
// deliverables remain readable under the same account/scope boundary. The real
// handler and temporary Pebble store prove byte preservation, bounded wire size,
// stale-reference rejection, cross-account isolation and read-only behavior.
// This deterministic regression is not a live generation/latency benchmark.
func TestProjectDeliverableContentLazyAccountBound(t *testing.T) {
	server, store, p := setupDirectMediaTestServer(t)
	project := &pebblestore.ProjectRecord{ID: "media-project", AccountID: p.AccountScopeID, Name: "Media"}
	if err := store.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, 1024*1024)...)
	inline := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	task := &pebblestore.ProjectTaskRecord{ID: "media-task", ProjectID: project.ID, AccountID: p.AccountScopeID, Title: "Image", Agent: "image", Status: "needs_review", Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "image", Kind: "image", Status: "ready", Thumbnail: inline, MediaURL: inline}}}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	request := func(principal identity.Principal, path, method, scope string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, principal))
		req = req.WithContext(context.WithValue(req.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Scopes: []string{scope}}))
		w := httptest.NewRecorder()
		server.handleProjects(w, req)
		return w
	}
	path := "/v3/projects/" + project.ID + "/tasks"
	list := request(p, path, http.MethodGet, "projects:read")
	if list.Code != 200 || list.Body.Len() > 16000 || strings.Contains(list.Body.String(), "data:image") {
		t.Fatalf("collection status=%d bytes=%d", list.Code, list.Body.Len())
	}
	var result struct {
		Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 {
		t.Fatal("task omitted")
	}
	d := result.Tasks[0].Deliverables[0]
	if d.MediaURL == inline || d.Thumbnail != d.MediaURL {
		t.Fatal("inline bytes not replaced by shared exact reference")
	}
	content := request(p, d.MediaURL, http.MethodGet, "projects:read")
	if content.Code != 200 || !bytes.Equal(content.Body.Bytes(), data) || content.Header().Get("Content-Type") != "image/png" || content.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("content status=%d bytes=%d", content.Code, content.Body.Len())
	}
	head := request(p, d.MediaURL, http.MethodHead, "projects:read")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal("HEAD returned content")
	}
	foreign := p
	foreign.AccountScopeID = "other-account"
	for _, denied := range []*httptest.ResponseRecorder{
		request(foreign, d.MediaURL, http.MethodGet, "projects:read"),
		request(p, d.MediaURL, http.MethodGet, "unrelated:read"),
		request(p, d.MediaURL+"0", http.MethodGet, "projects:read"),
		request(p, strings.Replace(d.MediaURL, "field=media", "field=path", 1), http.MethodGet, "projects:read"),
	} {
		if denied.Code == 200 || bytes.Contains(denied.Body.Bytes(), data[:32]) {
			t.Fatal("unauthorized or stale media readable")
		}
	}
	stored, _, err := store.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || stored.Deliverables[0].MediaURL != inline || stored.Revision != task.Revision {
		t.Fatal("read changed persisted output")
	}
	// A new generation cannot be read through a prior digest.
	stored.Deliverables[0].MediaURL = "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<script>bad()</script>"))
	if err := store.PutProjectTask(p.AccountScopeID, stored); err != nil {
		t.Fatal(err)
	}
	if got := request(p, d.MediaURL, http.MethodGet, "projects:read"); got.Code != 404 {
		t.Fatalf("stale URL status=%d", got.Code)
	}
	unsafe := projectDeliverableForClient(stored, stored.Deliverables[0]).MediaURL
	if got := request(p, unsafe, http.MethodGet, "projects:read"); got.Code != 415 {
		t.Fatalf("active content status=%d", got.Code)
	}
}
