package htmlcapture

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Requirement: captureState may exclude explicitly marked retained controls,
// never missing/unmarked required output. Browser capture is the narrowest
// layer proving selector resolution, removal order and viewport safety.
func TestCaptureRetainedExplicitUI(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skip("Chrome required")
	}
	for _, tc := range []struct{ name, nav, output, want string }{
		{"marked", `<nav id="controls" data-swarm-capture-ui>Controls</nav>`, `<main id="output">Output</main>`, ""},
		{"nested", `<aside data-swarm-capture-ui><nav id="controls">Controls</nav></aside>`, `<main id="output">Output</main>`, ""},
		{"ordinary-nav", `<nav id="controls">Controls</nav>`, `<main id="output">Output</main>`, "capture_required_element_clipped"},
		{"missing-output", `<nav id="controls" data-swarm-capture-ui>Controls</nav>`, "", "capture_required_element_missing"},
		{"hidden-output", `<nav id="controls" data-swarm-capture-ui>Controls</nav>`, `<main id="output" style="display:none">Output</main>`, "capture_required_element_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`<html><head><style>html,body{margin:0;overflow:hidden}nav{position:fixed;left:-50px;top:0;width:100px;height:40px}main{height:100px}</style><script>globalThis.__SWARM_CAPTURE_V1__={version:"swarm.capture/v1",select:id=>{document.documentElement.dataset.swarmCaptureState=id},ready:id=>({state_id:id})}</script></head><body>` + tc.nav + tc.output + `</body></html>`)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := NewChromedpRenderer(SystemChromePath, t.TempDir()).Capture(ctx, Request{Entry: "index.html", Files: map[string][]byte{"index.html": body}, StateIDs: []string{"default", "second"}, RequiredSelectors: []string{"#controls", "#output"}})
			if tc.want == "" {
				if err != nil || len(result) != 2 {
					t.Fatalf("capture: %v", err)
				}
				return
			}
			var captureErr *Error
			if !errors.As(err, &captureErr) || captureErr.Code != tc.want || len(result) != 0 {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}
