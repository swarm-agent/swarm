package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestChromedpRendererCapturesStableStateWithSystemChrome(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}

	html := []byte(`<!doctype html>
<html lang="en" data-swarm-capture-state="opening">
<head>
<meta charset="utf-8">
<script>
globalThis.__SWARM_CAPTURE_V1__ = {
  version: "swarm.capture/v1",
  select(stateId) {
    if (stateId !== "opening") throw new Error("unknown state");
    document.documentElement.dataset.swarmCaptureState = stateId;
  },
  ready(stateId) {
    if (document.documentElement.dataset.swarmCaptureState !== stateId) throw new Error("state mismatch");
    return {state_id: stateId};
  }
};
</script>
<style>
html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#08111f;color:#fff}
body{display:grid;place-items:center;font:700 72px system-ui}
</style>
</head>
<body>Sandboxed system Chrome</body>
</html>`)

	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	var browserOutput strings.Builder
	renderer.browserOutput = &browserOutput
	results, err := renderer.Capture(context.Background(), Request{
		Entry:    "index.html",
		Files:    map[string][]byte{"index.html": html},
		StateIDs: []string{"opening"},
	})
	if err != nil {
		t.Fatalf("Capture: %v; cause: %v; browser output: %s", err, errors.Unwrap(err), browserOutput.String())
	}
	if len(results) != 1 || results[0].StateID != "opening" {
		t.Fatalf("results = %+v", results)
	}
	config, err := png.DecodeConfig(bytes.NewReader(results[0].PNG))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if config.Width != Width || config.Height != Height {
		t.Fatalf("PNG dimensions = %dx%d, want %dx%d", config.Width, config.Height, Width, Height)
	}
}
