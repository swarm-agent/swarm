package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the registered /v3/limits boundary must enforce CAS and ceilings,
// not silently accept stale edits. Routed HTTP plus Pebble proves the wire contract.
func TestLimitsHTTPRevisionAndCeiling(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	call := func(method string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/v3/limits", bytes.NewReader(b))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
		return w
	}
	w := call(http.MethodGet, nil)
	if w.Code != 200 {
		t.Fatalf("get %d %s", w.Code, w.Body.String())
	}
	var l store.Limits
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	l.ActiveChildren = 2
	body := map[string]any{"expected_revision": l.Revision, "limits": l}
	if w = call(http.MethodPut, body); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPut, body); w.Code != 409 {
		t.Fatalf("stale %d", w.Code)
	}
	l.ActiveChildren = 257
	body["expected_revision"] = 1
	body["limits"] = l
	if w = call(http.MethodPut, body); w.Code != 400 {
		t.Fatalf("ceiling %d", w.Code)
	}
	w = call(http.MethodGet, nil)
	json.Unmarshal(w.Body.Bytes(), &l)
	if l.ActiveChildren != 2 || l.Revision != 1 {
		t.Fatalf("mutated %+v", l)
	}
}
