package modelprofile

import (
	"errors"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSwarmServiceCRUDIsolationAndValidation(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm-profile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewSwarmService(pebblestore.NewSwarmProfileStore(store))
	selection := Selection{Provider: "codex", Model: "gpt", Thinking: "high", ServiceTier: "default", ContextMode: "full"}
	accountOne := identity.ContextWithPrincipal(t.Context(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"})
	accountTwo := identity.ContextWithPrincipal(t.Context(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-two", AccountScopeID: "account-two"})
	input := SwarmInput{Name: "Crew", Members: []SwarmMemberInput{{AgentID: "swarm", ModelMode: "single", Single: &selection}, {AgentID: "system-explorer", ModelMode: "split", Plan: &selection, Auto: &selection}}}
	created, err := svc.Create(accountOne, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.ProfileID == "" || len(created.Members) != 2 {
		t.Fatalf("created = %#v", created)
	}
	if _, err := svc.Get(accountTwo, created.ProfileID); !errors.Is(err, ErrSwarmProfileNotFound) {
		t.Fatalf("cross-account get err = %v", err)
	}
	if _, err := svc.Create(accountOne, input); !errors.Is(err, pebblestore.ErrSwarmProfileNameConflict) {
		t.Fatalf("duplicate err = %v", err)
	}
	input.Name = "Renamed"
	updated, err := svc.Update(accountOne, created.ProfileID, input)
	if err != nil || updated.Name != "Renamed" {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	deleted, err := svc.Delete(accountOne, created.ProfileID)
	if err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	invalid := SwarmInput{Name: "bad", Members: []SwarmMemberInput{{AgentID: "swarm", ModelMode: "single", Single: &selection}, {AgentID: "SWARM", ModelMode: "single", Single: &selection}}}
	if _, err := svc.Create(accountOne, invalid); err == nil {
		t.Fatal("expected duplicate member validation error")
	}
}
