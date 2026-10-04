package api

import (
	"net/http/httptest"
	"testing"

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
