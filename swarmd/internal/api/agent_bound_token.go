package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// An agent-bound token (ScopedTokenRecord.AgentName) is what an application
// gateway holds to serve one sealed agent. It is default-deny: it may create
// sessions only with that agent, use only that agent's sessions through the
// routes below, and resolve only that agent's client tool calls, never an
// approval for anything Swarm would execute. The agent must still be sealed
// (only client tools enabled) on every request, so widening the agent later
// disables the token instead of widening it.

const agentBoundCreateBodyLimit = 1 << 20

// agentBoundMaxLifetime caps a gateway token; it is always minted with an expiry.
const agentBoundMaxLifetime = 30 * 24 * time.Hour

// agentBoundCreateFields is everything an agent-bound token may send when it
// creates a session. Anything else (project, parent, model, preference,
// metadata, session ID, worktree, swarm or target routing) is refused, so a new
// create field is closed to gateways until it is reviewed and added here.
var agentBoundCreateFields = map[string]bool{
	"agent_name": true, "client_request_id": true, "idempotency_key": true,
	"title": true, "mode": true, "workspace_path": true, "workspace_id": true,
}

// Defaults for what an agent-bound token may start: each message is a model
// run, each session a new conversation. Polling routes are not limited.
const (
	agentBoundDefaultMessagesPerMinute = 120
	agentBoundDefaultSessionsPerHour   = 300
	agentBoundDefaultConcurrentRuns    = 4
)

// agentBoundRunReservation holds an admitted message's slot until its run
// shows up as active (or the message fails), so simultaneous messages cannot
// all pass the concurrency check before any of them starts.
const agentBoundRunReservation = 15 * time.Second

type agentBoundConcurrency struct {
	mu       sync.Mutex
	reserved map[string]map[string]time.Time // token ID -> session ID -> reservation expiry
}

var agentBoundRuns = &agentBoundConcurrency{reserved: map[string]map[string]time.Time{}}

// admit reports whether a message to sessionID may start a run: the agent's
// running sessions plus this token's live reservations stay within limit. A
// message to a session that already holds a slot is admitted (it queues behind
// that session's run and adds no concurrency).
func (c *agentBoundConcurrency) admit(tokenID, sessionID string, running map[string]bool, limit int, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := map[string]bool{}
	for id := range running {
		held[id] = true
	}
	reservations := c.reserved[tokenID]
	for id, until := range reservations {
		if !now.Before(until) || running[id] {
			delete(reservations, id)
			continue
		}
		held[id] = true
	}
	if held[sessionID] {
		return true
	}
	if len(held) >= limit {
		return false
	}
	if reservations == nil {
		reservations = map[string]time.Time{}
		c.reserved[tokenID] = reservations
	}
	reservations[sessionID] = now.Add(agentBoundRunReservation)
	return true
}

// runningAgentSessions returns the account's sessions of agent with a run in flight.
func (s *Server) runningAgentSessions(accountScopeID, agent string) map[string]bool {
	running := map[string]bool{}
	if s.sessions == nil {
		return running
	}
	for _, id := range s.v3SessionExecutor.activeSessionIDs() {
		session, ok, err := s.sessions.GetSession(id)
		if err != nil || !ok || session.AccountScopeID != accountScopeID {
			continue
		}
		if sessionsV3MetadataString(session.Metadata, "agent_name") == agent {
			running[id] = true
		}
	}
	return running
}

// tokenBucket refills continuously up to its capacity.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

type agentBoundRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

var agentBoundLimits = &agentBoundRateLimiter{buckets: map[string]*tokenBucket{}}

// take spends one unit of key's bucket (capacity per window) and returns how
// long to wait when it is empty.
func (l *agentBoundRateLimiter) take(key string, capacity int, window time.Duration, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rate := float64(capacity) / window.Seconds()
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &tokenBucket{tokens: float64(capacity), last: now}
		l.buckets[key] = bucket
	}
	bucket.tokens = min(float64(capacity), bucket.tokens+now.Sub(bucket.last).Seconds()*rate)
	bucket.last = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}
	return false, time.Duration((1-bucket.tokens)/rate*float64(time.Second)) + time.Millisecond
}

