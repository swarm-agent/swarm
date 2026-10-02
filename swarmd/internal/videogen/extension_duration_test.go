package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: GenerateManagedVideo must accept small container/frame timing drift at
// Omni's 3-10s extension boundaries and 40s total ceiling without rounding away
// real violations or leaking invalid output to artifact publication. The service
// layer is the narrowest boundary owning output probes and returned provenance.
// Deterministic transport/probe doubles exercise that boundary, NOT live provider
// behavior. Rejections must return a wholly empty result; success must retain the
// exact source lineage, measured time, credential binding and retained interaction.
func TestOmniExtensionMeasuredDuration(t *testing.T) {
	const omni = "gemini-omni-1.1-flash"
	for _, tc := range []struct {
		name       string
		source     float64
		output     float64
		handleOnly bool
		wantErr    string
	}{
		{name: "exact minimum", source: 10, output: 13},
		{name: "exact maximum", source: 10, output: 20},
		{name: "fractional exact maximum", source: 10.005, output: 10.005 + 10},
		{name: "container audio drift", source: 10.005, output: 20.026},
		{name: "one frame drift", source: 10.005, output: 20.005 + 1.0/24},
		{name: "minimum container drift", source: 10.005, output: 13},
		{name: "handle only millisecond provenance", source: 10.005, output: 20.026, handleOnly: true},
		{name: "exact upper tolerance", source: 10.005, output: 10.005 + 10.050},
		{name: "exact lower tolerance", source: 10.005, output: 10.005 + 2.950},
		{name: "over tolerance", source: 10.005, output: 20.055001, wantErr: "outside allowed range"},
		{name: "under tolerance", source: 10.005, output: 12.954999, wantErr: "outside allowed range"},
		{name: "real overrun", source: 10.005, output: 20.205, wantErr: "outside allowed range"},
		{name: "real underrun", source: 10.005, output: 12.805, wantErr: "outside allowed range"},
		{name: "not an extension", source: 10.005, output: 10.005, wantErr: "outside allowed range"},
		{name: "exact ceiling", source: 37, output: 40},
		{name: "ceiling container drift", source: 37, output: 40.005},
		{name: "ceiling tolerance", source: 37, output: 40.050},
		{name: "ceiling over tolerance", source: 37, output: 40.050001, wantErr: "ceiling"},
		{name: "real total overrun with valid delta", source: 35, output: 41, wantErr: "ceiling"},
		{name: "source ceiling remains strict", source: 37.1, output: 40.1, wantErr: "maximum allowed for Omni"},
		{name: "zero output", source: 10, output: 0, wantErr: "invalid duration"},
		{name: "negative output", source: 10, output: -1, wantErr: "invalid duration"},
		{name: "nan output", source: 10, output: math.NaN(), wantErr: "invalid duration"},
		{name: "infinite output", source: 10, output: math.Inf(1), wantErr: "invalid duration"},
		{name: "negative infinite output", source: 10, output: math.Inf(-1), wantErr: "invalid duration"},
		{name: "zero source", source: 0, output: 20, wantErr: "positive and finite"},
		{name: "negative source", source: -1, output: 20, wantErr: "positive and finite"},
		{name: "nan source", source: math.NaN(), output: 20, wantErr: "positive and finite"},
		{name: "infinite source", source: math.Inf(1), output: 20, wantErr: "positive and finite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Form exact tolerance endpoints from the same measured float rather
			// than a compile-time decimal sum with different binary rounding.
			if tc.name == "exact upper tolerance" {
				tc.output = tc.source + (10.0 + 0.050)
			} else if tc.name == "exact lower tolerance" {
				tc.output = tc.source + (3.0 - 0.050)
			}
			auth, accountID := setupTestAuthStore(t, "test-google-key", "")
			svc := NewService(auth, nil, setupTestCatalog())
			sourceBytes, outputBytes := []byte("selected-video"), []byte("combined-video")
			sourceDigest := fmt.Sprintf("%x", sha256.Sum256(sourceBytes))
			outputDigest := fmt.Sprintf("%x", sha256.Sum256(outputBytes))
			svc.SetVideoProber(fakeProber{byDigest: map[string]VideoMetadata{
				sourceDigest: {DurationSeconds: tc.source, Width: 1280, Height: 720},
				outputDigest: {DurationSeconds: tc.output, Width: 1280, Height: 720},
			}})
			credential, _, err := auth.GetActiveCredentialForAccount(accountID, "google")
			if err != nil {
				t.Fatal(err)
			}
			prov := &pebblestore.VideoProvenance{
				AccountScopeID: accountID, CredentialID: credential.ID,
				CredentialVersion: fmt.Sprintf("v%d", credential.UpdatedAt),
				Provider:          ProviderGoogleGemini, Model: omni,
				Transport:     pebblestore.VideoTransportGoogleInteractions,
				InteractionID: "retained-interaction", OutputDigestSHA256: sourceDigest,
				// Deliberately stale display/provenance duration: bytes must win.
				ObservedDurationMs: 8000, ObservedWidth: 1280, ObservedHeight: 720,
				ExtensionCountKnown: true, ExtensionCount: 2,
				SourceLink: &pebblestore.VideoSourceLink{DeliverableID: "ancestor"},
			}
			source := &ManagedVideoSource{
				Bytes: sourceBytes, MediaType: "video/mp4", Provenance: prov,
				InteractionID: prov.InteractionID,
				SourceLink:    &pebblestore.VideoSourceLink{DeliverableID: "selected"},
			}
			if tc.handleOnly {
				source.Bytes = nil
				prov.ObservedDurationMs = int64(tc.source * 1000)
			}
			before := prov.Clone()
			calls := 0
			svc.SetHTTPClient(&http.Client{Transport: privacyTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				var body struct {
					PreviousInteractionID string `json:"previous_interaction_id"`
					Input                 string `json:"input"`
					GenerationConfig      struct {
						VideoConfig struct {
							Task string `json:"task"`
						} `json:"video_config"`
					} `json:"generation_config"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/interactions") || body.PreviousInteractionID != prov.InteractionID || body.GenerationConfig.VideoConfig.Task != "" || body.Input != "Extend the scene by continuing the action" {
					t.Fatalf("not exact-source native extension: %s %s %+v", req.Method, req.URL.Path, body)
				}
				response := fmt.Sprintf(`{"id":"continued-interaction","status":"completed","model":%q,"steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","data":%q}]}]}`, omni, base64.StdEncoding.EncodeToString(outputBytes))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := svc.GenerateManagedVideo(ctx, ManagedVideoRequest{
				Principal: identity.Principal{AccountScopeID: accountID},
				Operation: "extend", Model: omni, Prompt: "Extend the scene by continuing the action", Source: source,
			})
			wantCalls := 1
			if tc.wantErr == "positive and finite" || tc.wantErr == "maximum allowed for Omni" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("provider submissions = %d, want %d", calls, wantCalls)
			}
			if !reflect.DeepEqual(prov, before) {
				t.Fatal("source provenance mutated")
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !reflect.DeepEqual(result, ManagedVideoResult{}) {
					t.Fatalf("want %q and empty unpublished result, got %+v, %v", tc.wantErr, result, err)
				}
				if tc.wantErr == "ceiling" || tc.wantErr == "outside allowed range" {
					for _, detail := range []string{fmt.Sprintf("source=%.9fs", tc.source), fmt.Sprintf("output=%.9fs", tc.output), fmt.Sprintf("delta=%.9fs", tc.output-tc.source), "tolerance 0.050000s"} {
						if !strings.Contains(err.Error(), detail) {
							t.Fatalf("missing precise diagnostic %q: %v", detail, err)
						}
					}
				}
				return
			}
			if err != nil || result.Provenance == nil {
				t.Fatalf("extension failed: %v", err)
			}
			p := result.Provenance
			if string(result.Bytes) != string(outputBytes) || result.DurationMs != int(tc.output*1000) || p.ObservedDurationMs != int64(result.DurationMs) || p.DurationSeconds != 0 {
				t.Fatalf("measured duration clamped or omitted duration changed: %+v", result)
			}
			if p.SourceLink == nil || p.SourceLink.DeliverableID != "selected" || p.SourceLink == source.SourceLink || p.OutputDigestSHA256 != outputDigest || p.InteractionID != "continued-interaction" || p.Operation != "extend" || !p.IsCombinedOutput || p.ExtensionCount != 3 {
				t.Fatalf("lost extension lineage: %+v", p)
			}
			if p.AccountScopeID != accountID || p.CredentialID != prov.CredentialID || p.CredentialVersion != prov.CredentialVersion || p.Model != omni || p.Transport != prov.Transport {
				t.Fatalf("lost authenticated source binding: %+v", p)
			}
		})
	}
}
