// Package remote connects a Swarm daemon to a user-owned relay over an
// outbound WebSocket (protocol swarm-remote-1), so OAuth-authorized AI
// clients can use Swarm Control without any inbound listener on this machine.
//
// It is off by default: nothing connects until the owner initializes a relay
// and enables it. The relay authenticates AI clients; this daemon caps what
// any of them may do with its own locally configured ceiling and executes
// each tool through the scoped-token Swarm Control handler, so V3 scope,
// ownership and permission checks still decide.
package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	Protocol     = "swarm-remote-1"
	configKey    = "remote/transport/default"
	ScopeRead    = "swarm:read"
	ScopeWrite   = "swarm:write"
	ScopeApprove = "swarm:approve"
	ScopeManage  = "swarm:manage"

	handshakeTimeout = 15 * time.Second
	pingInterval     = 30 * time.Second
	readTimeout      = 90 * time.Second
	maxFrameBytes    = 4 << 20
	maxBackoff       = time.Minute
)

// toolScopes mirrors the relay's mapping; unknown tools need write.
var toolScopes = map[string]string{
	"swarm_list_workspaces":    ScopeRead,
	"swarm_list_sessions":      ScopeRead,
	"swarm_get_session":        ScopeRead,
	"swarm_list_projects":      ScopeRead,
	"swarm_list_workers":       ScopeRead,
	"swarm_get_worker":         ScopeRead,
	"swarm_get_usage":          ScopeRead,
	"swarm_start_session":      ScopeWrite,
	"swarm_send_message":       ScopeWrite,
	"swarm_run_plan":           ScopeWrite,
	"swarm_stop_run":           ScopeWrite,
	"swarm_assign_worker_task": ScopeWrite,
	"swarm_resolve_permission": ScopeApprove,
	"swarm_create_project":     ScopeManage,
	"swarm_create_worker":      ScopeManage,
	"swarm_update_worker":      ScopeManage,
	"swarm_manage_worker":      ScopeManage,
	"swarm_set_usage_limits":   ScopeManage,
}

func toolScope(name string) string {
	if scope, ok := toolScopes[name]; ok {
		return scope
	}
	return ScopeWrite
}

// SecretStore is the daemon's private secrets store.
type SecretStore interface {
	PutJSON(key string, v any) error
	GetJSON(key string, out any) (bool, error)
	Delete(key string) error
}

// TokenIssuer mints and revokes the scoped token this device uses to call
// Swarm Control in-process. Scopes are API scopes (sessions:read/write).
type TokenIssuer interface {
	Mint(name string, scopes []string) (token, id string, err error)
	Revoke(id string) error
}

// Config is persisted only in the secrets store.
type Config struct {
	RelayURL     string `json:"relay_url"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
	PrivateKey   string `json:"private_key"`
	Enabled      bool   `json:"enabled"`
	AllowWrite   bool   `json:"allow_write"`
	AllowApprove bool   `json:"allow_approve"`
	AllowManage  bool   `json:"allow_manage"`
	TokenID      string `json:"token_id"`
	Token        string `json:"token"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Consent is an authorization request an AI client is waiting on.
type Consent struct {
	Code         string   `json:"code"`
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	ClientDomain string   `json:"client_domain,omitempty"`
	RedirectHost string   `json:"redirect_host"`
	Scopes       []string `json:"scopes"`
	ExpiresAt    int64    `json:"expires_at"`
}

// Status is safe to show the owner: it never includes the private key or token.
type Status struct {
	Configured   bool      `json:"configured"`
	Enabled      bool      `json:"enabled"`
	Connected    bool      `json:"connected"`
	RelayURL     string    `json:"relay_url,omitempty"`
	DeviceID     string    `json:"device_id,omitempty"`
	DeviceName   string    `json:"device_name,omitempty"`
	PublicKey    string    `json:"public_key,omitempty"`
	AllowWrite   bool      `json:"allow_write"`
	AllowApprove bool      `json:"allow_approve"`
	AllowManage  bool      `json:"allow_manage"`
	LastError    string    `json:"last_error,omitempty"`
	Consents     []Consent `json:"pending_consents"`
}

type InitInput struct {
	RelayURL     string `json:"relay_url"`
	DeviceName   string `json:"device_name"`
	AllowWrite   bool   `json:"allow_write"`
	AllowApprove bool   `json:"allow_approve"`
	AllowManage  bool   `json:"allow_manage"`
}

type Service struct {
	store   SecretStore
	tokens  TokenIssuer
	control http.Handler

	mu        sync.Mutex
	cancel    context.CancelFunc
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connected bool
	lastError string
	consents  map[string]Consent
	wake      chan struct{}
}

func NewService(store SecretStore, tokens TokenIssuer) *Service {
	return &Service{store: store, tokens: tokens, consents: map[string]Consent{}, wake: make(chan struct{}, 1)}
}

// SetControlHandler supplies the scoped-token Swarm Control handler (the same
// one served on the SDK listener). Tool calls are executed through it.
func (s *Service) SetControlHandler(h http.Handler) {
	s.mu.Lock()
	s.control = h
	s.mu.Unlock()
}

func (s *Service) loadConfig() (Config, bool, error) {
	var cfg Config
	ok, err := s.store.GetJSON(configKey, &cfg)
	return cfg, ok, err
}

func (s *Service) Status() (Status, error) {
	cfg, ok, err := s.loadConfig()
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Configured: ok, Connected: s.connected, LastError: s.lastError, Consents: []Consent{}}
	now := time.Now().UnixMilli()
	for code, consent := range s.consents {
		if consent.ExpiresAt < now {
			delete(s.consents, code)
			continue
		}
		st.Consents = append(st.Consents, consent)
	}
	if !ok {
		return st, nil
	}
	st.Enabled, st.RelayURL, st.DeviceID, st.DeviceName = cfg.Enabled, cfg.RelayURL, cfg.DeviceID, cfg.DeviceName
	st.AllowWrite, st.AllowApprove, st.AllowManage = cfg.AllowWrite, cfg.AllowApprove, cfg.AllowManage
	if pub, err := publicKey(cfg.PrivateKey); err == nil {
		st.PublicKey = pub
	}
	return st, nil
}

