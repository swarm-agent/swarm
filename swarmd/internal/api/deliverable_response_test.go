package api

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: publicDeliverable is the response boundary shared by list, get and
// mutation handlers. Fencing tokens must stay durable but never enter public JSON.
// Testing the copy and its serialization is the narrowest redaction assertion.
func TestPublicationResponseHidesClaimWithoutErasingAuthority(t *testing.T) {
	rec := pebblestore.DeliverableRecord{
		ID:               "example",
		PublicationClaim: "private-fence",
		Status:           "reconciliation_required",
		ActionResult:     map[string]any{"tweet_id": "123"},
	}
	public := publicDeliverable(rec)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "publication_claim") || strings.Contains(string(encoded), "private-fence") {
		t.Fatal("public response exposed publication authority")
	}
	if rec.PublicationClaim != "private-fence" || public.Status != rec.Status || public.ActionResult["tweet_id"] != "123" {
		t.Fatal("redaction erased authority or receipt")
	}
}
