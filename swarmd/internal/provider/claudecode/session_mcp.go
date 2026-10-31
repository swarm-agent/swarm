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

// SessionMCP is mounted once on the daemon's loopback API listener. Credentials
// survive restart; active executors are installed only by the provider run loop.
type SessionMCP struct {
	security *security.Service
	auth     *pebblestore.AuthStore
	root     string
	mu       sync.RWMutex
	url      string
	active   map[string]*mcpTools
}

func NewSessionMCP(sec *security.Service, auth *pebblestore.AuthStore, root string) *SessionMCP {
	return &SessionMCP{security: sec, auth: auth, root: root, active: make(map[string]*mcpTools)}
}

func (s *SessionMCP) SetAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "0" {
		return errors.New("session MCP requires a bound loopback API listener")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.url = "http://" + net.JoinHostPort(host, port) + "/mcp"
	return nil
}

func bearer(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// Handles distinguishes session credentials from existing Swarm Control keys at
// the same /mcp path. Invalid credentials fall through to ordinary API auth.
func (s *SessionMCP) Handles(r *http.Request) bool {
	rec, err := s.security.ValidateScopedToken(bearer(r))
	return err == nil && rec != nil && rec.SessionID != ""
}

func (s *SessionMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec, err := s.security.ValidateScopedToken(bearer(r))
	if err != nil || rec == nil || rec.SessionID == "" || !rec.HasScope("session:mcp") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// No request parameter can select another session, even with a valid token.
	if requested := r.Header.Get("Mcp-Session-Id"); requested != "" && requested != rec.SessionID {
		http.Error(w, "session mismatch", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "session selection is not supported", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	executor := s.active[sessionMCPKey(rec.AccountScopeID, rec.UserID, rec.SessionID)]
	s.mu.RUnlock()
	if executor == nil || executor.ctx.Err() != nil {
		http.Error(w, "session has no active tool executor", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Mcp-Session-Id", rec.SessionID)
	executor.handle(w, r)
}

func sessionMCPKey(account, user, session string) string {
	return account + "\x00" + user + "\x00" + session
}

// prepare serializes credential/config creation. The raw token is sealed using
// the existing account secret store, never stored in session projections.
func (s *SessionMCP) prepare(p identity.Principal, sessionID string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.url == "" {
		return "", "", errors.New("session MCP API listener is not ready")
	}
	if sessionID == "" {
		return "", "", errors.New("session MCP requires a durable session")
	}
	sum := sha256.Sum256([]byte(sessionMCPKey(p.AccountScopeID, p.UserID, sessionID)))
	name := "claudecode-session-" + hex.EncodeToString(sum[:])
	raw, found, err := s.auth.GetSecretValueForAccount(p.AccountScopeID, name)
	if err != nil {
		return "", "", err
	}
	token := string(raw)
	if !found {
		token, err = s.security.CreateSessionToken(p.AccountScopeID, p.UserID, sessionID)
		if err != nil {
			return "", "", err
		}
		if err = s.auth.PutSecretValueForAccount(p.AccountScopeID, name, []byte(token)); err != nil {
			return "", "", err
		}
	}
	rec, err := s.security.ValidateScopedToken(token)
	if err != nil || rec == nil || rec.AccountScopeID != p.AccountScopeID || rec.UserID != p.UserID || rec.SessionID != sessionID {
		return "", "", errors.New("session MCP credential is invalid or revoked")
	}
	dir := filepath.Join(s.root, name)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"swarm": map[string]any{"type": "http", "url": s.url, "headers": map[string]string{"Authorization": "Bearer " + token}}}})
	path := filepath.Join(dir, "mcp.json")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
		return "", "", errors.New("session MCP config must be a private regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(config) {
			return "", "", errors.New("session MCP endpoint changed; existing CLI config is not reusable")
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	} else {
		if err = os.WriteFile(path, config, 0600); err != nil {
			return "", "", err
		}
	}
	return dir, path, nil
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
