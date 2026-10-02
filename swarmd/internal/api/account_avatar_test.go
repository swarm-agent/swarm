package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Requirement: the registered account avatar route authenticates and binds the
// rendered owner, enforces MIME/byte limits, and preserves bytes on failed writes.
// HTTP through Server.Handler is the narrowest test of middleware plus endpoint.
func TestAccountAvatarHTTPContract(t *testing.T) {
	server, store := newOnboardingIdentityTestServer(t, true)
	user, ok, err := store.GetUser("user_onboarding_test")
	if err != nil || !ok {
		t.Fatal("missing fixture user")
	}
	path := "/v1/account/avatar?" + url.Values{"user_id": {user.ID}, "account_scope_id": {user.AccountScopeID}}.Encode()
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	request := func(method, target, contentType string, data []byte) *httptest.ResponseRecorder {
		req := newAuthenticatedJSONSameOriginDesktopRequest(t, server, map[string]any{})
		req.Method = method
		req.URL, _ = url.Parse(target)
		req.Header.Set("Content-Type", contentType)
		req.Body = io.NopCloser(bytes.NewReader(data))
		req.ContentLength = int64(len(data))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}
	saved := request(http.MethodPut, path, "image/png", pixels.Bytes())
	if saved.Code != http.StatusOK {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	for _, tc := range []struct {
		path   string
		mime   string
		data   []byte
		status int
	}{
		{path, "image/jpeg", pixels.Bytes(), http.StatusUnsupportedMediaType},
		{path, "image/png", []byte("not a PNG"), http.StatusBadRequest},
		{path, "image/png", make([]byte, (2<<20)+1), http.StatusRequestEntityTooLarge},
		{"/v1/account/avatar?user_id=someone_else&account_scope_id=other", "image/png", pixels.Bytes(), http.StatusForbidden},
	} {
		if rec := request(http.MethodPut, tc.path, tc.mime, tc.data); rec.Code != tc.status {
			t.Fatalf("rejection: %d want %d: %s", rec.Code, tc.status, rec.Body.String())
		}
		got := request(http.MethodGet, path, "", nil)
		if got.Code != http.StatusOK || got.Body.String() != saved.Body.String() {
			t.Fatalf("failed upload changed saved avatar: %s", got.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK {
		t.Fatal("anonymous avatar read succeeded")
	}
	var response map[string]string
	if err := json.Unmarshal(saved.Body.Bytes(), &response); err != nil || response["image"] == "" || saved.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing safe image response")
	}
}
