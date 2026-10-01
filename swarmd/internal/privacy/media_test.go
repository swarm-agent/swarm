package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Purpose: SafeError must remove credential-bearing nested transport errors
// without an exportable cause, preserving cancellation/deadline classification.
// This unit boundary is the narrowest proof independent of provider behavior.
func TestMediaSafeError(t *testing.T) {
	key := "fake-media-credential+/="
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		raw := &url.Error{Op: "Post", URL: "https://example.invalid/" + key + "?key=" + url.QueryEscape(key), Err: fmt.Errorf("echo %s %s: %w", key, url.QueryEscape(key), cause)}
		err := SafeError(fmt.Errorf("generate: %w", raw), key)
		for _, text := range []string{err.Error(), fmt.Sprintf("%+v %#v", err, err)} {
			if strings.Contains(text, key) || strings.Contains(text, url.QueryEscape(key)) {
				t.Fatal("credential survived safe error boundary")
			}
		}
		if !errors.Is(err, cause) || errors.Unwrap(err) != nil {
			t.Fatal("unsafe cause retention or lost cancellation classification")
		}
		encoded, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
		if marshalErr != nil || strings.Contains(string(encoded), key) {
			t.Fatal("serialized failure leaked credential")
		}
	}
	for _, text := range []string{"x-goog-api-key: " + key, "Authorization: Bearer " + key, "https://example.invalid/download?X-Goog-Signature=" + key} {
		safe := SanitizeDiagnostic(text)
		if strings.Contains(safe, key) || SanitizeDiagnostic(safe) != safe {
			t.Fatal("diagnostic redaction leaked or is not idempotent")
		}
	}
}

// Purpose: GoogleMediaClient and GoogleMediaURL must not send API headers or
// upload bodies to provider-selected foreign origins, including loopback ports.
// Two local servers prove the HTTP boundary rejects before any foreign request.
func TestMediaOriginAndRedirect(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "fake-header" {
			t.Error("authentication header lost")
		}
		http.Redirect(w, r, foreign.URL+"/signed?token=fake-signature", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	for _, reference := range []string{foreign.URL, "//example.invalid/file", "https://user:pass@example.invalid/file"} {
		if _, err := GoogleMediaURL(origin.URL, reference); err == nil {
			t.Fatal("foreign resource accepted")
		}
	}
	if _, err := GoogleMediaURL(origin.URL, "/file"); err != nil {
		t.Fatal("same origin resource rejected")
	}
	req, _ := http.NewRequest(http.MethodPost, origin.URL, strings.NewReader("private upload"))
	req.Header.Set("x-goog-api-key", "fake-header")
	_, err := GoogleMediaClient(&http.Client{Timeout: time.Second}).Do(req)
	if err == nil || foreignCalls != 0 {
		t.Fatal("redirect was not rejected before disclosure")
	}
}
