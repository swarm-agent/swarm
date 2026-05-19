package identity

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestBootstrapFirstIdentityUsernameOnlyCreatesCanonicalRecords(t *testing.T) {
	svc, store := newTestService(t, "user_generated", "team_generated")

	result, err := svc.BootstrapFirstIdentity(" Alice ")
	if err != nil {
		t.Fatalf("bootstrap first identity: %v", err)
	}
	if result.User.ID != "user_generated" || result.User.Username != "alice" {
		t.Fatalf("user = %+v", result.User)
	}
	if result.Team.ID != "team_generated" || result.Team.Name != defaultBackendTeamName || !result.Team.Default {
		t.Fatalf("team = %+v", result.Team)
	}
	if result.Membership.UserID != result.User.ID || result.Membership.TeamID != result.Team.ID || result.Membership.Role != pebblestore.TeamRoleOwner {
		t.Fatalf("membership = %+v", result.Membership)
	}
	if result.CurrentSelection.UserID != result.User.ID || result.CurrentSelection.TeamID != result.Team.ID || result.CurrentSelection.WorkspaceID != "" {
		t.Fatalf("selection = %+v", result.CurrentSelection)
	}
	if result.Counts != (pebblestore.IdentityCounts{Users: 1, Teams: 1, TeamMemberships: 1, CurrentSelections: 1}) {
		t.Fatalf("counts = %+v", result.Counts)
	}

	state, err := svc.StateSummary()
	if err != nil {
		t.Fatalf("state summary: %v", err)
	}
	if state.Counts != result.Counts || state.CurrentSelection == nil || state.CurrentSelection.UserID != result.User.ID {
		t.Fatalf("state = %+v", state)
	}
	byUsername, ok, err := store.GetUserByUsername("ALICE")
	if err != nil || !ok || byUsername.ID != result.User.ID {
		t.Fatalf("get user by username = %+v ok=%v err=%v", byUsername, ok, err)
	}
}

func TestBootstrapFirstIdentityRejectsRebootstrapAndPartialState(t *testing.T) {
	svc, _ := newTestService(t, "user_one", "team_one", "user_two", "team_two")
	if _, err := svc.BootstrapFirstIdentity("alice"); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if _, err := svc.BootstrapFirstIdentity("bob"); !errors.Is(err, ErrBootstrapExists) {
		t.Fatalf("rebootstrap err=%v, want ErrBootstrapExists", err)
	}

	cases := []struct {
		name string
		seed func(*pebblestore.IdentityStore) error
	}{
		{
			name: "user only",
			seed: func(store *pebblestore.IdentityStore) error {
				_, err := store.CreateUserIfAbsent(pebblestore.UserRecord{ID: "user_existing", Username: "existing"})
				return err
			},
		},
		{
			name: "team only",
			seed: func(store *pebblestore.IdentityStore) error {
				_, err := store.CreateTeamIfAbsent(pebblestore.TeamRecord{ID: "team_existing", Name: "Existing", Default: true})
				return err
			},
		},
		{
			name: "membership only is impossible but full partial membership state is rejected",
			seed: func(store *pebblestore.IdentityStore) error {
				if _, err := store.CreateUserIfAbsent(pebblestore.UserRecord{ID: "user_existing", Username: "existing"}); err != nil {
					return err
				}
				if _, err := store.CreateTeamIfAbsent(pebblestore.TeamRecord{ID: "team_existing", Name: "Existing", Default: true}); err != nil {
					return err
				}
				_, err := store.CreateTeamMembershipIfAbsent(pebblestore.TeamMembershipRecord{TeamID: "team_existing", UserID: "user_existing", Role: pebblestore.TeamRoleOwner})
				return err
			},
		},
		{
			name: "selection state",
			seed: func(store *pebblestore.IdentityStore) error {
				if _, err := store.CreateUserIfAbsent(pebblestore.UserRecord{ID: "user_existing", Username: "existing"}); err != nil {
					return err
				}
				if _, err := store.CreateTeamIfAbsent(pebblestore.TeamRecord{ID: "team_existing", Name: "Existing", Default: true}); err != nil {
					return err
				}
				if _, err := store.CreateTeamMembershipIfAbsent(pebblestore.TeamMembershipRecord{TeamID: "team_existing", UserID: "user_existing", Role: pebblestore.TeamRoleOwner}); err != nil {
					return err
				}
				_, err := store.PutCurrentSelection(pebblestore.CurrentSelectionRecord{UserID: "user_existing", TeamID: "team_existing"})
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			partialSvc, store := newTestService(t, "user_new", "team_new")
			if err := tc.seed(store); err != nil {
				t.Fatalf("seed partial state: %v", err)
			}
			if _, err := partialSvc.BootstrapFirstIdentity("newuser"); !errors.Is(err, ErrBootstrapExists) {
				t.Fatalf("bootstrap with partial state err=%v, want ErrBootstrapExists", err)
			}
		})
	}
}

