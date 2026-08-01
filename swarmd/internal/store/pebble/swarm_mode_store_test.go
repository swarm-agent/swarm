package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSwarmModeSettingsStoreMissingAndAccountIsolation(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)

	if record, found, err := settings.GetForAccount("account-a"); err != nil || found {
		t.Fatalf("missing GetForAccount() = (%+v, %v, %v), want zero, false, nil", record, found, err)
	}

	wantA := SwarmModeSettingsRecord{
		AccountScopeID:   "account-a",
		ActionFavoriteID: "favorite-action-a",
		UpdatedAt:        101,
	}
	gotA, err := settings.PutForAccount(wantA)
	if err != nil {
		t.Fatalf("PutForAccount(account-a): %v", err)
	}
	if gotA != wantA {
		t.Fatalf("PutForAccount(account-a) = %+v, want %+v", gotA, wantA)
	}

	if record, found, err := settings.GetForAccount("account-b"); err != nil || found {
		t.Fatalf("GetForAccount(account-b) = (%+v, %v, %v), want zero, false, nil", record, found, err)
	}
	gotA, found, err := settings.GetForAccount("account-a")
	if err != nil || !found {
		t.Fatalf("GetForAccount(account-a) found = %v, err = %v", found, err)
	}
	if gotA != wantA {
		t.Fatalf("GetForAccount(account-a) = %+v, want %+v", gotA, wantA)
	}
}

func TestSwarmModeSettingsStoreRoundTripsActionAndOptionalPlan(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)

	tests := []struct {
		name   string
		record SwarmModeSettingsRecord
	}{
		{
			name: "action only",
			record: SwarmModeSettingsRecord{
				AccountScopeID:   "account-action",
				ActionFavoriteID: "favorite-action",
				PlanEnabled:      false,
				UpdatedAt:        201,
			},
		},
		{
			name: "action and plan",
			record: SwarmModeSettingsRecord{
				AccountScopeID:   "account-plan",
				ActionFavoriteID: "favorite-action",
				PlanEnabled:      true,
				PlanFavoriteID:   "favorite-plan",
				UpdatedAt:        202,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stored, err := settings.PutForAccount(test.record)
			if err != nil {
				t.Fatalf("PutForAccount(): %v", err)
			}
			if stored != test.record {
				t.Fatalf("PutForAccount() = %+v, want %+v", stored, test.record)
			}

			got, found, err := settings.GetForAccount(test.record.AccountScopeID)
			if err != nil || !found {
				t.Fatalf("GetForAccount() found = %v, err = %v", found, err)
			}
			if got != test.record {
				t.Fatalf("GetForAccount() = %+v, want %+v", got, test.record)
			}
		})
	}
}

func TestSwarmModeSettingsStoreRejectsInvalidShapes(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)

	tests := []struct {
		name   string
		record SwarmModeSettingsRecord
		want   error
	}{
		{
			name: "missing account",
			record: SwarmModeSettingsRecord{
				ActionFavoriteID: "favorite-action",
			},
			want: ErrSwarmModeAccountScopeIDRequired,
		},
		{
			name: "missing action favorite",
			record: SwarmModeSettingsRecord{
				AccountScopeID: "account-a",
			},
			want: ErrSwarmModeActionFavoriteIDRequired,
		},
		{
			name: "enabled plan missing favorite",
			record: SwarmModeSettingsRecord{
				AccountScopeID:   "account-a",
				ActionFavoriteID: "favorite-action",
				PlanEnabled:      true,
			},
			want: ErrSwarmModePlanFavoriteIDRequired,
		},
		{
			name: "disabled plan has favorite",
			record: SwarmModeSettingsRecord{
				AccountScopeID:   "account-a",
				ActionFavoriteID: "favorite-action",
				PlanEnabled:      false,
				PlanFavoriteID:   "favorite-plan",
			},
			want: ErrSwarmModePlanFavoriteIDUnexpected,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := settings.PutForAccount(test.record); !errors.Is(err, test.want) {
				t.Fatalf("PutForAccount() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSwarmModeSettingsStoreValidatesAccountIdentityOnRead(t *testing.T) {
	store := openSwarmModeSettingsTestStore(t)
	settings := NewSwarmModeSettingsStore(store)

	if err := store.PutJSON(swarmModeSettingsKeyForAccount("account-a"), SwarmModeSettingsRecord{
		AccountScopeID:   "account-b",
		ActionFavoriteID: "favorite-action",
	}); err != nil {
		t.Fatalf("seed mismatched record: %v", err)
	}

	if _, found, err := settings.GetForAccount("account-a"); !errors.Is(err, ErrSwarmModeAccountScopeIDMismatch) || found {
		t.Fatalf("GetForAccount() found = %v, error = %v, want false and %v", found, err, ErrSwarmModeAccountScopeIDMismatch)
	}
}

func openSwarmModeSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "swarm-mode-settings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}
