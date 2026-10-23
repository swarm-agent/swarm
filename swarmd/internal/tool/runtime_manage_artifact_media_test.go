package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
	"testing"
)

type mediaImageSettings struct{ manageThemeUISettingsService }

func (mediaImageSettings) GetForAccount(string) (uisettings.UISettings, error) {
	var settings uisettings.UISettings
	settings.Tools.Image.DefaultModel = "gemini-nano-banana-2.1"
	return settings, nil
}

type mediaImageGenerator struct {
	ManagedImageGenerationService
	image imagegen.ManagedImage
}

func (g mediaImageGenerator) GenerateManagedImage(context.Context, imagegen.ManagedGenerateRequest) (imagegen.ManagedImage, error) {
	return g.image, nil
}

type mediaImageAuthority struct{ ArtifactAuthority }

func (mediaImageAuthority) Create(_ context.Context, _ artifact.Principal, in artifact.CreateInput) (pebblestore.SessionArtifactVariant, error) {
	return pebblestore.SessionArtifactVariant{ID: in.VariantID}, nil
}

// Purpose: generateManagedImageArtifact must persist the image provider's
// measured tokens, or the selected size fallback, using standard catalog lines
// rather than cheaper batch lines. This runtime boundary with real Pebble
// pricing/receipts is the narrowest test of manage_artifact's billing wiring.
func TestManageArtifactImageMediaUsage(t *testing.T) {
	for _, tc := range []struct {
		name, size string
		tokens     int
		want       float64
	}{{"tokens", "1K", 1120, .0336}, {"size", "4K", 0, .113}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := pebblestore.NewSessionStore(db)
			if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session", Title: "media"}, "user", "account"); err != nil {
				t.Fatal(err)
			}
			pricing := json.RawMessage(`{"billing":{"status":"verified","lines":[{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":30,"conditions":{"tier":"paid","service_tier":"standard"}},{"kind":"billing_rate","billable":"image_output","unit":"million_tokens","price_usd":15,"conditions":{"tier":"paid","service_tier":"batch"}},{"kind":"equivalent_cost","billable":"image_output","unit":"image","variant":"1K","price_usd":0.0336,"conditions":{"tier":"paid","service_tier":"standard","output_tokens":1120}},{"kind":"equivalent_cost","billable":"image_output","unit":"image","variant":"2K","price_usd":0.067,"conditions":{"tier":"paid","service_tier":"standard","output_tokens":2240}},{"kind":"equivalent_cost","billable":"image_output","unit":"image","variant":"4K","price_usd":0.113,"conditions":{"tier":"paid","service_tier":"standard","output_tokens":3767}},{"kind":"equivalent_cost","billable":"image_output","unit":"image","variant":"4K","price_usd":0.0565,"conditions":{"tier":"paid","service_tier":"batch","output_tokens":3767}}]}}`)
			if err := pebblestore.NewModelCatalogStore(db).SetRecord(pebblestore.ModelCatalogRecord{Provider: "google", Model: "gemini-nano-banana-2.1", Pricing: pricing}); err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			r := NewRuntime(1)
			r.sessions = session.NewService(store, nil)
			r.uiSettings = mediaImageSettings{}
			r.artifactAuthority = mediaImageAuthority{}
			r.imageGeneration = mediaImageGenerator{image: imagegen.ManagedImage{Bytes: body.Bytes(), MediaType: "image/png", OutputTokens: int64(tc.tokens)}}
			p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user", SessionID: "session"}
			_, err = r.generateManagedImageArtifact(context.Background(), WorkspaceScope{SessionID: "session", Principal: p}, artifact.Principal{AccountScopeID: "account", UserID: "user", SessionID: "session"}, "call", "request", map[string]any{"prompt": "image", "capability_token": "fixture", "image_settings": map[string]any{"image_size": tc.size}})
			if err != nil {
				t.Fatal(err)
			}
			receipts, err := store.ListMediaUsage("account", 10)
			if err != nil || len(receipts) != 1 {
				t.Fatalf("receipts %+v %v", receipts, err)
			}
			if receipts[0].PriceStatus != "known" || math.Abs(receipts[0].CostUSD-tc.want) > 1e-10 {
				t.Fatalf("receipt %+v", receipts[0])
			}
		})
	}
}
