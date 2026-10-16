package auth

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: an adc (Vertex AI) credential stores the Google Cloud project
//     and location and nothing secret; its status shows the project and location,
//     never a key suffix; an adc credential without a project is refused; another
//     account cannot see it.
//   - Threat/regression: dropping the fields between API, service and store would
//     leave the runner without a project; accepting key material on an adc record
//     or reporting a Last4 would suggest a secret is held.
//   - Boundary/authority: Service.UpsertCredential, credentialStatusFromRecord and
//     the adc branch of pebblestore.AuthStore.UpsertCredential.
//   - Narrowest test layer: the auth service over a temp Pebble store; the API
//     handlers only copy these fields through.
func TestADCCredentialKeepsProjectAndLocationWithoutSecret(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "auth.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	authStore := pebblestore.NewAuthStore(store)
	service := NewService(authStore, events)

	if _, _, err := service.UpsertCredential(CredentialUpsertInput{Provider: "vertex", AccountScopeID: "account-1", Type: pebblestore.AuthTypeADC, GoogleLocation: "global", Active: true}); err == nil {
		t.Fatalf("adc credential without a project was saved")
	}
	status, _, err := service.UpsertCredential(CredentialUpsertInput{Provider: "vertex", AccountScopeID: "account-1", Type: pebblestore.AuthTypeADC, GoogleProject: " example-project ", GoogleLocation: "US-Central1", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if status.AuthType != pebblestore.AuthTypeADC || status.GoogleProject != "example-project" || status.GoogleLocation != "us-central1" || status.Last4 != "" || !status.Active {
		t.Fatalf("status = %+v", status)
	}
	raw, _ := json.Marshal(status)
	if !strings.Contains(string(raw), `"google_project":"example-project"`) || strings.Contains(string(raw), "last4") {
		t.Fatalf("status JSON = %s", raw)
	}
	record, ok, err := authStore.GetActiveCredentialForAccount("account-1", "vertex")
	if err != nil || !ok || record.APIKey != "" || record.AccessToken != "" || record.RefreshToken != "" || record.GoogleProject != "example-project" {
		t.Fatalf("stored record = %+v, ok=%v, err=%v", record, ok, err)
	}
	if _, found, err := authStore.GetActiveCredentialForAccount("account-2", "vertex"); err != nil || found {
		t.Fatalf("another account sees the vertex settings: found=%v err=%v", found, err)
	}
}
