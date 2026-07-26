package fireworks

import (
	"strings"
	"testing"
)

func TestParseFireworksEventStreamRejectsMissingDone(t *testing.T) {
	err := parseFireworksEventStream(strings.NewReader("data: {\"choices\":[]}\n\n"), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("error = %v, want missing completion error", err)
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
