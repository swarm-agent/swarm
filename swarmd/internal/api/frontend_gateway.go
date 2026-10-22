package api

import (
	"net/http"
	"strings"

	"swarm-refactor/swarmtui/pkg/buildinfo"
)

// Frontends (a console, a dashboard, an AI client) find and follow Swarm
// through the AI gateway: 127.0.0.1:7783 on the machine, and the tailnet
// gateway when it is served there. Two things on that gateway are for them
// (docs/frontends.md):
//
//   - GET /v3/hello answers without a key, so an app can tell that Swarm runs
//     here, which build and API, how to authenticate and whether the machine
//     has an owner yet, before it holds any key. It carries no secrets,
//     identities, paths or counts.
//   - AI keys (and app keys, which are AI keys with their own file) may read
//     the activity feed, GET /v3/activity and /v3/activity/stream, besides
//     Swarm Control (/mcp). The feed is metadata only (activity.go) and still
//     requires sessions:read, so a read key qualifies and nothing new is
//     reachable from it; it lets a frontend stay current without polling tools.
const helloPath = "/v3/hello"

// helloFeatures names what this build offers a frontend, so a client can check
// for a capability instead of comparing versions.
var helloFeatures = []string{"hello", "control_mcp", "activity_feed", "app_keys"}

func isHelloRequest(r *http.Request) bool {
	return r.URL.Path == helloPath && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

func (s *Server) serveHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	owner := false
	if s != nil && s.identityService != nil {
		if summary, err := s.identityService.StateSummary(); err == nil && summary.CurrentUser != nil {
			owner = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"product":  "swarm",
		"api":      "v3",
		"version":  strings.TrimSpace(buildinfo.Version),
		"commit":   strings.TrimSpace(buildinfo.Commit),
		"owner":    owner,
		"features": helloFeatures,
		"control":  map[string]any{"mcp": controlMCPPath, "activity": "/v3/activity", "activity_stream": "/v3/activity/stream"},
		"auth":     map[string]any{"scheme": "bearer", "key_prefix": "swk_"},
	})
}

// aiKeyFeedRoute lists the read-only routes an AI key reaches besides /mcp.
func aiKeyFeedRoute(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	return r.URL.Path == "/v3/activity" || r.URL.Path == "/v3/activity/stream"
}
