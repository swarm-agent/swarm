package tool

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/htmlcapture"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeHTMLCaptureRenderer struct {
	req htmlcapture.Request
	err error
}

func (f *fakeHTMLCaptureRenderer) Capture(_ context.Context, req htmlcapture.Request) ([]htmlcapture.Result, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	results := make([]htmlcapture.Result, 0, len(req.StateIDs))
	for _, stateID := range req.StateIDs {
		results = append(results, htmlcapture.Result{StateID: stateID, PNG: testCapturePNG()})
	}
	return results, nil
}

func testCapturePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, htmlcapture.Width, htmlcapture.Height))
	for y := 0; y < htmlcapture.Height; y++ {
		for x := 0; x < htmlcapture.Width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 12, G: 34, B: 56, A: 255})
		}
	}
	var output bytes.Buffer
	_ = png.Encode(&output, img)
	return output.Bytes()
}

func TestExportHTMLStillsUsesExactReferenceManifestOrderAndLineage(t *testing.T) {
	html := `<!doctype html><script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"opening"},{"id":"proof"}]}</script><body></body>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}}
	renderer := &fakeHTMLCaptureRenderer{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLCaptureRenderer(renderer)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "capture-call", map[string]any{"action": "export_html_stills", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 7, "state_ids": []any{"proof", "opening"}})
	if err != nil {
		t.Fatalf("export_html_stills: %v", err)
	}
	if strings.Join(renderer.req.StateIDs, ",") != "opening,proof" {
		t.Fatalf("state order = %v", renderer.req.StateIDs)
	}
	if authority.created.SourceSessionID != "source-session" || authority.created.SourceCollectionID != "source-collection" || authority.created.SourceVariantID != "source-variant" || authority.created.SourceEventSeq != 7 {
		t.Fatalf("lineage = %+v", authority.created)
	}
	if authority.created.OutputRequirements == nil || authority.created.OutputRequirements.Width != 1920 || authority.created.OutputRequirements.Height != 1080 {
		t.Fatalf("requirements = %+v", authority.created.OutputRequirements)
	}
	if authority.created.AutoAccept {
		t.Fatalf("multi-state still export unexpectedly requested auto-accept: %+v", authority.created)
	}
	for _, required := range []string{`"action":"export_html_stills"`, `"state_id":"opening"`, `"state_id":"proof"`, `"media_type":"image/png"`, `"count":2`} {
		if !strings.Contains(output, required) {
			t.Fatalf("output lacks %s: %s", required, output)
		}
	}

	authority.readBody = []byte(html)
	authority.variant = pebblestore.SessionArtifactVariant{ID: "source-variant", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}
	if _, err := runtime.executeManageArtifact(ctx, scope, "capture-single", map[string]any{"action": "export_html_stills", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source-variant", "event_seq": 7, "state_ids": []any{"opening"}}); err != nil {
		t.Fatalf("single-state export_html_stills: %v", err)
	}
	if !authority.created.AutoAccept {
		t.Fatalf("single-state still export did not request auto-accept: %+v", authority.created)
	}
}

func TestExportHTMLStillsReturnsExactStoryboardHandoffAndRequiresCompleteCaptures(t *testing.T) {
	html := `<!doctype html><script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"opening"},{"id":"proof"}]}</script><script id="swarm-storyboard-manifest" type="application/json">{"version":"swarm.storyboard/v1","sections":[{"id":"intro","capture_state_id":"opening","title":"Intro","duration_ms":2500,"narration":"Meet Swarm.","on_screen_text":"Local-first","creative_direction":"Slow push.","filming_requirements":["Locked camera"],"production_state":"pending"},{"id":"proof","capture_state_id":"proof","title":"Proof","duration_ms":3000,"creative_direction":"Over shoulder.","filming_requirements":["Readable screen"],"production_state":"ready"}]}</script><body></body>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}}
	renderer := &fakeHTMLCaptureRenderer{}
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(authority)
	runtime.SetHTMLCaptureRenderer(renderer)
	ctx, scope := artifactToolContext()
	output, err := runtime.executeManageArtifact(ctx, scope, "storyboard", map[string]any{"action": "export_html_stills", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source", "event_seq": 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"storyboard_handoff"`, `"compositions":null`, `"composition":null`, `"id":"intro"`, `"capture_state_id":"opening"`, `"duration_ms":2500`, `"creative_direction":"Slow push."`, `"production_state":"pending"`, `"variant_id":"variant-`} {
		if !strings.Contains(output, want) {
			t.Fatalf("storyboard output lacks %s: %s", want, output)
		}
	}
	authority.readBody = []byte(html)
	authority.variant = pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "source-collection", SessionID: "source-session", EventSeq: 7, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}
	_, err = runtime.executeManageArtifact(ctx, scope, "storyboard-incomplete", map[string]any{"action": "export_html_stills", "session_id": "source-session", "collection_id": "source-collection", "variant_id": "source", "event_seq": 7, "state_ids": []any{"opening"}})
	if err == nil || !strings.Contains(err.Error(), "storyboard_export_incomplete") {
		t.Fatalf("incomplete storyboard export error = %v", err)
	}
	if strings.Join(renderer.req.StateIDs, ",") != "opening,proof" {
		t.Fatalf("renderer should not run for incomplete storyboard request: %+v", renderer.req.StateIDs)
	}
}

func TestCaptureManifestAcceptsCanonicalAttributesInEitherOrder(t *testing.T) {
	html := []byte(`<script type="application/json" id="swarm-capture-manifest">{"version":"swarm.capture/v1","states":[{"id":"opening"}]}</script>`)
	manifest, err := parseCaptureManifest(html)
	if err != nil || len(manifest.States) != 1 || manifest.States[0].ID != "opening" {
		t.Fatalf("manifest = %+v, err = %v", manifest, err)
	}
}

func TestExportHTMLStillsRejectsIncompleteReferenceAndUnknownStateBeforeRenderer(t *testing.T) {
	runtime := NewRuntime(1)
	runtime.SetArtifactAuthority(&fakeArtifactAuthority{})
	renderer := &fakeHTMLCaptureRenderer{}
	runtime.SetHTMLCaptureRenderer(renderer)
	ctx, scope := artifactToolContext()
	if _, err := runtime.executeManageArtifact(ctx, scope, "capture-incomplete", map[string]any{"action": "export_html_stills", "variant_id": "source"}); err == nil || !strings.Contains(err.Error(), "capture_source_reference_invalid") {
		t.Fatalf("incomplete reference error = %v", err)
	}
	html := `<script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"opening"}]}</script><body></body>`
	authority := &fakeArtifactAuthority{readBody: []byte(html), variant: pebblestore.SessionArtifactVariant{ID: "source", CollectionID: "collection", SessionID: "source-session", EventSeq: 9, Status: pebblestore.SessionArtifactStatusReady, MediaType: "text/html"}}
	runtime.SetArtifactAuthority(authority)
	_, err := runtime.executeManageArtifact(ctx, scope, "capture-unknown", map[string]any{"action": "export_html_stills", "session_id": "source-session", "collection_id": "collection", "variant_id": "source", "event_seq": 9, "state_ids": []any{"missing"}})
	if err == nil || !strings.Contains(err.Error(), "capture_state_unknown") {
		t.Fatalf("unknown state error = %v", err)
	}
	if len(renderer.req.StateIDs) != 0 {
		t.Fatalf("renderer called for invalid state: %+v", renderer.req)
	}
}
