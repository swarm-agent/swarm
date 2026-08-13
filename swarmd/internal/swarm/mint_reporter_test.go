package swarm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestMintReporterSendsOnlyDerivedVersionedIdentifierAndCompletes(t *testing.T) {
	svc, _ := newTestService(t)
	state, err := svc.EnsureLocalState(EnsureLocalStateInput{})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}

	var requestBody string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		requestBody = string(body)
		if request.Method != http.MethodPost || request.URL.String() != MintReportURL {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q", request.Header.Get("Content-Type"))
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader(`{"accepted":true}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}

	reporter := newMintReporter(svc, MintReportURL, client)
	if err := reporter.ReportPending(context.Background()); err != nil {
		t.Fatalf("report pending: %v", err)
	}
	wantBody := `{"version":1,"identifier":"` + mintReportIdentifier(state.Node.SwarmID) + `"}`
	if requestBody != wantBody {
		t.Fatalf("request body = %q, want %q", requestBody, wantBody)
	}
	for _, forbidden := range []string{state.Node.SwarmID, "swarm_id", "name", "host", "user", "workspace", "account", "provider", "session", "fingerprint", "key", "network"} {
		if strings.Contains(requestBody, forbidden) {
			t.Fatalf("request body contains forbidden value %q: %s", forbidden, requestBody)
		}
	}
	if _, pending, err := svc.PendingMintReport(); err != nil || pending {
		t.Fatalf("report still pending=%t err=%v", pending, err)
	}
}

func TestMintReporterFailuresLeaveReportPending(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "redirect", statusCode: http.StatusTemporaryRedirect, body: `{"accepted":true}`},
		{name: "remote error", statusCode: http.StatusServiceUnavailable, body: `{}`},
		{name: "malformed response", statusCode: http.StatusAccepted, body: `not-json`},
		{name: "not accepted", statusCode: http.StatusAccepted, body: `{"accepted":false}`},
		{name: "oversized response", statusCode: http.StatusAccepted, body: strings.Repeat("x", mintReportMaxResponseBytes+1)},
		{name: "unexpected content type", statusCode: http.StatusAccepted, body: `{"accepted":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService(t)
			if _, err := svc.EnsureLocalState(EnsureLocalStateInput{}); err != nil {
				t.Fatalf("ensure local state: %v", err)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				header := http.Header{"Content-Type": []string{"application/json"}}
				if tt.name == "unexpected content type" {
					header.Set("Content-Type", "text/plain")
				}
				return &http.Response{StatusCode: tt.statusCode, Body: io.NopCloser(strings.NewReader(tt.body)), Header: header}, nil
			})}
			if err := newMintReporter(svc, MintReportURL, client).ReportPending(context.Background()); err == nil {
				t.Fatal("report unexpectedly succeeded")
			}
			if _, pending, err := svc.PendingMintReport(); err != nil || !pending {
				t.Fatalf("failed report pending=%t err=%v", pending, err)
			}
		})
	}
}

func TestMintReporterRejectsUnsafeEndpointBeforeSending(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureLocalState(EnsureLocalStateInput{}); err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	if err := newMintReporter(svc, "http://swarmagent.dev/api/mint", client).ReportPending(context.Background()); err == nil {
		t.Fatal("unsafe endpoint unexpectedly accepted")
	}
	if called {
		t.Fatal("unsafe endpoint reached transport")
	}
}
