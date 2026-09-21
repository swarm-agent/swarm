package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWebhookSignatureComputation(t *testing.T) {
	secret := "test-secret-key-123"
	timestamp := int64(1789999999000)
	body := []byte(`{"event":"worker.occurrence.succeeded","worker_id":"auto_1"}`)

	sig := ComputeSignature(secret, timestamp, body)
	if sig == "" {
		t.Fatal("expected non-empty signature")
	}

	// Verify manually
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if sig != expected {
		t.Fatalf("expected signature %s, got %s", expected, sig)
	}
}

func TestWebhookMatchesEvent(t *testing.T) {
	tests := []struct {
		destEvents []string
		eventType  string
		want       bool
	}{
		{destEvents: nil, eventType: "worker.occurrence.started", want: true},
		{destEvents: []string{"*"}, eventType: "worker.occurrence.succeeded", want: true},
		{destEvents: []string{"started", "failed"}, eventType: "worker.occurrence.started", want: true},
		{destEvents: []string{"started", "failed"}, eventType: "worker.occurrence.succeeded", want: false},
		{destEvents: []string{"worker.occurrence.retry_exhausted"}, eventType: "worker.occurrence.retry_exhausted", want: true},
		{destEvents: []string{"failed"}, eventType: EventTestPing, want: true}, // test ping always matches
	}

	for _, tt := range tests {
		got := MatchesEvent(tt.destEvents, tt.eventType)
		if got != tt.want {
			t.Errorf("MatchesEvent(%v, %q) = %v; want %v", tt.destEvents, tt.eventType, got, tt.want)
		}
	}
}

func TestWebhookFormatPayload(t *testing.T) {
	event := WebhookEvent{
		Type:         EventOccurrenceSucceeded,
		WorkerID:     "auto_backup",
		WorkerTitle:  "Daily Backup",
		State:        "succeeded",
		Detail:       "all checkpoints completed",
		AttemptCount: 1,
	}

	// Generic
	payload, ct, err := FormatPayload(event, "generic")
	if err != nil || ct != "application/json" {
		t.Fatalf("generic format failed: %v", err)
	}
	var genericMap map[string]any
	if err := json.Unmarshal(payload, &genericMap); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if genericMap["event"] != EventOccurrenceSucceeded || genericMap["worker_title"] != "Daily Backup" {
		t.Fatalf("unexpected generic payload: %s", string(payload))
	}

	// Slack
	payload, ct, err = FormatPayload(event, "slack")
	if err != nil || ct != "application/json" {
		t.Fatalf("slack format failed: %v", err)
	}
	var slackMap map[string]string
	if err := json.Unmarshal(payload, &slackMap); err != nil {
		t.Fatalf("invalid slack json: %v", err)
	}
	if slackMap["text"] == "" {
		t.Fatalf("expected non-empty slack text, got %s", string(payload))
	}

	// Discord
	payload, ct, err = FormatPayload(event, "discord")
	if err != nil || ct != "application/json" {
		t.Fatalf("discord format failed: %v", err)
	}
	var discordMap map[string]string
	if err := json.Unmarshal(payload, &discordMap); err != nil {
		t.Fatalf("invalid discord json: %v", err)
	}
	if discordMap["content"] == "" {
		t.Fatalf("expected non-empty discord content, got %s", string(payload))
	}

	// Telegram
	payload, ct, err = FormatPayload(event, "telegram")
	if err != nil || ct != "application/json" {
		t.Fatalf("telegram format failed: %v", err)
	}
	var telegramMap map[string]string
	if err := json.Unmarshal(payload, &telegramMap); err != nil {
		t.Fatalf("invalid telegram json: %v", err)
	}
	if telegramMap["text"] == "" {
		t.Fatalf("expected non-empty telegram text, got %s", string(payload))
	}
}

func TestWebhookDeliverSyncAndVerification(t *testing.T) {
	secret := "secret-signing-key"
	var receivedHeaders http.Header
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"received": true}`))
	}))
	defer server.Close()

	dispatcher := NewDispatcher(nil)
	defer dispatcher.Close()

	event := WebhookEvent{
		Type:         EventOccurrenceSucceeded,
		WorkerID:     "auto_123",
		WorkerTitle:  "CI Worker",
		State:        "succeeded",
		Detail:       "clean run",
		Timestamp:    time.Now().UnixMilli(),
		AccountID:    "acct_test",
	}

	dest := Destination{
		ID:      "whk_1",
		URL:     server.URL,
		Secret:  secret,
		Format:  "generic",
		Enabled: true,
	}

	result, err := dispatcher.DeliverSync(context.Background(), event, dest)
	if err != nil {
		t.Fatalf("DeliverSync failed: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}

	// Verify HTTP headers
	if receivedHeaders.Get("X-Swarm-Event") != EventOccurrenceSucceeded {
		t.Fatalf("unexpected X-Swarm-Event: %s", receivedHeaders.Get("X-Swarm-Event"))
	}
	if receivedHeaders.Get("X-Swarm-Delivery") == "" {
		t.Fatal("expected non-empty X-Swarm-Delivery header")
	}
	sigHeader := receivedHeaders.Get("X-Swarm-Signature")
	if sigHeader == "" {
		t.Fatal("expected non-empty X-Swarm-Signature header")
	}

	// Verify HMAC signature on receiver side
	var receivedTimestamp int64
	fmt.Sscanf(receivedHeaders.Get("X-Swarm-Timestamp"), "%d", &receivedTimestamp)
	expectedSig := "sha256=" + ComputeSignature(secret, receivedTimestamp, receivedBody)
	if sigHeader != expectedSig {
		t.Fatalf("signature mismatch: got %s, want %s", sigHeader, expectedSig)
	}
}

func TestWebhookDispatchAsync(t *testing.T) {
	var mu sync.Mutex
	receivedEvents := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedEvents = append(receivedEvents, r.Header.Get("X-Swarm-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := NewDispatcher(nil)
	defer dispatcher.Close()

	dest := Destination{
		ID:      "whk_async",
		URL:     server.URL,
		Enabled: true,
		Events:  []string{"started", "succeeded"},
	}

	// Dispatch matching
	dispatcher.DispatchAsync(WebhookEvent{Type: EventOccurrenceStarted, WorkerID: "auto_1"}, []Destination{dest})
	dispatcher.DispatchAsync(WebhookEvent{Type: EventOccurrenceSucceeded, WorkerID: "auto_1"}, []Destination{dest})
	// Dispatch non-matching (failed)
	dispatcher.DispatchAsync(WebhookEvent{Type: EventOccurrenceFailed, WorkerID: "auto_1"}, []Destination{dest})

	// Wait up to 2 seconds for background delivery
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(receivedEvents)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) != 2 {
		t.Fatalf("expected exactly 2 received events, got %d: %v", len(receivedEvents), receivedEvents)
	}
	if receivedEvents[0] != EventOccurrenceStarted || receivedEvents[1] != EventOccurrenceSucceeded {
		t.Fatalf("unexpected received events: %v", receivedEvents)
	}
}