func agentBoundRateFor(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	return fallback
}

// sealedAgentError reports why name is not a sealed agent in the account.
func (s *Server) sealedAgentError(accountScopeID, name string) error {
	name = strings.TrimSpace(name)
	if s.agents == nil || name == "" {
		return errors.New("agent-bound token has no agent")
	}
	profile, ok, err := s.agents.GetProfileForAccount(accountScopeID, name)
	if err != nil {
		return err
	}
	if !ok || !profile.Enabled {
		return fmt.Errorf("agent %q is not available", name)
	}
	if profile.Protected || profile.ToolContract == nil || strings.TrimSpace(profile.ToolContract.Preset) != "custom" || profile.ToolContract.InheritPolicy {
		return fmt.Errorf("agent %q is not sealed: it must use the custom preset with only client tools", name)
	}
	clientTools := 0
	for tool, config := range profile.ToolContract.Tools {
		if config.Enabled == nil || !*config.Enabled {
			continue
		}
		definition, found, err := s.agents.GetCustomToolForAccount(accountScopeID, tool)
		if err != nil {
			return err
		}
		if !found || definition.Kind != pebblestore.AgentCustomToolKindClient {
			return fmt.Errorf("agent %q is not sealed: tool %q is not a client tool", name, tool)
		}
		clientTools++
	}
	// A custom agent with every tool off is an ordinary chat agent, not a
	// sealed one: it keeps the normal prompt and no sealed limits.
	if clientTools == 0 {
		return fmt.Errorf("agent %q is not sealed: it has no client tools", name)
	}
	return nil
}

// agentBoundRequestError returns nil when an agent-bound token may make r.
func (s *Server) agentBoundRequestError(r *http.Request, rec *pebblestore.ScopedTokenRecord) error {
	agent := strings.TrimSpace(rec.AgentName)
	if err := s.sealedAgentError(rec.AccountScopeID, agent); err != nil {
		return err
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) == 2 && p[0] == "v3" && p[1] == "sessions:usage-limits" && r.Method == http.MethodGet {
		// Read-only: lets the gateway's budget breaker see today's spend.
		return nil
	}
	if len(p) < 2 || p[0] != "v3" || p[1] != "sessions" {
		return errors.New("route unavailable to an agent-bound token")
	}
	if len(p) == 2 {
		if rec.AgentOperator {
			return errors.New("an operator token cannot list or create sessions")
		}
		if r.Method != http.MethodPost {
			return errors.New("an agent-bound token cannot list sessions")
		}
		return checkAgentBoundCreate(r, agent)
	}
	sessionID := p[2]
	if sessionID == "" || sessionID == "." || sessionID == ".." || s.sessions == nil {
		return errors.New("session not found")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok || session.AccountScopeID != rec.AccountScopeID {
		return errors.New("session not found")
	}
	for _, key := range []string{"agent_name", "resolved_agent_name"} {
		if value := sessionsV3MetadataString(session.Metadata, key); value != "" && value != agent {
			return errors.New("session not found")
		}
	}
	if sessionsV3MetadataString(session.Metadata, "agent_name") != agent {
		return errors.New("session not found")
	}
	rest := strings.Join(p[3:], "/")
	if rec.AgentOperator {
		return agentOperatorRouteError(r.Method, rest)
	}
	switch {
	case rest == "messages" && r.Method == http.MethodPost:
		return checkAgentBoundMessage(r)
	case rest == "" && r.Method == http.MethodGet,
		rest == "messages" && r.Method == http.MethodGet,
		rest == "permissions" && r.Method == http.MethodGet,
		rest == "run/stop" && r.Method == http.MethodPost:
		return nil
	case len(p) == 6 && p[3] == "permissions" && p[5] == "resolve" && r.Method == http.MethodPost:
		return s.checkAgentBoundResolve(rec.AccountScopeID, sessionID, p[4])
	}
	return errors.New("route unavailable to an agent-bound token")
}

// agentOperatorRouteError admits the human-takeover key (an agent-bound token
// minted with operator) to its agent's conversations only to read them, take
// them over or hand them back, and reply as a person. It cannot post as the
// visitor, start runs, create or list sessions, or answer tool calls.
func agentOperatorRouteError(method, rest string) error {
	switch {
	case rest == "" && method == http.MethodGet,
		rest == "messages" && method == http.MethodGet,
		rest == "takeover" && (method == http.MethodGet || method == http.MethodPost),
		rest == "operator-messages" && method == http.MethodPost:
		return nil
	}
	return errors.New("route unavailable to an operator token")
}

func checkAgentBoundCreate(r *http.Request, agent string) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, agentBoundCreateBodyLimit+1))
	if err != nil || len(raw) > agentBoundCreateBodyLimit {
		return errors.New("session create body is invalid")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return errors.New("session create body is invalid")
	}
	if name, _ := body["agent_name"].(string); strings.TrimSpace(name) != agent {
		return fmt.Errorf("an agent-bound token can only create %q sessions", agent)
	}
	for key := range body {
		if !agentBoundCreateFields[key] {
			return fmt.Errorf("an agent-bound token cannot set %q", key)
		}
	}
	return nil
}

