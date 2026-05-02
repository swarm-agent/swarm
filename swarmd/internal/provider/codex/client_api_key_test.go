package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSendRoutesAPIKeyAuthToOpenAIResponsesHTTP(t *testing.T) {
	var gotAuth string
	var gotAccountID string
	var gotBeta string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get(chatGPTAccountIDHeader)
		gotBeta = r.Header.Get(openAIBetaHeader)
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(nil)
	client.responsesAPIURL = server.URL + "/v1/responses"

	var deltas []string
	decoded, status, err := client.send(context.Background(), pebblestore.CodexAuthRecord{
		Type:   pebblestore.CodexAuthTypeAPI,
		APIKey: "sk-test",
	}, []byte(`{"model":"gpt-5","stream":true,"input":[]}`), func(event StreamEvent) {
		if event.Type == StreamEventOutputTextDelta {
			deltas = append(deltas, event.Delta)
		}
	})
	if err != nil {
		t.Fatalf("send API key error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotAccountID != "" {
		t.Fatalf("%s = %q, want empty for API key auth", chatGPTAccountIDHeader, gotAccountID)
	}
	if gotBeta != "" {
		t.Fatalf("%s = %q, want empty for API key auth", openAIBetaHeader, gotBeta)
	}
	if decoded[codexTransportMetadataKey] != codexTransportResponsesHTTP {
		t.Fatalf("transport metadata = %v, want %s", decoded[codexTransportMetadataKey], codexTransportResponsesHTTP)
	}
	if decoded[codexConnectedViaWSMetadataKey] != false {
		t.Fatalf("websocket metadata = %v, want false", decoded[codexConnectedViaWSMetadataKey])
	}
	if strings.Join(deltas, "") != "ok" {
		t.Fatalf("stream deltas = %q, want ok", strings.Join(deltas, ""))
	}
}
