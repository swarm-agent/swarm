package api

import (
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	// TailscaleAppCapsHeader is set by Tailscale Serve (1.92+, started with
	// --accept-app-caps) from the caller's grants in the tailnet policy. Serve
	// drops any copy the caller sends, so only the policy can set it.
	TailscaleAppCapsHeader = "Tailscale-App-Capabilities"
	// SwarmTailnetCapability is the app capability a tailnet policy grants to
	// devices that may use Swarm Control: [{"level":"read"}] or [{"level":"write"}].
	SwarmTailnetCapability = "swarmagent.dev/cap/swarm"
)

// TailnetGatewayHandler is the AI gateway behind Tailscale Serve. A request
// with a bearer credential goes through ContainerSDKHandler unchanged. A
// request without one is admitted to Swarm Control (/mcp) only, at the level
// the tailnet policy grants the calling device, and only from Serve on this
// machine's loopback. Approve and manage are never granted this way.
func (s *Server) TailnetGatewayHandler() http.Handler {
	sdk := s.ContainerSDKHandler()
	next := s.withVaultGate(s.withJSON(s.apiMux()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
			sdk.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if s.security == nil || s.identitySessions == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("SDK authentication unavailable"))
			return
		}
		if !remoteIsLoopback(r.RemoteAddr) {
			writeError(w, http.StatusUnauthorized, errors.New("scoped bearer token required"))
			return
		}
		raw := r.Header.Get(TailscaleAppCapsHeader)
		if strings.TrimSpace(raw) == "" {
			writeError(w, http.StatusUnauthorized, errors.New("this tailnet device has no Swarm permission: grant "+SwarmTailnetCapability+" in the tailnet policy, or send an AI key"))
			return
		}
		level, err := tailnetSwarmLevel(raw)
		if err != nil || level == "" {
			writeError(w, http.StatusForbidden, errors.New("this tailnet device has no Swarm permission: grant "+SwarmTailnetCapability+" with level read or write"))
			return
		}
		if r.URL.Path != controlMCPPath {
			writeError(w, http.StatusForbidden, errors.New("tailnet permissions work only through Swarm Control (/mcp)"))
			return
		}
		actor, err := s.identitySessions.ActorForCurrentSelection()
		if err != nil || !isCompleteProductActor(actor) {
			writeError(w, http.StatusUnauthorized, errors.New("machine owner identity unavailable"))
			return
		}
		scopes, err := aiKeyScopes(level)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		who := tailnetCaller(r)
		// An in-memory record: the same levels as an AI key of this level,
		// never stored and never usable outside this request.
		rec := &pebblestore.ScopedTokenRecord{
			ID:             "tailnet:" + who,
			Name:           "tailnet " + who,
			Scopes:         scopes,
			AccountScopeID: actor.AccountScopeID,
			UserID:         actor.UserID,
		}
		log.Printf("swarmd tailnet gateway: %s level=%s", who, level)
		s.serveControlMCP(w, requestWithScopedToken(requestWithActorContext(r, actor), rec), next)
	})
}

// tailnetSwarmLevel reads the highest Swarm level (write over read) that the
// policy grants in a Tailscale-App-Capabilities header. Other capabilities and
// unknown levels are ignored; approve and manage are never honored.
func tailnetSwarmLevel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "=?") {
		decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
		if err != nil {
			return "", err
		}
		raw = decoded
	}
	var caps map[string][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return "", err
	}
	level := ""
	for _, value := range caps[SwarmTailnetCapability] {
		var grant struct {
			Level string `json:"level"`
		}
		if json.Unmarshal(value, &grant) != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(grant.Level)) {
		case "write":
			level = "write"
		case "read":
			if level == "" {
				level = "read"
			}
		}
	}
	return level, nil
}

// tailnetCaller names the caller for the log: the Tailscale login of a
// person's device, else the device's tailnet address. Both are set by Serve.
func tailnetCaller(r *http.Request) string {
	if login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); login != "" {
		return login
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
		return forwarded
	}
	return "unknown-device"
}

func remoteIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
