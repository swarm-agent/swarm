package environment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cloudTransport func(*http.Request) (*http.Response, error)

func (f cloudTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Purpose: GoogleREST.call owns bounded HTTP retries and API error propagation.
// A transport fake proves replayed uploads and bounded request deadlines without
// requiring cloud access; it does not claim IAM or live API qualification.
func TestCloudBuildRESTRetry(t *testing.T) {
	calls := 0
	rest := GoogleREST{Client: &http.Client{Transport: cloudTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("unbounded request")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "payload" {
			t.Fatalf("body replay: %q %v", data, err)
		}
		code := http.StatusTooManyRequests
		if calls == 2 {
			code = http.StatusOK
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("response")), Header: make(http.Header)}, nil
	})}}
	if _, err := rest.call(context.Background(), "POST", "https://example.invalid", strings.NewReader("payload"), 100); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

// Purpose: permanent API denials must retain the API detail and never retry;
// this direct transport boundary verifies the failure contract used by the CLI.
func TestCloudBuildRESTDenial(t *testing.T) {
	calls := 0
	rest := GoogleREST{Client: &http.Client{Transport: cloudTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"error":"permission denied"}`)), Header: make(http.Header)}, nil
	})}}
	_, err := rest.call(context.Background(), "GET", "https://example.invalid", nil, 100)
	if err == nil || !strings.Contains(err.Error(), "permission denied") || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
