package openrouter

import (
	"strings"
	"testing"
)

func TestParseOpenRouterEventStreamRejectsMissingDone(t *testing.T) {
	err := parseOpenRouterEventStream(strings.NewReader("data: {\"choices\":[]}\n\n"), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("error = %v, want missing completion error", err)
	}
}

func TestParseOpenRouterEventStreamAllowsHighlyFragmentedResponse(t *testing.T) {
	const fragments = 16_385
	var stream strings.Builder
	for i := 0; i < fragments; i++ {
		stream.WriteString("data: {}\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")

	state := newOpenRouterStreamState()
	seen := 0
	err := parseOpenRouterEventStream(strings.NewReader(stream.String()), func(string) error {
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

func TestOpenRouterAPIErrorMessagePrefersMetadataRawDetail(t *testing.T) {
	raw := []byte(`{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"code\":400,\"message\":\"Corrupted thought signature.\",\"status\":\"INVALID_ARGUMENT\"}}"}}}`)
	if got := apiErrorMessage(raw); got != "Corrupted thought signature." {
		t.Fatalf("apiErrorMessage() = %q, want precise upstream detail", got)
	}
}

func TestOpenRouterAPIErrorMessageAcceptsPlainAndObjectMetadataRaw(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "plain string",
			raw:  `{"error":{"message":"Provider returned error","metadata":{"raw":"Malformed function call"}}}`,
			want: "Malformed function call",
		},
		{
			name: "object",
			raw:  `{"error":{"message":"Provider returned error","metadata":{"raw":{"detail":"Invalid tool schema"}}}}`,
			want: "Invalid tool schema",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiErrorMessage([]byte(tt.raw)); got != tt.want {
				t.Fatalf("apiErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenRouterAPIErrorMessageFallsBackWhenMetadataRawIsMissingOrMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing metadata",
			raw:  `{"error":{"message":"Invalid request","code":400}}`,
			want: "Invalid request",
		},
		{
			name: "metadata wrong shape",
			raw:  `{"error":{"message":"Provider returned error","code":400,"metadata":"invalid"}}`,
			want: "Provider returned error",
		},
		{
			name: "raw unsupported shape",
			raw:  `{"error":{"code":"invalid_request","metadata":{"raw":42}}}`,
			want: "invalid_request",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiErrorMessage([]byte(tt.raw)); got != tt.want {
				t.Fatalf("apiErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenRouterAPIErrorMessageRedactsAndBoundsMetadataRawDetail(t *testing.T) {
	raw := []byte(`{"error":{"message":"Provider returned error","metadata":{"raw":"authorization: Bearer secret-token ` + strings.Repeat("x", maxProviderErrorBytes) + `"}}}`)
	got := apiErrorMessage(raw)
	if strings.Contains(got, "secret-token") {
		t.Fatalf("error detail retained credential: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("error detail did not retain a redaction marker: %q", got)
	}
	if len(got) > maxProviderErrorBytes+len("…") {
		t.Fatalf("error detail length = %d", len(got))
	}
}
