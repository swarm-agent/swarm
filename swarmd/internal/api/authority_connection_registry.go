package api

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const authorityConnectionDefaultTTL = 2 * time.Minute

const (
	authorityConnectionTransportLocal    = "local"
	authorityConnectionTransportHTTP     = "http"
	authorityConnectionTransportTopology = "topology"
	// AuthorityConnectionHealthOnline marks a connection usable for dispatch.
	AuthorityConnectionHealthOnline = "online"
	// AuthorityConnectionHealthStale marks a connection that is known but expired.
	AuthorityConnectionHealthStale = "stale"
)

// AuthorityConnection is live service discovery for an authority host.
// It may carry URLs and transport refs, but callers must not copy those into
// durable session/binding records as routing authority.
type AuthorityConnection struct {
	AuthorityHostSwarmID string
	AccountScopeID       string
	ConnectionID         string
	TransportKind        string
	TransportRef         string
	Health               string
	LastSeenAt           time.Time
	ExpiresAt            time.Time
	ConnectionGeneration int64
}

func (c AuthorityConnection) endpoint() string {
	return strings.TrimRight(strings.TrimSpace(c.TransportRef), "/")
}

func (c AuthorityConnection) usable(now time.Time) bool {
	if strings.TrimSpace(c.AuthorityHostSwarmID) == "" || c.endpoint() == "" {
		return false
	}
	if health := strings.TrimSpace(c.Health); health != "" && !strings.EqualFold(health, AuthorityConnectionHealthOnline) {
		return false
	}
	return c.ExpiresAt.IsZero() || now.Before(c.ExpiresAt)
}

// AuthorityConnectionRegistry resolves the current live transport for an
// authority host. Resolution is account-scoped because the same swarm id can be
// observed in separate account stores during tests/multi-account runtime.
type AuthorityConnectionRegistry interface {
	Resolve(accountScopeID, authorityHostSwarmID string) (AuthorityConnection, bool)
	Upsert(connection AuthorityConnection)
}

type memoryAuthorityConnectionRegistry struct {
	mu          sync.RWMutex
	ttl         time.Duration
	generation  int64
	connections map[string]AuthorityConnection
}

func newAuthorityConnectionRegistry() *memoryAuthorityConnectionRegistry {
	return &memoryAuthorityConnectionRegistry{ttl: authorityConnectionDefaultTTL, connections: make(map[string]AuthorityConnection)}
}

