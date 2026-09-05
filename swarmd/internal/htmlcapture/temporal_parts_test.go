package htmlcapture

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Requirement: each temporal scene is visible at its sampled state; hidden or
// clipped required subjects and unstable author code still reject publication.
// This is a bounded real-browser test, deliberately outside hermetic tiers.
func TestTemporalPartsCapturePreservesVisibilityAndStability(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skip("system-managed Chrome unavailable")
	}
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	html := `<!doctype html><html><head><style>html,body{margin:0;background:#08111f;overflow:hidden}.scene{position:absolute;left:80px;top:80px;width:220px;height:160px;background:#87ceeb}.scene[hidden]{display:none}</style></head><body><section id="one" class="scene">One</section><section id="two" class="scene" hidden>Two</section><script>globalThis.__SWARM_CAPTURE_V1__={version:"swarm.capture/v1",select:async id=>{document.getElementById('one').hidden=id!=='one';document.getElementById('two').hidden=id!=='two';document.documentElement.dataset.swarmCaptureState=id},ready:async id=>({state_id:id})}</script></body></html>`
	for _, mode := range []string{"valid", "hidden", "clipped", "unstable"} {
		t.Run(mode, func(t *testing.T) {
			body := html
			if mode == "clipped" {
				body += `<style>#two{left:-40px}</style>`
			}
			if mode == "unstable" {
				body += `<script>let n=0;setInterval(()=>document.getElementById('one').style.width=(220+(++n%30))+'px',5)</script>`
			}
			selectors := map[string][]string{"one": {"#one"}, "two": {"#two"}}
			if mode == "hidden" {
				selectors["one"] = []string{"#two"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := renderer.Capture(ctx, Request{Entry: "index.html", Files: map[string][]byte{"index.html": []byte(body)}, StateIDs: []string{"one", "two"}, StateRequiredSelectors: selectors, TemporalStates: true, ViewportWidth: 1440, ViewportHeight: 900})
			if mode == "valid" {
				if err != nil || len(result) != 2 {
					t.Fatalf("valid states failed: %v", err)
				}
				if same, e := equalPixels(result[0].PNG, result[1].PNG, 1440, 900); e != nil || same {
					t.Fatal("distinct state pixels missing")
				}
				return
			}
			var failure *Error
			expected := map[string]string{"hidden": "capture_required_element_missing", "clipped": "capture_required_element_clipped", "unstable": "capture_state_unstable"}[mode]
			if !errors.As(err, &failure) || failure.Code != expected || len(result) != 0 {
				t.Fatalf("%s not rejected atomically: %v", mode, err)
			}
		})
	}
}