// checkAgentBoundMessage admits plain text only: media, artifact selections
// and metadata could pull the account's other content into a public session.
func checkAgentBoundMessage(r *http.Request) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, agentBoundCreateBodyLimit+1))
	if err != nil || len(raw) > agentBoundCreateBodyLimit {
		return errors.New("message body is invalid")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return errors.New("message body is invalid")
	}
	for key := range body {
		switch key {
		case "client_request_id", "idempotency_key", "role", "content":
		default:
			return fmt.Errorf("an agent-bound token cannot set %q on a message", key)
		}
	}
	return nil
}

func (s *Server) checkAgentBoundResolve(accountScopeID, sessionID, permissionID string) error {
	if s.perm == nil || permissionID == "" {
		return errors.New("permission not found")
	}
	records, err := s.perm.ListPermissions(sessionID, 1000)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID != permissionID {
			continue
		}
		definition, ok, err := s.agents.GetCustomToolForAccount(accountScopeID, record.ToolName)
		if err != nil || !ok || definition.Kind != pebblestore.AgentCustomToolKindClient {
			return errors.New("an agent-bound token can only answer client tool calls")
		}
		return nil
	}
	return errors.New("permission not found")
}

// gateAgentBoundToken writes a 403 and returns false when rec is agent-bound
// and may not make r. Other tokens pass through unchanged.
func (s *Server) gateAgentBoundToken(w http.ResponseWriter, r *http.Request, rec *pebblestore.ScopedTokenRecord) bool {
	if rec == nil || strings.TrimSpace(rec.AgentName) == "" {
		return true
	}
	if err := s.agentBoundRequestError(r, rec); err != nil {
		writeError(w, http.StatusForbidden, err)
		return false
	}
	if r.Method != http.MethodPost {
		return true
	}
	path := strings.Trim(r.URL.Path, "/")
	var ok bool
	var wait time.Duration
	switch {
	case path == "v3/sessions":
		ok, wait = agentBoundLimits.take(rec.ID+"/sessions", agentBoundRateFor(rec.SessionsPerHour, agentBoundDefaultSessionsPerHour), time.Hour, time.Now())
	case strings.HasSuffix(path, "/messages"):
		ok, wait = agentBoundLimits.take(rec.ID+"/messages", agentBoundRateFor(rec.MessagesPerMinute, agentBoundDefaultMessagesPerMinute), time.Minute, time.Now())
	default:
		return true
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, errors.New("agent-bound token rate limit reached"))
		return false
	}
	if path != "v3/sessions" {
		// A message starts a model run: bound how many run at once.
		sessionID := strings.Split(path, "/")[2]
		agent := strings.TrimSpace(rec.AgentName)
		limit := agentBoundRateFor(rec.MaxConcurrentRuns, agentBoundDefaultConcurrentRuns)
		if !agentBoundRuns.admit(rec.ID, sessionID, s.runningAgentSessions(rec.AccountScopeID, agent), limit, time.Now()) {
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusTooManyRequests, errors.New("agent-bound token concurrent run limit reached"))
			return false
		}
	}
	return true
}
