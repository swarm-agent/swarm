package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// SessionMCPScope is the only scope of a session tools credential. The key is
// an internal key in the canonical scoped credential store; it grants no HTTP
// API scope, and the API refuses it everywhere except the loopback /mcp mount.
const SessionMCPScope = "session:mcp"

// IsSessionMCPCredential reports whether a validated scoped key is a session
// tools credential, which only the loopback session MCP handler accepts.
// Do not use HasScope: wildcard/admin authorization is not a credential marker.
func IsSessionMCPCredential(rec *pebblestore.ScopedTokenRecord) bool {
	return rec != nil && rec.Kind == security.KeyKindInternal && len(rec.Scopes) == 1 && rec.Scopes[0] == SessionMCPScope
}

// SessionMCP is mounted once on the daemon's loopback API listener. Its state
// is provider-owned files under root: the session binding of each credential,
// the CLI config, and the CLI continuation. Credentials survive restart; active
// executors are installed only by the provider run loop.
type SessionMCP struct {
	security  *security.Service
	root      string
	address   string
	ready     chan struct{}
	readyOnce sync.Once
	mu        sync.RWMutex
	active    map[string]*mcpTools
}

// NewSessionMCP captures the storage root before run recovery can start. A run
// waits for SetAddress rather than failing while the daemon binds its listener.
func NewSessionMCP(sec *security.Service, root string) *SessionMCP {
	return &SessionMCP{security: sec, root: strings.TrimSpace(root), ready: make(chan struct{}), active: make(map[string]*mcpTools)}
}

func (s *SessionMCP) SetAddress(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || port == "" || port == "0" {
		return errors.New("session MCP requires a bound loopback API listener")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.address = net.JoinHostPort(host, port)
	s.readyOnce.Do(func() { close(s.ready) })
	return nil
}

// WaitReady is cancellable so daemon shutdown never strands a recovered turn.
func (s *SessionMCP) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ready:
		return ctx.Err()
	}
}

func (s *SessionMCP) endpointURL() (string, error) {
	// The caller holds mu, as it does for storageRoot and config creation.
	addr := s.address
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", errors.New("session MCP API listener is not ready")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "" || port == "0" {
		return "", errors.New("session MCP requires a bound loopback API listener")
	}
	return "http://" + net.JoinHostPort(host, port) + "/mcp", nil
}

func (s *SessionMCP) storageRoot() (string, error) {
	// root is captured at construction and never mutated.
	root := s.root
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("session MCP storage root is not configured")
	}
	return root, nil
}

