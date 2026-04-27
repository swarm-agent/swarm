package copilot

import (
	"context"
	"strings"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestStatusFallsBackToCopilotCLISidecarWhenNoCredentialSaved(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	authStore := pebblestore.NewAuthStore(store)
	manager := NewManager(authStore)
	adapter := NewAdapterWithManager(authStore, manager)

	status, err := adapter.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Ready {
		t.Skipf("local Copilot CLI sidecar is unavailable or not logged in: %s", status.Reason)
	}
	if !strings.Contains(status.Reason, "Copilot CLI sidecar authenticated") {
		t.Fatalf("status reason = %q, want sidecar authenticated message", status.Reason)
	}
}

func TestStatusRechecksSavedCopilotCLISourceWithStaleDisconnectedConnection(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	authStore := pebblestore.NewAuthStore(store)
	record, err := authStore.UpsertCredential(pebblestore.AuthCredentialInput{
		Provider:  "copilot",
		Type:      pebblestore.AuthTypeCLI,
		Label:     "Copilot CLI login",
		SetActive: true,
	})
	if err != nil {
		t.Fatalf("upsert credential: %v", err)
	}
	if _, err := authStore.UpdateCredentialConnection("copilot", record.ID, &pebblestore.AuthCredentialConnectionRecord{
		Connected: false,
		Method:    pebblestore.AuthTypeCLI,
		Message:   "Copilot CLI is not authenticated; run `copilot login`",
	}); err != nil {
		t.Fatalf("update connection: %v", err)
	}

	adapter := NewAdapterWithManager(authStore, nil)
	status, err := adapter.status(context.Background(), func(context.Context, provideriface.AuthCredential) (AuthStatus, error) {
		return AuthStatus{IsAuthenticated: true, Login: "octocat"}, nil
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Ready {
		t.Fatalf("status.Ready = false, reason = %q", status.Reason)
	}
	if !strings.Contains(status.Reason, "Copilot CLI sidecar authenticated as octocat") {
		t.Fatalf("status reason = %q, want live sidecar authenticated message", status.Reason)
	}
}
