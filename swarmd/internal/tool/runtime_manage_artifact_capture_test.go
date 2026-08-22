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
	for _, required := range []string{`"action":"export_html_stills"`, `"state_id":"opening"`, `"state_id":"proof"`, `"media_type":"image/png"`, `"count":2`} {
		if !strings.Contains(output, required) {
			t.Fatalf("output lacks %s: %s", required, output)
		}
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
