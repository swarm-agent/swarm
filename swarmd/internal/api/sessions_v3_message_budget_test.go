package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: optional byte bounds apply only to backward history, preserving
// legacy forward callers. The parser/response boundary is the narrowest layer
// proving invalid requests fail and byte-limited pages retain continuation even
// when their message count is below the requested count.
func TestSessionsV3MessageByteBudgetQuery(t *testing.T) {
	for _, raw := range []string{"max_bytes=1", "tail=true&max_bytes=0", "tail=true&max_bytes=262145", "tail=true&max_bytes=bad"} {
		w := httptest.NewRecorder()
		if _, ok := parseSessionsV3MessagesPageQuery(w, httptest.NewRequest("GET", "/?"+raw, nil)); ok || w.Code != 400 {
			t.Fatalf("accepted invalid byte query %q: %d", raw, w.Code)
		}
	}
	w := httptest.NewRecorder()
	query, ok := parseSessionsV3MessagesPageQuery(w, httptest.NewRequest("GET", "/?before_seq=9&limit=200&max_bytes=262144", nil))
	if !ok || query.MaxBytes != pebblestore.V3RecentMessageByteBudget {
		t.Fatalf("valid query rejected: %+v", query)
	}
	query.MoreOlder = true
	response := sessionsV3MessagesPageResponse("budget", []pebblestore.MessageSnapshot{{GlobalSeq: 8, Content: "whole"}}, query)
	if response["has_more_older"] != true || response["next_before_seq"] != uint64(8) || response["count"] != 1 {
		t.Fatalf("lost byte-limited continuation: %+v", response)
	}
	legacy, ok := parseSessionsV3MessagesPageQuery(httptest.NewRecorder(), httptest.NewRequest("GET", "/?after_seq=1", nil))
	if !ok || legacy.MaxBytes != 0 {
		t.Fatal("legacy forward history changed")
	}
}

// Requirement: the actual authorized hydrate and history routes must reconstruct
// a byte-limited suffix exactly, even after a concurrent append. This HTTP layer
// proves the store budget is wired to production admission and continuation;
// foreign principals must get no messages and reads must not alter history.
func TestSessionsV3MessageByteBudgetHTTPReconstruction(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "budget-http", "budget")
	for i := 0; i < 12; i++ {
		appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, fmt.Sprintf("budget-%d", i), strings.Repeat("界", 20000))
	}
	want, err := server.sessions.ListSessionMessages(created.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(sessionsV3SelectedSessionHydrateRequest(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(w, withTestPrincipal(r))
	if w.Code != 200 {
		t.Fatalf("hydrate %d: %s", w.Code, w.Body.String())
	}
	var hydrate struct {
		Messages map[string][]pebblestore.MessageSnapshot `json:"messages_by_session"`
		Cursor   string                                   `json:"snapshot_endpoint_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &hydrate); err != nil {
		t.Fatal(err)
	}
	got := hydrate.Messages[created.ID]
	wire, _ := json.Marshal(got)
	if len(got) == 0 || len(got) >= len(want) || len(wire) > pebblestore.V3RecentMessageByteBudget || hydrate.Cursor == "" {
		t.Fatalf("unbounded/unrecoverable hydrate: count=%d bytes=%d cursor=%q", len(got), len(wire), hydrate.Cursor)
	}
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "budget-later", "later append")
	for page := 0; page < 20; page++ {
		w = httptest.NewRecorder()
		path := fmt.Sprintf("/v3/sessions/%s/messages?before_seq=%d&limit=200&max_bytes=262144", created.ID, got[0].GlobalSeq)
		server.Handler().ServeHTTP(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, path, nil)))
		if w.Code != 200 {
			t.Fatalf("page %d: %s", w.Code, w.Body.String())
		}
		var response struct {
			Messages []pebblestore.MessageSnapshot `json:"messages"`
			More     bool                          `json:"has_more_older"`
			Before   uint64                        `json:"next_before_seq"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Messages) == 0 {
			t.Fatal("lost continuation")
		}
		got = append(response.Messages, got...)
		if !response.More {
			break
		}
		if response.Before != got[0].GlobalSeq {
			t.Fatal("incorrect boundary")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("HTTP pages lost or changed snapshot messages")
	}
	foreign := testPrincipal()
	foreign.AccountScopeID = "foreign-account"
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v3/sessions/%s/messages?tail=true&max_bytes=262144", created.ID), nil)
	r = r.WithContext(identity.ContextWithPrincipal(r.Context(), foreign))
	server.Handler().ServeHTTP(w, r)
	if w.Code == 200 || strings.Contains(w.Body.String(), strings.Repeat("界", 10)) {
		t.Fatal("foreign principal received history")
	}
	after, err := server.sessions.ListSessionMessages(created.ID, 0, 200)
	if err != nil || len(after) != len(want)+1 {
		t.Fatalf("read mutated history: %v", err)
	}
}
