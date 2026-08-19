package fireworks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fireworksRoundTripFunc func(*http.Request) (*http.Response, error)

func (f fireworksRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestVerifyFireworksAPIKeyUsesCanonicalVerificationEndpoint(t *testing.T) {
	client := &Client{httpClient: &http.Client{Transport: fireworksRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != verifyAPIKeyURL {
			t.Fatalf("request URL = %q, want %q", got, verifyAPIKeyURL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-fireworks-key" {
			t.Fatalf("authorization header = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})}}

	message, err := client.VerifyAPIKey(context.Background(), " test-fireworks-key ")
	if err != nil {
		t.Fatalf("VerifyAPIKey: %v", err)
	}
	if message != "Fireworks API key verified via /verifyApiKey" {
		t.Fatalf("message = %q", message)
	}
}

func TestParseFireworksEventStreamRejectsMissingDone(t *testing.T) {
	err := parseFireworksEventStream(strings.NewReader("data: {\"choices\":[]}\n\n"), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("error = %v, want missing completion error", err)
	}
}

func TestParseFireworksEventStreamAllowsHighlyFragmentedResponse(t *testing.T) {
	const fragments = 16_385
	var stream strings.Builder
	for i := 0; i < fragments; i++ {
		stream.WriteString("data: {}\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")

	state := newFireworksStreamState()
	seen := 0
	err := parseFireworksEventStream(strings.NewReader(stream.String()), func(string) error {
		seen++
		return state.apply(chatCompletionChunk{})
	})
	if err != nil {
		t.Fatalf("parse highly fragmented response: %v", err)
	}
	if seen != fragments {
		t.Fatalf("fragments seen = %d, want %d", seen, fragments)
	}
}

func TestFireworksAPIErrorMessageRedactsAndBoundsDetail(t *testing.T) {
	raw := []byte(`{"error":{"message":"authorization: Bearer secret-token ` + strings.Repeat("x", maxProviderErrorBytes) + `"}}`)
	got := apiErrorMessage(raw)
	if strings.Contains(got, "secret-token") {
		t.Fatalf("error detail retained credential: %q", got)
	}
	if len(got) > maxProviderErrorBytes+len("…") {
		t.Fatalf("error detail length = %d", len(got))
	}
}
