package pebblestore

import "testing"

// Purpose: the stored end-user binding is the authority for limits and usage,
// so reading it must fail closed: a malformed binding is an error (never read
// as "no end user", which would drop the session to token-only limits), and an
// end user's key encoding keeps case-different ids apart (store key parts are
// lowercased). Boundary: SessionEndUserFromMetadata, EndUserScopeID. Narrowest
// layer: the pure functions.
func TestSessionEndUserBindingFailsClosedAndKeepsCase(t *testing.T) {
	metadata := map[string]any{SessionEndUserMetadataKey: EndUserMetadata("frontdesk", "Cust_42", map[string]string{"plan": "pro"})}
	binding, ok, err := SessionEndUserFromMetadata(metadata)
	if err != nil || !ok || binding.EndUser != "Cust_42" || binding.AgentName != "frontdesk" || binding.Labels["plan"] != "pro" {
		t.Fatalf("binding %+v ok=%v err=%v", binding, ok, err)
	}
	if _, ok, err := SessionEndUserFromMetadata(map[string]any{"agent_name": "frontdesk"}); ok || err != nil {
		t.Fatalf("a session without a binding: ok=%v err=%v", ok, err)
	}
	for name, value := range map[string]any{
		"not a map":     "Cust_42",
		"no agent":      map[string]any{"end_user": "Cust_42"},
		"bad end user":  map[string]any{"agent_name": "frontdesk", "end_user": "a b"},
		"bad labels":    map[string]any{"agent_name": "frontdesk", "end_user": "x", "labels": []any{"a"}},
		"numeric label": map[string]any{"agent_name": "frontdesk", "end_user": "x", "labels": map[string]any{"k": 1}},
	} {
		if _, ok, err := SessionEndUserFromMetadata(map[string]any{SessionEndUserMetadataKey: value}); err == nil || ok {
			t.Fatalf("%s: malformed binding read as ok=%v err=%v", name, ok, err)
		}
	}
	if EndUserScopeID("Alice") == EndUserScopeID("alice") {
		t.Fatal("case-different end users share a scope id")
	}
	if back, err := EndUserFromScopeID(EndUserScopeID("Cust_42@example.com")); err != nil || back != "Cust_42@example.com" {
		t.Fatalf("scope id round trip: %q %v", back, err)
	}
}