func (r *memoryAuthorityConnectionRegistry) Resolve(accountScopeID, authorityHostSwarmID string) (AuthorityConnection, bool) {
	if r == nil {
		return AuthorityConnection{}, false
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	authorityHostSwarmID = strings.TrimSpace(authorityHostSwarmID)
	if authorityHostSwarmID == "" {
		return AuthorityConnection{}, false
	}
	now := time.Now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, key := range []string{authorityConnectionKey(accountScopeID, authorityHostSwarmID), authorityConnectionKey("", authorityHostSwarmID)} {
		conn, ok := r.connections[key]
		if !ok || !conn.usable(now) {
			continue
		}
		return conn, true
	}
	return AuthorityConnection{}, false
}

func (r *memoryAuthorityConnectionRegistry) Upsert(connection AuthorityConnection) {
	if r == nil {
		return
	}
	connection.AuthorityHostSwarmID = strings.TrimSpace(connection.AuthorityHostSwarmID)
	connection.AccountScopeID = strings.TrimSpace(connection.AccountScopeID)
	connection.TransportRef = strings.TrimRight(strings.TrimSpace(connection.TransportRef), "/")
	if connection.AuthorityHostSwarmID == "" || connection.TransportRef == "" {
		return
	}
	if strings.TrimSpace(connection.Health) == "" {
		connection.Health = AuthorityConnectionHealthOnline
	}
	if connection.LastSeenAt.IsZero() {
		connection.LastSeenAt = time.Now()
	}
	if connection.ExpiresAt.IsZero() && r.ttl > 0 {
		connection.ExpiresAt = connection.LastSeenAt.Add(r.ttl)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if connection.ConnectionGeneration == 0 {
		connection.ConnectionGeneration = r.generation
	}
	if connection.ConnectionID == "" {
		connection.ConnectionID = fmt.Sprintf("authority:%s:%d", connection.AuthorityHostSwarmID, connection.ConnectionGeneration)
	}
	r.connections[authorityConnectionKey(connection.AccountScopeID, connection.AuthorityHostSwarmID)] = connection
}

func authorityConnectionKey(accountScopeID, authorityHostSwarmID string) string {
	return strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(authorityHostSwarmID)
}

func (s *Server) ensureAuthorityConnectionRegistry() AuthorityConnectionRegistry {
	if s == nil {
		return nil
	}
	if s.authorityConnections == nil {
		s.authorityConnections = newAuthorityConnectionRegistry()
	}
	return s.authorityConnections
}

func (s *Server) ResolveAuthorityConnection(accountScopeID, authorityHostSwarmID string) (AuthorityConnection, bool) {
	authorityHostSwarmID = strings.TrimSpace(authorityHostSwarmID)
	accountScopeID = strings.TrimSpace(accountScopeID)
	if s == nil || authorityHostSwarmID == "" {
		return AuthorityConnection{}, false
	}
	if conn, ok := s.observeAuthorityConnectionFromState(accountScopeID, authorityHostSwarmID); ok {
		return conn, true
	}
	if s.authorityConnections != nil {
		if conn, ok := s.authorityConnections.Resolve(accountScopeID, authorityHostSwarmID); ok {
			return conn, true
		}
	}
	if s.isLocalSwarmID(authorityHostSwarmID) {
		return AuthorityConnection{
			AuthorityHostSwarmID: authorityHostSwarmID,
			AccountScopeID:       accountScopeID,
			ConnectionID:         "local:" + authorityHostSwarmID,
			TransportKind:        authorityConnectionTransportLocal,
			TransportRef:         "local://" + authorityHostSwarmID,
			Health:               AuthorityConnectionHealthOnline,
			LastSeenAt:           time.Now(),
		}, true
	}
	return AuthorityConnection{}, false
}

func (s *Server) observeAuthorityConnectionFromState(accountScopeID, authorityHostSwarmID string) (AuthorityConnection, bool) {
	authorityHostSwarmID = strings.TrimSpace(authorityHostSwarmID)
	if s == nil || authorityHostSwarmID == "" {
		return AuthorityConnection{}, false
	}
	var endpoint string
	var transportKind string
	if s.swarm != nil {
		if cfg, err := s.loadStartupConfig(); err == nil {
			if state, err := s.currentSwarmState(cfg); err == nil {
				for _, target := range listTrustedPeerTargets(state.TrustedPeers) {
					if strings.EqualFold(strings.TrimSpace(target.SwarmID), authorityHostSwarmID) && strings.TrimSpace(target.BackendURL) != "" {
						endpoint = strings.TrimSpace(target.BackendURL)
						transportKind = strings.TrimSpace(target.AttachStatus)
						break
					}
				}
			}
		}
	}
	if endpoint == "" && s.topology != nil {
		if accountScopeID != "" {
			if runtimeRecord, ok, err := s.topology.GetRuntimeForAccount(accountScopeID, authorityHostSwarmID); err == nil && ok {
				endpoint = strings.TrimSpace(runtimeRecord.BackendURL)
				transportKind = strings.TrimSpace(runtimeRecord.Transport)
			}
		} else if runtimeRecord, ok, err := s.topology.GetRuntime(authorityHostSwarmID); err == nil && ok {
			endpoint = strings.TrimSpace(runtimeRecord.BackendURL)
			transportKind = strings.TrimSpace(runtimeRecord.Transport)
		}
	}
	if endpoint == "" && s.swarmNodes != nil {
		if node, ok, err := s.swarmNodes.Get(authorityHostSwarmID); err == nil && ok {
			endpoint = strings.TrimSpace(node.BackendURL)
			transportKind = strings.TrimSpace(node.Transport)
		}
	}
	if endpoint == "" {
		return AuthorityConnection{}, false
	}
	conn := AuthorityConnection{
		AuthorityHostSwarmID: authorityHostSwarmID,
		AccountScopeID:       strings.TrimSpace(accountScopeID),
		TransportKind:        firstNonEmpty(transportKind, authorityConnectionTransportHTTP),
		TransportRef:         endpoint,
		Health:               AuthorityConnectionHealthOnline,
		LastSeenAt:           time.Now(),
	}
	s.ensureAuthorityConnectionRegistry().Upsert(conn)
	resolved, ok := s.authorityConnections.Resolve(accountScopeID, authorityHostSwarmID)
	return resolved, ok
}

func (s *Server) RegisterAuthorityConnection(connection AuthorityConnection) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	if strings.TrimSpace(connection.AuthorityHostSwarmID) == "" {
		return errors.New("authority_host_swarm_id is required")
	}
	if strings.TrimSpace(connection.TransportRef) == "" {
		return errors.New("authority transport_ref is required")
	}
	s.ensureAuthorityConnectionRegistry().Upsert(connection)
	return nil
}

func authorityConnectionForTarget(target swarmTarget) AuthorityConnection {
	return AuthorityConnection{
		AuthorityHostSwarmID: firstNonEmpty(strings.TrimSpace(target.HostSwarmID), strings.TrimSpace(target.SwarmID)),
		TransportKind:        firstNonEmpty(strings.TrimSpace(target.AttachStatus), authorityConnectionTransportHTTP),
		TransportRef:         strings.TrimSpace(target.BackendURL),
		Health:               AuthorityConnectionHealthOnline,
		LastSeenAt:           time.Now(),
	}
}

func (s *Server) backendURLForAuthorityHost(accountScopeID, authorityHostSwarmID string) string {
	conn, ok := s.ResolveAuthorityConnection(accountScopeID, authorityHostSwarmID)
	if !ok {
		return ""
	}
	if strings.EqualFold(conn.TransportKind, authorityConnectionTransportLocal) {
		return ""
	}
	return conn.endpoint()
}

func (s *Server) resolveTargetAuthorityBackendURL(accountScopeID string, target swarmTarget) (string, error) {
	authorityHostSwarmID := firstNonEmpty(strings.TrimSpace(target.HostSwarmID), strings.TrimSpace(target.SwarmID))
	if s.isLocalSwarmID(authorityHostSwarmID) {
		if backendURL := strings.TrimSpace(target.BackendURL); backendURL != "" {
			return backendURL, nil
		}
	}
	conn, ok := s.ResolveAuthorityConnection(accountScopeID, authorityHostSwarmID)
	if ok && !strings.EqualFold(conn.TransportKind, authorityConnectionTransportLocal) {
		return conn.endpoint(), nil
	}
	if backendURL := strings.TrimSpace(target.BackendURL); backendURL != "" {
		conn := authorityConnectionForTarget(target)
		conn.AccountScopeID = strings.TrimSpace(accountScopeID)
		s.ensureAuthorityConnectionRegistry().Upsert(conn)
		return strings.TrimRight(backendURL, "/"), nil
	}
	return "", fmt.Errorf("authority transport is unavailable for swarm %q", authorityHostSwarmID)
}

func authorityConnectionHealthKey(accountScopeID, authorityHostSwarmID string) string {
	return "authority|" + strings.TrimSpace(accountScopeID) + "|" + strings.TrimSpace(authorityHostSwarmID)
}

func (s *Server) markAuthorityConnectionHealth(accountScopeID, authorityHostSwarmID string, online bool) {
	if s == nil {
		return
	}
	key := authorityConnectionHealthKey(accountScopeID, authorityHostSwarmID)
	if strings.TrimSpace(key) == "authority||" {
		return
	}
	s.swarmTargetHealth.mu.Lock()
	if s.swarmTargetHealth.entries == nil {
		s.swarmTargetHealth.entries = make(map[string]swarmTargetHealthEntry)
	}
	s.swarmTargetHealth.entries[key] = swarmTargetHealthEntry{online: online, checkedAt: time.Now()}
	s.swarmTargetHealth.mu.Unlock()
}
