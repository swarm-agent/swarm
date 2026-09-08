package htmlcapture

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Requirement: serveFiles must allow ES module CORS from the opaque sandbox
// while retaining the private token, no network connections, and no same-origin
// privileges. HTTP is the narrowest layer proving these serving postconditions;
// a browser test is still required to prove actual WebGL rendering.
func TestCaptureModuleServerOpaqueOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origin, stop, err := serveFiles(ctx, map[string][]byte{"index.html": []byte("<html></html>"), "runtime/three.module.js": []byte("export {}")})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	client := &http.Client{Timeout: 2 * time.Second}
	for _, name := range []string{"index.html", "runtime/three.module.js"} {
		req, _ := http.NewRequest(http.MethodGet, origin+"/"+name, nil)
		req.Header.Set("Origin", "null")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		csp := response.Header.Get("Content-Security-Policy")
		if response.StatusCode != 200 || !strings.Contains(csp, origin+"/") || !strings.Contains(csp, "connect-src 'none'") || strings.Contains(csp, "allow-same-origin") {
			t.Fatal("sandbox/token boundary lost")
		}
		if strings.HasSuffix(name, ".js") && (response.Header.Get("Access-Control-Allow-Origin") != "*" || response.Header.Get("X-Content-Type-Options") != "nosniff") {
			t.Fatal("opaque-origin module contract absent")
		}
	}
	response, err := client.Get(origin + "/runtime/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("missing dependency silently substituted")
	}
}
