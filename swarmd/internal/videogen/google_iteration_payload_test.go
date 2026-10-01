package videogen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: retained Omni edits/extensions must use the exact conversation
// without mutually exclusive task fields or forbidden geometry overrides.
// Boundary: generateGoogleOmni HTTP serialization; prevents provider 400s while
// proving fresh generation retains its requested geometry. This is a payload
// regression test, not evidence of live generation or playable output.
func TestOmniIterationPayload(t *testing.T) {
	for _, op := range []string{"create", "edit", "extend"} {
		t.Run(op, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				format, _ := body["response_format"].(map[string]any)
				if op == "create" {
					if format["aspect_ratio"] != "16:9" {
						t.Error("create lost aspect ratio")
					}
					if _, exists := body["previous_interaction_id"]; exists {
						t.Error("create gained source")
					}
				} else {
					if body["previous_interaction_id"] != "retained-latest" {
						t.Error("wrong conversation source")
					}
					if _, exists := body["generation_config"]; exists {
						t.Error("retained iteration must omit task")
					}
					if _, exists := format["aspect_ratio"]; exists {
						t.Error("iteration must omit aspect ratio")
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "next", "steps": []any{map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "video", "data": base64.StdEncoding.EncodeToString([]byte("payload"))}}}}})
			}))
			defer server.Close()
			svc := &Service{}
			svc.SetBaseURLs(server.URL, "")
			var src *ManagedVideoSource
			if op != "create" {
				src = &ManagedVideoSource{InteractionID: "retained-latest", Provenance: &pebblestore.VideoProvenance{ExtensionCount: 2}}
			}
			result, err := svc.generateGoogleOmni(context.Background(), "fixture", "fixture-omni", "Continue the scene", "16:9", "720p", op, src, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.InteractionID != "next" || string(result.Bytes) != "payload" {
				t.Fatal("lost result identity/bytes")
			}
			if op == "extend" && (!result.IsCombinedOutput || result.ExtensionCount != 3) {
				t.Fatal("lost extension lineage")
			}
		})
	}
}

// Requirement: a missing retained Veo URI must fail before network dispatch;
// raw bytes alone cannot authorize a native extension.
func TestVeoExtensionMissingResourceNoDispatch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	svc := &Service{}
	svc.SetBaseURLs(server.URL, "")
	for _, src := range []*ManagedVideoSource{nil, {Bytes: []byte("video")}, {Provenance: &pebblestore.VideoProvenance{}}} {
		_, err := svc.generateGoogleVeo(context.Background(), "fixture", "fixture-veo", "continue", "16:9", "720p", 8, "extend", src, nil)
		if err == nil {
			t.Fatal("missing resource accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("dispatched %d requests", calls)
	}
}
