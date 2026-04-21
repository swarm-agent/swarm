package api

import (
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
	endpoint, err := cloneURLWithQuery(strings.TrimRight(target.BackendURL, "/")+r.URL.Path, r.URL.Query())
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
	endpoint, err := cloneURLWithQuery(strings.TrimRight(target.BackendURL, "/")+r.URL.Path, r.URL.Query())
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
	token, ok, err := s.swarm.OutgoingPeerAuthToken(target.SwarmID)
	if err != nil {
		return "", err
	}
	if ok {
		return token, nil
	}
	return "", fmt.Errorf("selected swarm target %q is missing peer auth", strings.TrimSpace(target.SwarmID))
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