func TestBootstrapFirstIdentityRejectsInvalidUsernameAndNormalizationCollision(t *testing.T) {
	svc, store := newTestService(t, "user_one", "team_one")
	if _, err := svc.BootstrapFirstIdentity("   "); err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("empty username err=%v, want username error", err)
	}
	if _, err := store.CreateUserIfAbsent(pebblestore.UserRecord{ID: "user_existing", Username: "alice"}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := svc.BootstrapFirstIdentity(" ALICE "); !errors.Is(err, ErrBootstrapExists) {
		t.Fatalf("bootstrap with existing username err=%v, want ErrBootstrapExists", err)
	}
}

func TestBootstrapFirstIdentityRejectsDerivedIdentityIndexOnlyState(t *testing.T) {
	rawStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "identity-index-only.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	if err := rawStore.PutBytes(pebblestore.KeyIdentityUserByUsername("alice"), []byte("missing-user")); err != nil {
		t.Fatalf("seed username index: %v", err)
	}
	svc := NewService(pebblestore.NewIdentityStore(rawStore), WithIDGenerator(func(prefix string) (string, error) {
		return prefix + "_new", nil
	}))
	if _, err := svc.BootstrapFirstIdentity("alice"); !errors.Is(err, ErrBootstrapExists) {
		t.Fatalf("bootstrap with index-only identity state err=%v, want ErrBootstrapExists", err)
	}
}

func TestBootstrapFirstIdentityDoesNotReadOrWriteSwarmKeys(t *testing.T) {
	rawStore, err := pebblestore.Open(filepath.Join(t.TempDir(), "identity-service.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = rawStore.Close() })
	identities := pebblestore.NewIdentityStore(rawStore)
	svc := NewService(identities, WithIDGenerator(func(prefix string) (string, error) {
		return prefix + "_one", nil
	}))
	if _, err := svc.BootstrapFirstIdentity("alice"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var swarmKeys []string
	if err := rawStore.IteratePrefix("swarm/", 0, func(key string, _ []byte) error {
		swarmKeys = append(swarmKeys, key)
		return nil
	}); err != nil {
		t.Fatalf("iterate swarm keys: %v", err)
	}
	if len(swarmKeys) != 0 {
		t.Fatalf("bootstrap wrote swarm keys: %v", swarmKeys)
	}
}

func TestBootstrapFirstIdentityDoesNotRequireTeamInput(t *testing.T) {
	seenPrefixes := make([]string, 0, 2)
	svc, _ := newTestServiceWithGenerator(t, func(prefix string) (string, error) {
		seenPrefixes = append(seenPrefixes, prefix)
		return prefix + "_generated", nil
	})
	result, err := svc.BootstrapFirstIdentity("alice")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if result.Team.Name != defaultBackendTeamName || !result.Team.Default {
		t.Fatalf("default backend team = %+v", result.Team)
	}
	if !reflect.DeepEqual(seenPrefixes, []string{"user", "team"}) {
		t.Fatalf("id generator prefixes = %v, want user/team only", seenPrefixes)
	}
}

func newTestService(t *testing.T, ids ...string) (*Service, *pebblestore.IdentityStore) {
	t.Helper()
	idx := 0
	return newTestServiceWithGenerator(t, func(prefix string) (string, error) {
		if idx >= len(ids) {
			t.Fatalf("unexpected id generation for prefix %q", prefix)
		}
		id := ids[idx]
		idx++
		return id, nil
	})
}

func newTestServiceWithGenerator(t *testing.T, generate IDGenerator) (*Service, *pebblestore.IdentityStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "identity-service.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	identities := pebblestore.NewIdentityStore(store)
	return NewService(identities, WithIDGenerator(generate)), identities
}
