package htmlcapture

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Requirement: native static sections are reachable through ordinary vertical
// document/nested scrolling, while missing/hidden/clipped content cannot publish.
// Capture and captureState own the gate. The real sandboxed browser is the
// narrowest layer proving layout, scroll reachability and nonempty stable pixels;
// these tests are outside hermetic tiers and never relax the browser sandbox.
func TestDocumentSectionCapture(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skip("system-managed Chrome unavailable")
	}
	for _, tc := range []struct {
		name, css, body, selector, code string
		minTiles                        int
	}{
		{"below_fold", "", `<div style="height:1200px"></div><section id="part">Below fold</section>`, "#part", "", 1},
		{"long_section", "", `<section id="part" style="height:2200px;background:linear-gradient(red,blue)">Long</section>`, "#part", "", 3},
		{"oversized_main", "", `<main id="part" style="height:1800px">Long main</main>`, "#part", "", 2},
		{"nested_scroll", "", `<div style="height:300px;overflow:auto"><div style="height:700px"></div><section id="part" style="height:650px;background:linear-gradient(red,blue)">Nested</section></div>`, "#part", "", 3},
		{"scroll_section", "", `<section id="part" style="height:300px;overflow:auto"><div style="height:750px;background:linear-gradient(red,blue)">Scrollable</div></section>`, "#part", "", 3},
		{"missing", "", `<section>Missing</section>`, "#part", "capture_required_element_missing", 0},
		{"hidden", "", `<section id="part" hidden>Hidden</section>`, "#part", "capture_required_element_missing", 0},
		{"hidden_parent", "", `<div style="opacity:0"><section id="part">Hidden</section></div>`, "#part", "capture_required_element_missing", 0},
		{"clipped_parent", "", `<div style="height:100px;overflow:hidden"><section id="part" style="height:400px">Clipped</section></div>`, "#part", "capture_required_element_clipped", 0},
		{"clipped_descendant", "", `<section id="part"><div style="height:100px;overflow:hidden"><div style="height:400px">Clipped</div></div></section>`, "#part", "capture_required_element_clipped", 0},
		{"horizontal_overflow", "", `<section id="part" style="width:1600px">Too wide</section>`, "#part", "capture_viewport_overflow", 0},
		{"unreachable_fixed", "html,body{height:900px;overflow:hidden}", `<section id="part" style="position:fixed;top:880px;height:40px">Clipped</section>`, "#part", "capture_required_element_clipped", 0},
		{"ordinary_frame", "html,body{height:900px;overflow:hidden}", `<section id="part" style="height:300px">Frame</section>`, "#part", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html := `<!doctype html><html><head><style>html,body{margin:0;background:white}section{min-height:40px}` + tc.css + `</style></head><body>` + tc.body + `<script>globalThis.__SWARM_CAPTURE_V1__={version:"swarm.capture/v1",select:async id=>{document.documentElement.dataset.swarmCaptureState=id},ready:async id=>({state_id:id})}</script></body></html>`
			files := map[string][]byte{"index.html": []byte(html)}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			results, err := NewChromedpRenderer(SystemChromePath, t.TempDir()).Capture(ctx, Request{Entry: "index.html", Files: files, StateIDs: []string{"section"}, StateRequiredSelectors: map[string][]string{"section": {tc.selector}}, DocumentSections: true, ViewportWidth: 1440, ViewportHeight: 900})
			if string(files["index.html"]) != html {
				t.Fatal("capture mutated immutable source")
			}
			if tc.code != "" {
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != tc.code || len(results) != 0 || !strings.Contains(failure.SafeMessage, "section") {
					t.Fatalf("expected atomic %s rejection with state: results=%d error=%v", tc.code, len(results), err)
				}
				return
			}
			if err != nil || len(results) != 1 || len(results[0].PNG) == 0 || len(results[0].SectionDigests) < tc.minTiles {
				t.Fatalf("section evidence missing: result count=%d error=%v", len(results), err)
			}
		})
	}
}

// Requirement: document tile exhaustion returns no partial evidence; the narrow
// helper layer proves the budget is enforced before any browser work is started.
func TestDocumentSectionTileBudget(t *testing.T) {
	budget := 0
	png, digests, err := captureDocumentSection(context.Background(), "part", 1440, 900, []string{"#part"}, &budget)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "capture_source_limit_exceeded" || png != nil || digests != nil {
		t.Fatalf("exhausted capture returned evidence: %v", err)
	}
}
