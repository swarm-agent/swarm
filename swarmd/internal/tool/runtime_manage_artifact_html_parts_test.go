package tool

import "testing"

func TestDeriveArtifactHTMLPartsFromAuthoredStructureWithoutSplittingBytes(t *testing.T) {
	html := []byte(`<!doctype html><html><body>
<script id="swarm-iteration-manifest" type="application/json">{"version":"swarm.iteration/v1","duration_ms":2000,"sections":[{"id":"opening","label":"Opening","start_ms":0,"end_ms":1000},{"id":"proof","label":"Proof","start_ms":1000,"end_ms":2000}]}</script>
<script id="swarm-capture-manifest" type="application/json">{"version":"swarm.capture/v1","states":[{"id":"poster","label":"Poster"}]}</script>
<header id="hero" aria-label="Hero message"></header><main id="product-proof"></main>
</body></html>`)
	parts := deriveArtifactHTMLParts(html, "text/html; charset=utf-8")
	if len(parts) != 5 {
		t.Fatalf("derived parts = %#v", parts)
	}
	want := map[string]string{"opening": "temporal", "proof": "temporal", "poster": "state", "hero": "selector", "product-proof": "selector"}
	for _, part := range parts {
		if want[part.ID] != part.Kind {
			t.Fatalf("part %q = %#v", part.ID, part)
		}
		delete(want, part.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing derived parts: %#v", want)
	}
}

func TestDeriveArtifactHTMLPartsRejectsUnstableOrDuplicateRegionIDs(t *testing.T) {
	html := []byte(`<main id="Hero"></main><section id="hero"></section><footer id="hero"></footer><section></section>`)
	parts := deriveArtifactHTMLParts(html, "text/html")
	if len(parts) != 1 || parts[0].ID != "hero" || parts[0].Selector != "#hero" {
		t.Fatalf("derived parts = %#v", parts)
	}
	if got := deriveArtifactHTMLParts(html, "text/plain"); got != nil {
		t.Fatalf("non-HTML parts = %#v", got)
	}
}
