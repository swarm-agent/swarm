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

func TestParseFireworksEventStreamAllowsAggregateResponseBeyondUnaryBodyLimit(t *testing.T) {
	const event = "data: {\"choices\":[]}\n\n"
	fragments := maxResponseBytes/len(event) + 1
	stream := strings.Repeat(event, fragments) + "data: [DONE]\n\n"

	state := newFireworksStreamState()
	seen := 0
	err := parseFireworksEventStream(strings.NewReader(stream), func(string) error {
		seen++
		return state.apply(chatCompletionChunk{})
	})
	if err != nil {
		t.Fatalf("parse response larger than unary body limit: %v", err)
	}
	if seen != fragments {
		t.Fatalf("fragments seen = %d, want %d", seen, fragments)
	}
}

func TestParseFireworksEventStreamRejectsOversizedSingleEvent(t *testing.T) {
	stream := "data: " + strings.Repeat("x", maxStreamEventBytes+1) + "\n\n"
	err := parseFireworksEventStream(strings.NewReader(stream), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "event byte limit") {
		t.Fatalf("error = %v, want single-event byte limit", err)
	}
}

func TestFireworksStreamStateAllowsOutputBeyondFormerAggregateLimit(t *testing.T) {
	state := newFireworksStreamState()
	chunk := chatCompletionChunk{Choices: []chatCompletionChoice{{Delta: &chatCompletionMessageDelta{
		Content:          strings.Repeat("x", 4<<20),
		ReasoningContent: "y",
	}}}}
	if err := state.apply(chunk); err != nil {
		t.Fatalf("apply output beyond former aggregate limit: %v", err)
	}
}

func TestFireworksStreamStateAllowsToolArgumentsBeyondFormerAggregateLimit(t *testing.T) {
	state := newFireworksStreamState()
	chunk := chatCompletionChunk{Choices: []chatCompletionChoice{{Delta: &chatCompletionMessageDelta{ToolCalls: []chatCompletionToolCallDelta{{
		Function: &chatCompletionToolFunctionDelta{Arguments: strings.Repeat("x", (1<<20)+1)},
	}}}}}}
	if err := state.apply(chunk); err != nil {
		t.Fatalf("apply tool arguments beyond former aggregate limit: %v", err)
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
