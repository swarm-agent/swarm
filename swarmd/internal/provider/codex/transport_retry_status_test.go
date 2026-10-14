package codex

import (
	"net/http"
	"testing"
)

// Purpose: shouldRetryTransportStatus owns the inner WebSocket handshake retry
// policy. Pin empty-body edge rejection and all 5xx retries independently of
// the narrower outer turn policy, while rejecting body-bearing 403 responses.
// A direct policy test is the narrowest layer proving this classification;
// it does not claim live handshake or provider recovery evidence.
func TestShouldRetryTransportStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decoded map[string]any
		want    bool
	}{
		{"nil body", nil, true},
		{"empty map", map[string]any{}, true},
		{"empty raw body", map[string]any{"raw_body": ""}, true},
		{"whitespace raw body", map[string]any{"raw_body": " \n"}, true},
		{"body present", map[string]any{"raw_body": "forbidden"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetryTransportStatus(http.StatusForbidden, tc.decoded); got != tc.want {
				t.Fatalf("retry=%v want=%v", got, tc.want)
			}
		})
	}
	for status := 500; status <= 599; status++ {
		if !shouldRetryTransportStatus(status, nil) {
			t.Errorf("HTTP %d must retry", status)
		}
	}
	for _, status := range []int{200, 400, 401, 404, 429, 499, 600} {
		if shouldRetryTransportStatus(status, nil) {
			t.Errorf("HTTP %d must not retry", status)
		}
	}
}
