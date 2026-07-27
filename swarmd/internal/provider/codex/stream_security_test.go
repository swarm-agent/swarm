package codex

import (
	"strings"
	"testing"
)

func TestParseEventStreamReaderRejectsMalformedEvent(t *testing.T) {
	_, err := parseEventStreamReader(strings.NewReader("event: response.output_text.delta\ndata: {not-json}\n\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "decode codex stream event") {
		t.Fatalf("error = %v, want malformed event error", err)
	}
}

func TestParseEventStreamReaderRequiresCompletedEvent(t *testing.T) {
	stream := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	_, err := parseEventStreamReader(strings.NewReader(stream), nil)
	if err == nil || !strings.Contains(err.Error(), "before response.completed") {
		t.Fatalf("error = %v, want missing completion error", err)
	}
}
