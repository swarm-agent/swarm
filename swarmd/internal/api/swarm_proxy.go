package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gorillaws "github.com/gorilla/websocket"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	peerAuthSwarmIDHeader = "X-Swarm-Peer-ID"
	peerAuthTokenHeader   = "X-Swarm-Peer-Token"
)

func (s *Server) currentRemoteSwarmTargetForRequest(r *http.Request) (*swarmTarget, error) {
	_, currentTarget, err := s.swarmTargetsForRequestWithOptions(r, true)
	if err != nil {
		return nil, err
	}
	if currentTarget == nil || strings.EqualFold(strings.TrimSpace(currentTarget.Relationship), "self") {
		return nil, nil
	}
	if strings.TrimSpace(currentTarget.BackendURL) == "" {
		return nil, errors.New("selected swarm target is missing backend_url")
	}
	return currentTarget, nil
}

func (s *Server) proxyRequestToSwarmTarget(w http.ResponseWriter, r *http.Request, target swarmTarget) error {
	if isWebsocketUpgradeRequest(r) {
		return s.proxyWebsocketToSwarmTarget(w, r, target)
	}
	startedAt := time.Now()
	if s.swarm == nil {
		return errors.New("swarm service not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, target)
	if err != nil {
		return err
	}
	endpoint, err := cloneURLWithQuery(strings.TrimRight(s.proxyBackendURLForTarget(target), "/")+r.URL.Path, r.URL.Query())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint, r.Body)
	if err != nil {
		return err
	}
	req.Header = cloneHeaderExcludingAuth(r.Header)
	if strings.TrimSpace(req.Header.Get("Accept")) == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
	if principal, ok := PrincipalFromRequest(r); ok && principal.Valid() {
		req.Header.Set("X-Swarm-Principal-User-ID", strings.TrimSpace(principal.UserID))
		req.Header.Set("X-Swarm-Principal-Account-Scope-ID", strings.TrimSpace(principal.AccountScopeID))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logSwarmProxyTiming(r, target, 0, startedAt, err)
		return err
	}
	defer resp.Body.Close()
	copyProxyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	logSwarmProxyTiming(r, target, resp.StatusCode, startedAt, err)
	return err
}

func (s *Server) proxyWebsocketToSwarmTarget(w http.ResponseWriter, r *http.Request, target swarmTarget) error {
	if s.swarm == nil {
		return errors.New("swarm service not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, target)
	if err != nil {
		return err
	}
	endpoint, err := cloneURLWithQuery(strings.TrimRight(s.proxyBackendURLForTarget(target), "/")+r.URL.Path, r.URL.Query())
	if err != nil {
		return err
	}
	wsEndpoint, err := websocketEndpointForBackend(endpoint)
	if err != nil {
		return err
	}
	headers := cloneHeaderForUpstreamWebsocket(r.Header)
	headers.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	headers.Set(peerAuthTokenHeader, peerToken)
	upstream, resp, err := gorillaws.DefaultDialer.DialContext(r.Context(), wsEndpoint, headers)
	if err != nil {
		return summarizeWebsocketDialError(err, resp)
	}
	defer upstream.Close()
	downstream, err := transportws.Accept(w, r)
	if err != nil {
		return err
	}
	defer downstream.Close()
	bridgeWebsocketText(downstream, upstream)
	return nil
}

func (s *Server) proxyBackendURLForTarget(target swarmTarget) string {
	backendURL := strings.TrimSpace(target.BackendURL)
	if backendURL == "" || !isLoopbackBackendURL(backendURL) {
		return backendURL
	}
	hostSwarmID := strings.TrimSpace(target.HostSwarmID)
	if hostSwarmID == "" && strings.EqualFold(strings.TrimSpace(target.Kind), "mirrored") {
		hostSwarmID = s.ownerHostSwarmIDForTarget(target)
	}
	if hostSwarmID == "" || s.isLocalSwarmID(hostSwarmID) {
		return backendURL
	}
	if ownerBackendURL := s.backendURLForSwarmID(hostSwarmID); ownerBackendURL != "" {
		return ownerBackendURL
	}
	return backendURL
}

func (s *Server) isLocalSwarmID(swarmID string) bool {
	swarmID = strings.TrimSpace(swarmID)
	if s == nil || s.swarm == nil || swarmID == "" {
		return false
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return false
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(state.Node.SwarmID), swarmID)
}

func isLoopbackBackendURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func websocketEndpointForBackend(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported backend websocket scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}

func summarizeWebsocketDialError(err error, resp *http.Response) error {
	if resp == nil {
		return err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil || len(body) == 0 {
		return fmt.Errorf("upstream websocket dial failed: %s", resp.Status)
	}
	return fmt.Errorf("upstream websocket dial failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func bridgeWebsocketText(downstream *transportws.Conn, upstream *gorillaws.Conn) {
	bridgeWebsocketTextWithUpstreamObserver(downstream, upstream, nil)
}

func bridgeWebsocketTextWithUpstreamObserver(downstream *transportws.Conn, upstream *gorillaws.Conn, upstreamObserver func([]byte)) {
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = downstream.WriteClose()
			_ = downstream.Close()
			_ = upstream.WriteMessage(gorillaws.CloseMessage, gorillaws.FormatCloseMessage(gorillaws.CloseNormalClosure, ""))
			_ = upstream.Close()
		})
	}
	errCh := make(chan error, 2)
	go func() {
		for {
			payload, err := downstream.ReadText()
			if err != nil {
				errCh <- err
				return
			}
			if err := upstream.WriteMessage(gorillaws.TextMessage, payload); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, payload, err := upstream.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if messageType != gorillaws.TextMessage {
				continue
			}
			if upstreamObserver != nil {
				upstreamObserver(payload)
			}
			if err := downstream.WriteText(payload); err != nil {
				errCh <- err
				return
			}
		}
	}()
	<-errCh
	closeBoth()
}

func (s *Server) outgoingPeerAuthTokenForTarget(r *http.Request, target swarmTarget) (string, error) {
	_ = r
	if s.swarm == nil {
		return "", errors.New("swarm service not configured")
	}
	peerSwarmID := s.peerAuthSwarmIDForTarget(target)
	token, ok, err := s.swarm.OutgoingPeerAuthToken(peerSwarmID)
	if err != nil {
		return "", err
	}
	if ok {
		return token, nil
	}
	return "", fmt.Errorf("selected swarm target %q is missing peer auth", strings.TrimSpace(target.SwarmID))
}

func (s *Server) peerAuthSwarmIDForTarget(target swarmTarget) string {
	if hostSwarmID := strings.TrimSpace(target.HostSwarmID); hostSwarmID != "" && !s.isLocalSwarmID(hostSwarmID) {
		return hostSwarmID
	}
	if strings.EqualFold(strings.TrimSpace(target.Kind), "mirrored") {
		if hostSwarmID := s.ownerHostSwarmIDForTarget(target); hostSwarmID != "" && !s.isLocalSwarmID(hostSwarmID) {
			return hostSwarmID
		}
	}
	return strings.TrimSpace(target.SwarmID)
}

func (s *Server) ownerHostBackendURLForTarget(target swarmTarget) string {
	hostSwarmID := strings.TrimSpace(target.HostSwarmID)
	if hostSwarmID == "" {
		hostSwarmID = s.ownerHostSwarmIDForTarget(target)
	}
	return s.backendURLForSwarmID(hostSwarmID)
}

func (s *Server) backendURLForSwarmID(swarmID string) string {
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" || s == nil {
		return ""
	}
	if s.swarm != nil {
		if cfg, err := s.loadStartupConfig(); err == nil {
			if state, err := s.currentSwarmState(cfg); err == nil {
				for _, target := range listTrustedPeerTargets(state.TrustedPeers) {
					if strings.EqualFold(strings.TrimSpace(target.SwarmID), swarmID) && strings.TrimSpace(target.BackendURL) != "" {
						return strings.TrimSpace(target.BackendURL)
					}
				}
			}
		}
	}
	if s.topology != nil {
		if runtimeRecord, ok, err := s.topology.GetRuntime(swarmID); err == nil && ok {
			if backendURL := strings.TrimSpace(runtimeRecord.BackendURL); backendURL != "" {
				return backendURL
			}
		}
	}
	return ""
}

func (s *Server) ownerHostSwarmIDForTarget(target swarmTarget) string {
	swarmID := strings.TrimSpace(target.SwarmID)
	if swarmID == "" || s == nil {
		return ""
	}
	if s.topology != nil {
		if runtimeRecord, ok, err := s.topology.GetRuntime(swarmID); err == nil && ok {
			if hostSwarmID := strings.TrimSpace(runtimeRecord.OwnerHostSwarmID); hostSwarmID != "" {
				return hostSwarmID
			}
		}
		if _, err := s.topology.EnsureSnapshot(); err == nil {
			if runtimeRecord, ok, err := s.topology.GetRuntime(swarmID); err == nil && ok {
				if hostSwarmID := strings.TrimSpace(runtimeRecord.OwnerHostSwarmID); hostSwarmID != "" {
					return hostSwarmID
				}
			}
		}
	}
	if s.deployContainers == nil {
		return ""
	}
	deployments, err := s.deployContainers.List(context.Background())
	if err != nil {
		return ""
	}
	for _, deployment := range deployments {
		if strings.EqualFold(strings.TrimSpace(deployment.ChildSwarmID), swarmID) {
			return strings.TrimSpace(deployment.HostSwarmID)
		}
	}
	return ""
}

func isWebsocketUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func copyProxyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Connection") ||
			strings.EqualFold(key, "Upgrade") ||
			strings.EqualFold(key, "Transfer-Encoding") ||
			strings.EqualFold(key, "Keep-Alive") ||
			strings.EqualFold(key, "Proxy-Authenticate") ||
			strings.EqualFold(key, "Proxy-Authorization") ||
			strings.EqualFold(key, "TE") ||
			strings.EqualFold(key, "Trailer") {
			continue
		}
		copied := append([]string(nil), values...)
		dst[key] = copied
	}
}

