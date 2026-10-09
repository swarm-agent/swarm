package egress

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Registry maps a per-sandbox proxy token to the secrets that sandbox may use.
// A token is minted when a sandbox with grants starts and dropped when the
// sandbox stops, so a leaked token stops working once the sandbox is gone.
// It implements Resolver.
type Registry struct {
	mu      sync.RWMutex
	byToken map[string]Sandbox
	byRoot  map[string]string // workspace path -> token
}

func NewRegistry() *Registry {
	return &Registry{byToken: map[string]Sandbox{}, byRoot: map[string]string{}}
}

func (r *Registry) ResolveSandbox(token string) (Sandbox, bool) {
	if token == "" {
		return Sandbox{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	sb, ok := r.byToken[token]
	return sb, ok
}

// Register mints a fresh token for a sandbox's current grants, replacing any
// previous token for the same workspace, and returns it. An empty grant set
// still mints a token so the proxy authenticates; every host is then tunnelled.
func (r *Registry) Register(sandbox Sandbox) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byRoot[sandbox.WorkspacePath]; ok {
		delete(r.byToken, old)
	}
	r.byToken[token] = sandbox
	r.byRoot[sandbox.WorkspacePath] = token
	return token, nil
}

// Unregister drops a workspace's token.
func (r *Registry) Unregister(workspacePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if token, ok := r.byRoot[workspacePath]; ok {
		delete(r.byToken, token)
		delete(r.byRoot, workspacePath)
	}
}
