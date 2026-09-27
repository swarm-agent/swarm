package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: task create/update must preserve media JSON above the old 1 MiB
// truncation boundary. Test the shared HTTP body reader directly, without a
// provider call, and require rejected requests to return no partial payload.
func TestProjectMediaRequest(t *testing.T) {
	payload := `{"attached_media":[{"data":"` + strings.Repeat("A", 2<<20) + `"}]}`
	for _, tc := range []struct {
		name, limit, body string
		status            int
	}{
		{"media default", "", payload, http.StatusOK},
		{"adjusted limit", "3145728", payload, http.StatusOK},
		{"exact limit", "2", `{}`, http.StatusOK},
		{"over limit", "2", `{} `, http.StatusRequestEntityTooLarge},
		{"invalid configuration", "bad", `{}`, http.StatusInternalServerError},
		{"zero configuration", "0", `{}`, http.StatusInternalServerError},
		{"negative configuration", "-1", `{}`, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(projectMediaRequestLimitEnv, tc.limit)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v3/projects/example/tasks", strings.NewReader(tc.body))
			body, ok := readProjectMediaRequest(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				if !ok || string(body) != tc.body || !json.Valid(body) {
					t.Fatal("valid media JSON was truncated or rejected")
				}
			} else if ok || body != nil {
				t.Fatal("rejected request returned partial media")
			}
			if tc.status == http.StatusRequestEntityTooLarge && !strings.Contains(w.Body.String(), projectMediaRequestLimitEnv) {
				t.Fatal("size error must explain how to adjust the limit")
			}
		})
	}
}