func cloneHeaderForUpstreamWebsocket(src http.Header) http.Header {
	dst := cloneHeaderExcludingAuth(src)
	for _, key := range []string{
		"Connection",
		"Upgrade",
		"Host",
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Version",
		"Sec-WebSocket-Extensions",
		"Sec-WebSocket-Protocol",
	} {
		dst.Del(key)
	}
	return dst
}

func logSwarmProxyTiming(r *http.Request, target swarmTarget, statusCode int, startedAt time.Time, err error) {
	if r == nil || !shouldLogSwarmProxyTiming(r.URL.Path) {
		return
	}
	if err != nil {
		log.Printf("swarm proxy timing method=%s path=%q swarm_id=%q status=%d elapsed_ms=%d err=%v", r.Method, strings.TrimSpace(r.URL.Path), strings.TrimSpace(target.SwarmID), statusCode, time.Since(startedAt).Milliseconds(), err)
		return
	}
	log.Printf("swarm proxy timing method=%s path=%q swarm_id=%q status=%d elapsed_ms=%d", r.Method, strings.TrimSpace(r.URL.Path), strings.TrimSpace(target.SwarmID), statusCode, time.Since(startedAt).Milliseconds())
}

func shouldLogSwarmProxyTiming(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	return strings.HasPrefix(path, "/v1/sessions/")
}
