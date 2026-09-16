package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/memory"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Desktop map adapter must retain WorkspaceMapStore CAS and
// account isolation. Missing consent, stale revision and invalid content must
// leave the complete memory document unchanged; reads never create maps.
func TestWorkspaceMapAPIExplicitSave(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := store.NewMemoryStore(db)
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	s.SetMemoryService(memory.NewService(ms, nil))
	call := func(account, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/memory/workspace-map", strings.NewReader(body))
		if account != "" {
			r = r.WithContext(identity.ContextWithPrincipal(r.Context(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: account, UserID: "user"}))
		}
		w := httptest.NewRecorder()
		s.handleWorkspaceMap(w, r)
		return w
	}
	if w := call("", "GET", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("empty", "GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"found":false`) {
		t.Fatal(w.Body.String())
	}
	empty, _ := ms.GetForAccount("empty")
	if len(empty.Entries) != 0 {
		t.Fatal("read created map")
	}
	maps := ms.WorkspaceMapView()
	record, _, err := maps.CreateDefaultForAccount("a", store.DefaultWorkspaceMap)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := ms.GetForAccount("a")
	payload := func(rev int64, confirm bool, content string) string {
		b, _ := json.Marshal(map[string]any{"expected_revision": rev, "confirm": confirm, "intent": "explicit save", "content": content})
		return string(b)
	}
	for _, body := range []string{payload(record.Revision, false, "# Workspace Map\nnew"), payload(record.Revision+1, true, "# Workspace Map\nnew"), payload(record.Revision, true, "invalid"), `{"confirm":true,"account":"other"}`} {
		if w := call("a", "POST", body); w.Code < 400 {
			t.Fatal("accepted invalid request")
		}
		after, _ := ms.GetForAccount("a")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("partial mutation")
		}
	}
	if w := call("b", "POST", payload(record.Revision, true, "# Workspace Map\nforeign")); w.Code < 400 {
		t.Fatal("cross account mutation")
	}
	if w := call("a", "POST", payload(record.Revision, true, "# Workspace Map\nnew")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	after, _ := ms.GetForAccount("a")
	if after.Entries[0].Origin != before.Entries[0].Origin || after.Entries[0].CreatedAt != before.Entries[0].CreatedAt || after.Entries[0].Revision <= record.Revision {
		t.Fatal("metadata or revision lost")
	}
	if w := call("a", "POST", payload(record.Revision, true, "# Workspace Map\nstale")); w.Code != 409 {
		t.Fatal(w.Code)
	}
	final, _ := ms.GetForAccount("a")
	if !reflect.DeepEqual(after, final) {
		t.Fatal("stale write changed state")
	}
}
