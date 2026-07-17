package modelprofile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceValidatesSingleAndSplitShapes(t *testing.T) {
	svc, ctx := newTestService(t, "acct-one")
	selection := completeSelection("model-one")

	single, err := svc.Create(ctx, Input{Name: "Solo", ModelMode: "single", Single: &selection})
	if err != nil || single.Single == nil || single.Plan != nil || single.Auto != nil {
		t.Fatalf("create single = %+v, err=%v", single, err)
	}
	plan, auto := completeSelection("plan-model"), completeSelection("auto-model")
	split, err := svc.Create(ctx, Input{Name: "Split", ModelMode: "split", Plan: &plan, Auto: &auto})
	if err != nil || split.Single != nil || split.Plan == nil || split.Auto == nil {
		t.Fatalf("create split = %+v, err=%v", split, err)
	}

	invalid := []Input{
		{Name: "mixed-single", ModelMode: "single", Single: &selection, Plan: &plan},
		{Name: "mixed-split", ModelMode: "split", Single: &selection, Plan: &plan, Auto: &auto},
		{Name: "missing-auto", ModelMode: "split", Plan: &plan},
		{Name: "incomplete", ModelMode: "single", Single: &Selection{Provider: "codex", Model: "m"}},
	}
	for _, input := range invalid {
		if _, err := svc.Create(ctx, input); err == nil {
			t.Fatalf("Create(%q) succeeded, want validation error", input.Name)
		}
	}
}

func TestServiceNameUniquenessRenameIsolationAndBulkDelete(t *testing.T) {
	svc, accountOne := newTestService(t, "acct-one")
	accountTwo := principalContext("acct-two")
	selection := completeSelection("model-one")

	first, err := svc.Create(accountOne, Input{Name: " Primary ", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := svc.Create(accountOne, Input{Name: "primary", ModelMode: "single", Single: &selection}); !errors.Is(err, pebblestore.ErrModelProfileNameConflict) {
		t.Fatalf("duplicate name err=%v", err)
	}
	other, err := svc.Create(accountTwo, Input{Name: "PRIMARY", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("same name in other account: %v", err)
	}

	first, err = svc.Update(accountOne, first.ProfileID, Input{Name: "Renamed", ModelMode: "single", Single: &selection})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := svc.Create(accountOne, Input{Name: "primary", ModelMode: "single", Single: &selection}); err != nil {
		t.Fatalf("old name index was not released: %v", err)
	}
	if _, err := svc.Get(accountOne, other.ProfileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account get err=%v", err)
	}

	result, err := svc.BulkDelete(accountOne, []string{first.ProfileID, "missing", first.ProfileID})
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != first.ProfileID || len(result.MissingIDs) != 1 || result.MissingIDs[0] != "missing" {
		t.Fatalf("bulk result = %+v", result)
	}
	if _, err := svc.Create(accountOne, Input{Name: "renamed", ModelMode: "single", Single: &selection}); err != nil {
		t.Fatalf("deleted name index was not released: %v", err)
	}
}

func newTestService(t *testing.T, accountScopeID string) (*Service, context.Context) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewModelProfileStore(store))
	return svc, principalContext(accountScopeID)
}

func principalContext(accountScopeID string) context.Context {
	return identity.ContextWithPrincipal(context.Background(), identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "user-one",
		AccountScopeID: accountScopeID,
	})
}

func completeSelection(model string) Selection {
	return Selection{
		Provider:    "codex",
		Model:       model,
		Thinking:    "high",
		ServiceTier: "priority",
		ContextMode: "full",
	}
}
