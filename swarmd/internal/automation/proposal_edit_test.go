package automation

import (
	"context"
	"errors"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: EditParentDefinition cannot use an absent identity, user transport or
// foreign-account agent as sidechat authority. A nil repository makes any access
// past admission observable as a panic; rejection must occur before any writes.
func TestParentDefinitionEditRejectsUntrustedOrigin(t *testing.T) {
	s := &Service{}
	for _, ctx := range []context.Context{
		context.Background(),
		context.WithValue(context.Background(), runtimeIdentityKey{}, runtimeIdentity{principal: Principal{AccountID: "account", SubjectID: "user", Role: "user"}, explicit: true}),
		context.WithValue(context.Background(), runtimeIdentityKey{}, runtimeIdentity{principal: Principal{AccountID: "foreign", SubjectID: "sidechat", Role: "agent"}}),
	} {
		if _, _, err := s.EditParentDefinition(ctx, store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, "automation", "edit", 1, store.AutomationDefinition{}); !errors.Is(err, ErrDenied) {
			t.Fatalf("unexpected admission: %v", err)
		}
	}
}
