package codex

import (
	"strings"
	"testing"
)

func TestParseEventStreamReaderAllowsAggregateResponseBeyondUnaryEventLimit(t *testing.T) {
	const event = "event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n"
	fragments := maxCodexStreamEventBytes/len(event) + 1
	stream := strings.Repeat(event, fragments) + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"

	decoded, err := parseEventStreamReader(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parse response larger than single-event limit: %v", err)
	}
	if got := asString(decoded["id"]); got != "resp_1" {
		t.Fatalf("response id = %q, want resp_1", got)
	}
}

func TestParseEventStreamReaderRejectsOversizedSingleEvent(t *testing.T) {
	stream := "event: response.in_progress\ndata: " + strings.Repeat("x", maxCodexStreamEventBytes+1) + "\n\n"
	_, err := parseEventStreamReader(strings.NewReader(stream), nil)
	if err == nil || !strings.Contains(err.Error(), "event byte limit") {
		t.Fatalf("error = %v, want single-event byte limit", err)
	}
}

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