func bearer(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// ServeIfSession serves the request when its bearer is a session tools
// credential and reports whether it did. Other credentials (Swarm Control keys
// at the same /mcp path) are left to ordinary API authentication.
func (s *SessionMCP) ServeIfSession(w http.ResponseWriter, r *http.Request) bool {
	rec, err := s.security.ValidateScopedToken(bearer(r))
	if err != nil || !IsSessionMCPCredential(rec) {
		return false
	}
	s.serveSession(w, r, rec)
	return true
}

// ServeHTTP serves only session tools credentials.
func (s *SessionMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.ServeIfSession(w, r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func (s *SessionMCP) serveSession(w http.ResponseWriter, r *http.Request, rec *pebblestore.ScopedTokenRecord) {
	binding, err := s.bindingFor(rec)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// No request parameter can select another session, even with a valid token.
	if requested := r.Header.Get("Mcp-Session-Id"); requested != "" && requested != binding.SessionID {
		http.Error(w, "session mismatch", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "session selection is not supported", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	executor := s.active[sessionMCPKey(binding.AccountScopeID, binding.UserID, binding.SessionID)]
	s.mu.RUnlock()
	if executor == nil || executor.ctx.Err() != nil {
		http.Error(w, "session has no active tool executor", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Mcp-Session-Id", binding.SessionID)
	executor.handle(w, r)
}

func sessionMCPKey(account, user, session string) string {
	return account + "\x00" + user + "\x00" + session
}

// credentialBinding ties one credential to exactly one owner and session. It
// is written by prepare only, under the provider's private storage root.
type credentialBinding struct {
	AccountScopeID string `json:"account_scope_id"`
	UserID         string `json:"user_id"`
	SessionID      string `json:"session_id"`
}

func validTokenID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *SessionMCP) bindingPath(tokenID string) (string, error) {
	root, err := s.storageRoot()
	if err != nil {
		return "", err
	}
	if !validTokenID(tokenID) {
		return "", errors.New("invalid session credential id")
	}
	return filepath.Join(root, "credentials", tokenID+".json"), nil
}

func (s *SessionMCP) bindingFor(rec *pebblestore.ScopedTokenRecord) (credentialBinding, error) {
	path, err := s.bindingPath(rec.ID)
	if err != nil {
		return credentialBinding{}, err
	}
	raw, err := readPrivateFile(path)
	if err != nil {
		return credentialBinding{}, err
	}
	var binding credentialBinding
	if err = json.Unmarshal(raw, &binding); err != nil {
		return credentialBinding{}, err
	}
	if binding.SessionID == "" || binding.AccountScopeID != rec.AccountScopeID || binding.UserID != rec.UserID {
		return credentialBinding{}, errors.New("session credential binding mismatch")
	}
	return binding, nil
}

// sessionConfig is the per-session CLI state prepared once and reused.
type sessionConfig struct {
	dir        string
	configPath string
	token      string
}

// credentialState records only the current credential id of a session (never
// the secret), so a replaced credential can be revoked.
type credentialState struct {
	TokenID string `json:"token_id"`
}

// prepare returns the session's private directory and CLI config, issuing the
// session credential on first use. The config is rewritten when the endpoint
// URL changes, and a revoked or lapsed credential is replaced by a new one, so
// an existing conversation always recovers on its next turn.
func (s *SessionMCP) prepare(p identity.Principal, sessionID string) (sessionConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(sessionID) == "" || p.AccountScopeID == "" || p.UserID == "" {
		return sessionConfig{}, errors.New("session MCP requires a durable session owner")
	}
	url, err := s.endpointURL()
	if err != nil {
		return sessionConfig{}, err
	}
	root, err := s.storageRoot()
	if err != nil {
		return sessionConfig{}, err
	}
	sum := sha256.Sum256([]byte(sessionMCPKey(p.AccountScopeID, p.UserID, sessionID)))
	dir := filepath.Join(root, "sessions", hex.EncodeToString(sum[:]))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return sessionConfig{}, err
	}
	if err = os.MkdirAll(filepath.Join(root, "credentials"), 0o700); err != nil {
		return sessionConfig{}, err
	}
	configPath := filepath.Join(dir, "mcp.json")
	statePath := filepath.Join(dir, "credential.json")

	token, tokenID := "", ""
	if raw, err := readPrivateFile(configPath); err == nil {
		token = configToken(raw)
	} else if !os.IsNotExist(err) {
		return sessionConfig{}, err
	}
	if raw, err := readPrivateFile(statePath); err == nil {
		var state credentialState
		if json.Unmarshal(raw, &state) == nil {
			tokenID = state.TokenID
		}
	} else if !os.IsNotExist(err) {
		return sessionConfig{}, err
	}
	if !s.credentialValid(token, tokenID, p, sessionID) {
		if token, err = s.issueCredential(p, sessionID, tokenID, statePath); err != nil {
			return sessionConfig{}, err
		}
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{mcpServerName: map[string]any{"type": "http", "url": url, "headers": map[string]string{"Authorization": "Bearer " + token}}}})
	if err != nil {
		return sessionConfig{}, err
	}
	if existing, err := readPrivateFile(configPath); err != nil || string(existing) != string(config) {
		if err != nil && !os.IsNotExist(err) {
			return sessionConfig{}, err
		}
		if err = writePrivateFile(configPath, config); err != nil {
			return sessionConfig{}, err
		}
	}
	return sessionConfig{dir: dir, configPath: configPath, token: token}, nil
}

func configToken(raw []byte) string {
	var config struct {
		Servers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return ""
	}
	header := strings.TrimSpace(config.Servers[mcpServerName].Headers["Authorization"])
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func (s *SessionMCP) credentialValid(token, tokenID string, p identity.Principal, sessionID string) bool {
	if token == "" || tokenID == "" {
		return false
	}
	rec, err := s.security.ValidateScopedToken(token)
	if err != nil || !IsSessionMCPCredential(rec) || rec.ID != tokenID || rec.AccountScopeID != p.AccountScopeID || rec.UserID != p.UserID {
		return false
	}
	binding, err := s.bindingFor(rec)
	return err == nil && binding.SessionID == sessionID
}

func (s *SessionMCP) issueCredential(p identity.Principal, sessionID, previousID, statePath string) (string, error) {
	token, rec, err := s.security.CreateInternalToken("Claude Code session tools", []string{SessionMCPScope}, p.AccountScopeID, p.UserID)
	if err != nil {
		return "", err
	}
	path, err := s.bindingPath(rec.ID)
	if err == nil {
		binding, _ := json.Marshal(credentialBinding{AccountScopeID: p.AccountScopeID, UserID: p.UserID, SessionID: sessionID})
		err = writePrivateFile(path, binding)
	}
	if err == nil {
		state, _ := json.Marshal(credentialState{TokenID: rec.ID})
		err = writePrivateFile(statePath, state)
	}
	if err != nil {
		_, _ = s.security.RevokeScopedToken(p.AccountScopeID, rec.ID)
		return "", err
	}
	if previousID != "" && previousID != rec.ID {
		// The replaced credential may still be valid (for example after a
		// tampered config); it must not keep authority over the session.
		_, _ = s.security.RevokeScopedToken(p.AccountScopeID, previousID)
		if old, err := s.bindingPath(previousID); err == nil {
			_ = os.Remove(old)
		}
	}
	return token, nil
}

// readPrivateFile refuses anything other than a private regular file.
func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("session MCP state must be a private regular file: " + filepath.Base(path))
	}
	return os.ReadFile(path)
}

// writePrivateFile replaces path atomically with a 0600 file.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SessionToken returns the session's standing tools credential, issuing it on
// first use. The provider runner uses the same path through prepare.
func (s *SessionMCP) SessionToken(p identity.Principal, sessionID string) (string, error) {
	config, err := s.prepare(p, sessionID)
	return config.token, err
}

// WithToolExecutor installs the already-resolved run executor for this session.
// The caller retains run lifecycle ownership and must invoke the returned detach.
func (s *SessionMCP) WithToolExecutor(ctx context.Context, p identity.Principal, req provideriface.Request) (func(), error) {
	plan := turnPlan{input: &turnInput{}, restart: &restartSignal{}, correlat: newToolUseCorrelator()}
	return s.attach(ctx, p, req, &plan)
}

func (s *SessionMCP) attach(ctx context.Context, p identity.Principal, req provideriface.Request, plan *turnPlan) (func(), error) {
	invoker := req.ToolInvoker
	definitions := req.Tools
	if strings.EqualFold(req.ToolChoice, "none") {
		definitions = nil
	}
	if invoker == nil {
		invoker = unavailableInvoker{}
	}
	if req.QueuedUserInput != nil {
		invoker = &queuedInputToolInvoker{inner: invoker, source: req.QueuedUserInput, input: plan.input}
	}
	executor, err := newMCPTools(ctx, definitions, invoker, plan.correlat, plan.restart.Trigger)
	if err != nil {
		return nil, err
	}
	executor.mediaContract = req.MediaContract
	key := sessionMCPKey(p.AccountScopeID, p.UserID, req.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[key] != nil {
		return nil, errors.New("session already has an active Claude Code turn")
	}
	s.active[key] = executor
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active[key] == executor {
			delete(s.active, key)
		}
	}, nil
}

type unavailableInvoker struct{}

func (unavailableInvoker) ExecuteTool(context.Context, provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	return provideriface.ToolExecutionResult{}, errors.New("session tools unavailable")
}
