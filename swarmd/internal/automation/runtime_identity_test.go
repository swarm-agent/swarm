package automation

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: PolicyApproval runtime identity cannot come from JSON-like context
// keys, and agent/system origins cannot acquire explicit user approval. This
// adapter-level test is the narrowest layer proving origin and trigger separation.
func TestRuntimeIdentityOrigins(t *testing.T) {
	verified := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
	resolver := RuntimeApprovalIdentity()
	for _, origin := range []string{"user", "agent", "system"} {
		session := ""
		if origin == "agent" { session = "execution" }
		ctx, err := BindRuntimeIdentity(context.Background(), verified, origin, session)
		if err != nil { t.Fatal(err) }
		p, err := resolver.Current(ctx)
		if err != nil || p.Role != origin || p.AccountID != "account" { t.Fatal(p, err) }
		if origin == "agent" && p.SubjectID != session { t.Fatal("agent became owner") }
		_, err = resolver.ExplicitUser(ctx)
		if (err == nil) != (origin == "user") { t.Fatal("incorrect approval origin", origin) }
		scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
		for _, kind := range []string{"manual", "schedule", "event"} {
			err := (RuntimeTriggers{}).Verify(ctx, p, scope, Trigger{Kind: kind})
			allowed := kind == "manual" && origin == "user" || kind == "schedule" && origin == "system"
			if (err == nil) != allowed { t.Fatal("trigger origin mismatch", origin, kind) }
		}
		p.SubjectID = "substituted"
		if err := (RuntimeTriggers{}).Verify(ctx, p, scope, Trigger{Kind: "manual"}); err == nil { t.Fatal("substituted principal accepted") }
	}
	forged := context.WithValue(context.Background(), "principal", Principal{AccountID: "account", SubjectID: "owner", Role: "user"})
	if _, err := resolver.Current(forged); err == nil { t.Fatal("string context key granted identity") }
	if _, err := BindRuntimeIdentity(context.Background(), identity.Principal{}, "user", ""); err == nil { t.Fatal("invalid identity accepted") }
	if _, err := BindRuntimeIdentity(context.Background(), verified, "agent", ""); err == nil { t.Fatal("agent without session accepted") }
}
