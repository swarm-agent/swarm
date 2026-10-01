package run

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: media_inspect's narrow parser must accept exact independent evidence
// without letting mixed selectors, extra authority fields or trailing JSON bypass
// backend authentication. Store tests separately prove byte/owner validation.
func TestDesignMediaSelectorStrict(t *testing.T) {
	ref := mediaInspectDesignReference{SessionID: "parent", Preview: pebblestore.DesignPreviewRef{SHA256: strings.Repeat("a", 64), Output: pebblestore.DesignOutputRef{RequestID: "request", Candidate: 0, Attempt: 1, ChildSessionID: "child", RunID: "run", SHA256: strings.Repeat("b", 64)}}}
	valid := map[string]any{"design_preview_reference": ref}
	body, _ := json.Marshal(valid)
	got, err := decodeMediaInspectArguments(string(body))
	if err != nil || got.DesignPreviewReference == nil || *got.DesignPreviewReference != ref {
		t.Fatal(got, err)
	}
	for _, selector := range []string{"path", "asset_id", "session_id"} {
		input := map[string]any{"design_preview_reference": ref, selector: "untrusted"}
		b, _ := json.Marshal(input)
		if _, err := decodeMediaInspectArguments(string(b)); err == nil {
			t.Fatal("accepted mixed selector", selector)
		}
	}
	for _, invalid := range []string{string(body) + " {}", `{"design_preview_reference":{"session_id":"parent","preview":{},"account_id":"foreign"}}`, `{"design_preview_reference":{"session_id":"parent","preview":{}}}`} {
		if _, err := decodeMediaInspectArguments(invalid); err == nil {
			t.Fatal("accepted malformed reference")
		}
	}
}
