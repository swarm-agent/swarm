package diagnostics

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestProviderDiagnosticsRecordMetadataWithoutContent(t *testing.T) {
	t.Setenv(EnvName, "1")
	var recorded Event
	ctx := ContextWithRecorder(context.Background(), func(_ context.Context, event Event) {
		recorded = event
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/run?token=query-secret#fragment", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("X-Trace", "trace-secret")

	LogRequest("provider", "operation", req, []byte("request-secret"))
	if recorded.Body != "" || strings.Contains(recorded.Headers, "secret") || strings.Contains(recorded.URL, "secret") {
		t.Fatalf("request diagnostic retained content: %+v", recorded)
	}
	if recorded.Extra["request_bytes"] != len("request-secret") {
		t.Fatalf("request bytes = %#v", recorded.Extra["request_bytes"])
	}

	LogErrorContext(ctx, "provider", "operation", errors.New("upstream response-secret"))
	if recorded.Error != "provider operation failed" || strings.Contains(recorded.Error, "secret") {
		t.Fatalf("error diagnostic retained detail: %+v", recorded)
	}
}

func TestMetadataOnlyClearsBodyAndBoundsFields(t *testing.T) {
	event := metadataOnly(Event{Provider: strings.Repeat("p", 200), Body: "media-secret", Extra: map[string]any{"nested": map[string]any{"secret": "value"}, "shape": "bounded"}})
	if event.Body != "" {
		t.Fatalf("body = %q, want empty", event.Body)
	}
	if len([]rune(event.Provider)) > 100 {
		t.Fatalf("provider was not bounded: %d", len([]rune(event.Provider)))
	}
	if _, ok := event.Extra["nested"]; ok || event.Extra["shape"] != "bounded" {
		t.Fatalf("extra metadata was not reduced to safe scalars: %#v", event.Extra)
	}
}
