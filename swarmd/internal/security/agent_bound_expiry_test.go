package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an agent-bound token (gateway or operator) stored without an expiry
// is refused when used, with a token.denied signal and one log line however
// often it is tried; it is not revoked or changed. Other keys stored without an
// expiry keep working (they are only warned about at start), and an agent-bound
// token with an expiry still validates.
// Threat: a gateway or operator token minted before agent-bound tokens had to
// expire staying valid forever on an internet-facing host.
// Boundary: Service.ValidateScopedToken. Narrowest layer: the service over a
// temporary store with records stored as an older daemon would have.
func TestAgentBoundTokenWithoutExpiryIsRefused(t *testing.T) {
	svc, authStore, feed := newExpiryTestService(t)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	const account = "acct-1"
	put := func(id string, record pebblestore.ScopedTokenRecord) string {
		t.Helper()
		raw := "swarm_test_" + id
		sum := sha256.Sum256([]byte(raw))
		record.ID, record.TokenHash, record.AccountScopeID, record.UserID = id, hex.EncodeToString(sum[:]), account, "user-1"
		record.Scopes = []string{"sessions:read", "sessions:write"}
		if err := authStore.PutScopedToken(record); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	gateway := put("tok_gw_old", pebblestore.ScopedTokenRecord{AgentName: "frontdesk"})
	operator := put("tok_op_old", pebblestore.ScopedTokenRecord{AgentName: "frontdesk", AgentOperator: true})
	current := put("tok_gw_new", pebblestore.ScopedTokenRecord{AgentName: "frontdesk", ExpiresAt: future})
	sdk := put("tok_sdk_old", pebblestore.ScopedTokenRecord{})

	for _, raw := range []string{gateway, gateway, operator} {
		if record, err := svc.ValidateScopedToken(raw); err == nil || record != nil || !strings.Contains(err.Error(), "no expiry") {
			t.Fatalf("agent-bound token without expiry: record=%v err=%v", record, err)
		}
	}
	for _, raw := range []string{current, sdk} {
		if record, err := svc.ValidateScopedToken(raw); err != nil || record == nil {
			t.Fatalf("token %s refused: %v", raw, err)
		}
	}
	if got := strings.Count(logs.String(), "refusing agent-bound token tok_gw_old"); got != 1 {
		t.Fatalf("gateway refusal logged %d times, want once:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "refusing agent-bound token tok_op_old") {
		t.Fatalf("operator refusal not logged:\n%s", logs.String())
	}
	page, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: account, KindPrefixes: []string{"token.denied"}})
	if err != nil {
		t.Fatal(err)
	}
	denied := map[string]bool{}
	for _, sig := range page.Signals {
		if sig.Attrs["reason"] == "agent-bound token has no expiry" {
			denied[sig.DedupKey] = true
		}
	}
	if !denied["token.denied:tok_gw_old"] || !denied["token.denied:tok_op_old"] || len(denied) != 2 {
		t.Fatalf("token.denied signals = %v", denied)
	}
	for _, id := range []string{"tok_gw_old", "tok_op_old"} {
		record, found, err := authStore.GetScopedToken(account, id)
		if err != nil || !found || record.Revoked || record.ExpiresAt != 0 || record.LastUsedAt != 0 {
			t.Fatalf("%s changed by the refusal: found=%v %+v err=%v", id, found, record, err)
		}
	}
}
