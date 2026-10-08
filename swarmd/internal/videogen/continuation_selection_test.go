package videogen

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: PreflightVideoOperation and GenerateManagedVideo must preserve account
// selection policy without bypassing continuation provenance or submitting rejected
// requests. This service-layer test exercises the real evaluator and dispatch gate;
// a rejecting transport makes accidental provider calls observable and hermetic.
func TestVideoContinuationSelection(t *testing.T) {
	const omni = "gemini-omni-1.1-flash"
	for _, tc := range []struct {
		name, operation, iteration, explicit, mutation, wantErr string
	}{
		{name: "create default only", operation: "create"},
		{name: "missing iteration", operation: "extend", wantErr: "no default video iteration model configured"},
		{name: "configured iteration", operation: "extend", iteration: omni},
		{name: "explicit precedence", operation: "extend", iteration: "veo-lite-preview", explicit: omni},
		{name: "unsupported operation model", operation: "extend", explicit: "veo-lite-preview", wantErr: "Veo 3.1 standard or fast"},
		{name: "missing source", operation: "extend", explicit: omni, mutation: "missing", wantErr: "requires source video"},
		{name: "stale source", operation: "extend", explicit: omni, mutation: "expired", wantErr: "expired"},
		{name: "cross account", operation: "extend", explicit: omni, mutation: "account", wantErr: "different account"},
		{name: "credential mismatch", operation: "extend", explicit: omni, mutation: "credential", wantErr: "credential mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, accountID := setupTestAuthStore(t, "google-key", "")
			ui := setupTestUISettingsForAccount(t, accountID, omni, tc.iteration)
			svc := NewService(auth, ui, setupTestCatalog())
			calls := 0
			svc.SetHTTPClient(&http.Client{Transport: privacyTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, fmt.Errorf("unexpected provider submission")
			})})
			prov := &pebblestore.VideoProvenance{
				Provider: ProviderGoogleGemini, Model: omni, AccountScopeID: accountID,
				CredentialID: "cred-google", Transport: pebblestore.VideoTransportGoogleInteractions,
				InteractionID: "retained-test-interaction", ObservedDurationMs: 10005,
				ObservedWidth: 1280, ObservedHeight: 720,
			}
			source := &ManagedVideoSource{InteractionID: prov.InteractionID, Provenance: prov}
			switch tc.mutation {
			case "missing":
				source = nil
			case "expired":
				prov.ExpiresAt = 1
			case "account":
				prov.AccountScopeID = "other-account"
			case "credential":
				prov.CredentialID = "other-credential"
			}
			if tc.operation == "create" {
				source = nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			principal := identity.Principal{AccountScopeID: accountID}
			result, err := svc.PreflightVideoOperation(ctx, VideoPreflightRequest{
				Principal: principal, Operation: tc.operation, ExplicitModel: tc.explicit, Prompt: "Continue scene", Source: source,
			})
			if tc.wantErr == "" {
				if err != nil || result == nil || result.ResolvedModel != omni || result.DurationSeconds != 0 || result.Operation != tc.operation {
					t.Fatalf("preflight = %+v, %v", result, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || result != nil {
					t.Fatalf("expected %q and no result, got %+v, %v", tc.wantErr, result, err)
				}
				_, err = svc.GenerateManagedVideo(ctx, ManagedVideoRequest{
					Principal: principal, Operation: tc.operation, Model: tc.explicit, Prompt: "Continue scene", Source: source,
				})
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("generation rejection = %v", err)
				}
			}
			if calls != 0 {
				t.Fatalf("provider calls = %d, want zero", calls)
			}
		})
	}
}
