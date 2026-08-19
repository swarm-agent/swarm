package codex

import (
	"strings"
	"testing"
)

func TestParseEventStreamReaderAllowsHighlyFragmentedResponse(t *testing.T) {
	const fragments = 16_385
	var stream strings.Builder
	for i := 0; i < fragments; i++ {
		stream.WriteString("event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n")
	}
	stream.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")

	decoded, err := parseEventStreamReader(strings.NewReader(stream.String()), nil)
	if err != nil {
		t.Fatalf("parse highly fragmented response: %v", err)
	}
	if got := asString(decoded["id"]); got != "resp_1" {
		t.Fatalf("response id = %q, want resp_1", got)
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
