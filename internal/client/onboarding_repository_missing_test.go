package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Requirement: InspectOnboardingRepository must preserve the daemon's typed
// missing-directory prerequisite so the TUI can request setup consent. It must
// not reinterpret permission failures, other conflicts, or mismatched paths as
// permission to create. This wire-level test is the narrowest transport boundary;
// inspection must issue only one authenticated GET and never mutate anything.
func TestInspectOnboardingMissingDirectory(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	for _, tc := range []struct {
		name   string
		status int
		body   string
		wantOK bool
	}{
		{"missing", 409, `{"ok":false,"code":"workspace_repository_not_ready","repository":{"path":"/projects/new","state":"directory_missing"}}`, true},
		{"denied", 403, `{"ok":false,"code":"workspace_repository_not_ready","repository":{"path":"/projects/new","state":"directory_missing"}}`, false},
		{"other-state", 409, `{"ok":false,"code":"workspace_repository_not_ready","repository":{"path":"/projects/new","state":"access_denied"}}`, false},
		{"other-path", 409, `{"ok":false,"code":"workspace_repository_not_ready","repository":{"path":"/projects/other","state":"directory_missing"}}`, false},
		{"untyped", 409, `{"error":"directory missing"}`, false},
		{"malformed", 409, `{`, false},
		{"unacknowledged", 200, `{"ok":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/workspace/repository" || r.URL.Query().Get("path") != "/projects/new" || r.Header.Get("X-Swarm-Token") != "test-token" {
					t.Error("inspection changed the request or attempted mutation")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			api := New(server.URL)
			api.SetToken("test-token")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			state, err := api.InspectOnboardingRepository(ctx, "/projects/new")
			if (err == nil) != tc.wantOK {
				t.Fatalf("state=%+v error=%v", state, err)
			}
			if tc.wantOK && (state.State != "directory_missing" || state.Path != "/projects/new" || state.ContentReady) {
				t.Fatalf("missing state lost or presented as ready: %+v", state)
			}
			if calls != 1 {
				t.Fatalf("expected one read-only inspection, got %d requests", calls)
			}
		})
	}
}