func publicKey(private string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(private)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return "", errors.New("invalid device key")
	}
	return base64.StdEncoding.EncodeToString(ed25519.PrivateKey(raw).Public().(ed25519.PublicKey)), nil
}

// ValidateRelayURL requires https, or http only on a loopback host for local
// development. The URL is the relay origin; no path, query or credentials.
func ValidateRelayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("relay URL must be an origin such as https://swarm-relay.example.workers.dev")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
			return "", errors.New("relay URL must use https (http is allowed only for a loopback relay)")
		}
	default:
		return "", errors.New("relay URL must use https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// Init creates this device's identity for one relay. It does not connect;
// the owner registers the public key with the relay, then calls Enable.
func (s *Service) Init(in InitInput) (Status, error) {
	if _, ok, err := s.loadConfig(); err != nil {
		return Status{}, err
	} else if ok {
		return Status{}, errors.New("remote transport is already configured; reset it first")
	}
	origin, err := ValidateRelayURL(in.RelayURL)
	if err != nil {
		return Status{}, err
	}
	name := strings.TrimSpace(in.DeviceName)
	if name == "" || len(name) > 80 {
		return Status{}, errors.New("device name is required (at most 80 characters)")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Status{}, err
	}
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return Status{}, err
	}
	// API scopes the device token needs for its ceiling. Remote clients are
	// still limited per tool by the ceiling (toolScopes) on every call.
	scopes := []string{"sessions:read", "automations:read"}
	if in.AllowWrite || in.AllowApprove {
		scopes = append(scopes, "sessions:write")
	}
	if in.AllowWrite || in.AllowManage {
		scopes = append(scopes, "automations:write")
	}
	if in.AllowManage {
		scopes = append(scopes, "usage:write")
	}
	token, tokenID, err := s.tokens.Mint("swarm-remote "+name, scopes)
	if err != nil {
		return Status{}, fmt.Errorf("mint device token: %w", err)
	}
	now := time.Now().UnixMilli()
	cfg := Config{
		RelayURL: origin, DeviceID: "dev_" + hex.EncodeToString(idBytes[:]), DeviceName: name,
		PrivateKey: base64.StdEncoding.EncodeToString(private), AllowWrite: in.AllowWrite, AllowApprove: in.AllowApprove, AllowManage: in.AllowManage,
		TokenID: tokenID, Token: token, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.PutJSON(configKey, cfg); err != nil {
		_ = s.tokens.Revoke(tokenID)
		return Status{}, err
	}
	return s.Status()
}

func (s *Service) SetEnabled(enabled bool) (Status, error) {
	cfg, ok, err := s.loadConfig()
	if err != nil {
		return Status{}, err
	}
	if !ok {
		return Status{}, errors.New("remote transport is not configured")
	}
	cfg.Enabled, cfg.UpdatedAt = enabled, time.Now().UnixMilli()
	if err := s.store.PutJSON(configKey, cfg); err != nil {
		return Status{}, err
	}
	s.disconnect()
	s.signal()
	return s.Status()
}

// Reset disconnects, revokes the device token and deletes the identity.
func (s *Service) Reset() (Status, error) {
	cfg, ok, err := s.loadConfig()
	if err != nil {
		return Status{}, err
	}
	if ok {
		if cfg.TokenID != "" {
			if err := s.tokens.Revoke(cfg.TokenID); err != nil {
				return Status{}, fmt.Errorf("revoke device token: %w", err)
			}
		}
		if err := s.store.Delete(configKey); err != nil {
			return Status{}, err
		}
	}
	s.disconnect()
	s.mu.Lock()
	s.consents = map[string]Consent{}
	s.lastError = ""
	s.mu.Unlock()
	return s.Status()
}

// DecideConsent answers a pending authorization request. Approved scopes are
// limited to what the client requested and to this device's ceiling.
func (s *Service) DecideConsent(code string, approve bool, scopes []string) (Consent, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	s.mu.Lock()
	consent, ok := s.consents[code]
	s.mu.Unlock()
	if !ok || consent.ExpiresAt < time.Now().UnixMilli() {
		return Consent{}, errors.New("no pending authorization request with that code")
	}
	frame := map[string]any{"type": "consent.decision", "code": code, "approve": approve}
	if approve {
		cfg, _, err := s.loadConfig()
		if err != nil {
			return Consent{}, err
		}
		if len(scopes) == 0 {
			scopes = consent.Scopes
		}
		granted := []string{}
		for _, scope := range scopes {
			if contains(consent.Scopes, scope) && contains(ceiling(cfg), scope) && !contains(granted, scope) {
				granted = append(granted, scope)
			}
		}
		if !contains(granted, ScopeRead) {
			return Consent{}, errors.New("approval must include swarm:read")
		}
		frame["scopes"] = granted
		consent.Scopes = granted
	}
	if err := s.send(frame); err != nil {
		return Consent{}, fmt.Errorf("relay is not connected: %w", err)
	}
	s.mu.Lock()
	delete(s.consents, code)
	s.mu.Unlock()
	return consent, nil
}

func ceiling(cfg Config) []string {
	out := []string{ScopeRead}
	if cfg.AllowWrite {
		out = append(out, ScopeWrite)
	}
	if cfg.AllowApprove {
		out = append(out, ScopeApprove)
	}
	if cfg.AllowManage {
		out = append(out, ScopeManage)
	}
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) disconnect() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *Service) send(frame any) error {
	s.mu.Lock()
	conn := s.conn
	ready := s.connected
	s.mu.Unlock()
	if conn == nil || !ready {
		return errors.New("not connected")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// Run keeps the outbound connection alive while the transport is enabled.
// It returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		cfg, ok, err := s.loadConfig()
		if err != nil || !ok || !cfg.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		started := time.Now()
		err = s.session(ctx, cfg)
		s.mu.Lock()
		s.connected = false
		s.conn = nil
		if err != nil && ctx.Err() == nil {
			s.lastError = err.Error()
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 2*maxBackoff {
			backoff = time.Second
		}
		jitter := time.Duration(time.Now().UnixNano() % int64(backoff/2+1))
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(backoff + jitter):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

type frame struct {
	Type   string          `json:"type"`
	Nonce  string          `json:"nonce,omitempty"`
	ID     string          `json:"id,omitempty"`
	Client *frameClient    `json:"client,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	// consent.request
	Code         string   `json:"code,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ClientName   string   `json:"client_name,omitempty"`
	ClientDomain string   `json:"client_domain,omitempty"`
	RedirectHost string   `json:"redirect_host,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	ExpiresAt    int64    `json:"expires_at,omitempty"`
}

type frameClient struct {
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

func AuthMessage(relayOrigin, deviceID, nonce string) string {
	return Protocol + "\n" + relayOrigin + "\n" + deviceID + "\n" + nonce
}

func (s *Service) session(ctx context.Context, cfg Config) error {
	raw, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return errors.New("stored device key is invalid")
	}
	endpoint := strings.Replace(strings.Replace(cfg.RelayURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/device/connect"
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: handshakeTimeout}
	conn, resp, err := dialer.DialContext(ctx, endpoint, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect relay: %w", err)
	}
	conn.SetReadLimit(maxFrameBytes)
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	var challenge frame
	if err := conn.ReadJSON(&challenge); err != nil || challenge.Type != "challenge" || challenge.Nonce == "" {
		return errors.New("relay did not send a challenge")
	}
	signature := ed25519.Sign(ed25519.PrivateKey(raw), []byte(AuthMessage(cfg.RelayURL, cfg.DeviceID, challenge.Nonce)))
	if err := conn.WriteJSON(map[string]any{"type": "auth", "protocol": Protocol, "device_id": cfg.DeviceID, "name": cfg.DeviceName, "signature": base64.StdEncoding.EncodeToString(signature)}); err != nil {
		return err
	}
	var ready frame
	if err := conn.ReadJSON(&ready); err != nil || ready.Type != "ready" {
		if closeErr, ok := err.(*websocket.CloseError); ok {
			return fmt.Errorf("relay rejected device: %s", closeErr.Text)
		}
		return errors.New("relay did not accept the device")
	}
	s.mu.Lock()
	s.connected, s.lastError = true, ""
	s.mu.Unlock()
	log.Printf("remote transport connected relay=%s device=%s", cfg.RelayURL, cfg.DeviceID)

	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readTimeout)) })
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				s.writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				s.writeMu.Unlock()
				if err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		var msg frame
		if err := conn.ReadJSON(&msg); err != nil {
			if closeErr, ok := err.(*websocket.CloseError); ok {
				return fmt.Errorf("relay closed the connection: %s", closeErr.Text)
			}
			return fmt.Errorf("relay connection lost: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		switch msg.Type {
		case "mcp.request":
			go s.answer(cfg, msg)
		case "consent.request":
			s.mu.Lock()
			s.consents[strings.ToUpper(msg.Code)] = Consent{Code: strings.ToUpper(msg.Code), ClientID: msg.ClientID, ClientName: msg.ClientName, ClientDomain: msg.ClientDomain, RedirectHost: msg.RedirectHost, Scopes: msg.Scopes, ExpiresAt: msg.ExpiresAt}
			s.mu.Unlock()
			log.Printf("remote transport authorization request code=%s client=%q redirect_host=%s", msg.Code, msg.ClientName, msg.RedirectHost)
		}
	}
}

func (s *Service) answer(cfg Config, msg frame) {
	body := s.handle(cfg, msg)
	_ = s.send(map[string]any{"type": "mcp.response", "id": msg.ID, "body": body})
}

// handle enforces this device's ceiling on the relay-verified client scopes,
// then executes the JSON-RPC message through the Swarm Control handler.
func (s *Service) handle(cfg Config, msg frame) json.RawMessage {
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg.Body, &rpc); err != nil {
		return rpcError(nil, -32700, "parse error")
	}
	effective := []string{}
	if msg.Client != nil {
		for _, scope := range msg.Client.Scopes {
			if contains(ceiling(cfg), scope) {
				effective = append(effective, scope)
			}
		}
	}
	if !contains(effective, ScopeRead) {
		return rpcError(rpc.ID, -32001, "this client is not authorized on this machine")
	}
	switch rpc.Method {
	case "tools/list", "tools/call":
	default:
		return rpcError(rpc.ID, -32601, "method not available through the relay")
	}
	if rpc.Method == "tools/call" && !contains(effective, toolScope(rpc.Params.Name)) {
		result, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "This machine does not allow " + rpc.Params.Name + " for remote clients."}},
			"isError": true,
		}})
		return result
	}
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return rpcError(rpc.ID, -32603, "Swarm Control is unavailable")
	}
	req, err := http.NewRequest(http.MethodPost, "http://swarm-remote/mcp", bytes.NewReader(msg.Body))
	if err != nil {
		return rpcError(rpc.ID, -32603, "internal error")
	}
	req.RemoteAddr = "swarm-remote"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	recorder := &responseRecorder{header: http.Header{}, status: http.StatusOK}
	control.ServeHTTP(recorder, req)
	if recorder.status != http.StatusOK || recorder.overflow {
		return rpcError(rpc.ID, -32603, fmt.Sprintf("Swarm Control returned HTTP %d", recorder.status))
	}
	if rpc.Method == "tools/list" {
		return filterTools(recorder.body.Bytes(), effective)
	}
	return json.RawMessage(recorder.body.Bytes())
}

func filterTools(raw []byte, effective []string) json.RawMessage {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return raw
	}
	result, _ := response["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	kept := []any{}
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		if contains(effective, toolScope(name)) {
			kept = append(kept, tool)
		}
	}
	if result != nil {
		result["tools"] = kept
	}
	out, err := json.Marshal(response)
	if err != nil {
		return raw
	}
	return out
}

func rpcError(id json.RawMessage, code int, message string) json.RawMessage {
	var responseID any
	if len(id) > 0 {
		responseID = id
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": responseID, "error": map[string]any{"code": code, "message": message}})
	return out
}

type responseRecorder struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	wrote    bool
	overflow bool
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	r.wrote = true
	if r.body.Len()+len(p) > maxFrameBytes {
		r.overflow = true
		return 0, errors.New("response too large")
	}
	return r.body.Write(p)
}
