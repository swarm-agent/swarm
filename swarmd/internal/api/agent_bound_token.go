package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

// agentBoundCreateFields is everything an agent-bound token may send when it
// creates a session. Anything else (project, parent, model, preference,
// metadata, session ID, worktree, swarm or target routing) is refused, so a new
// create field is closed to gateways until it is reviewed and added here.
var agentBoundCreateFields = map[string]bool{
	"agent_name": true, "client_request_id": true, "idempotency_key": true,
	"title": true, "mode": true, "workspace_path": true, "workspace_id": true,
	"end_user": true, "labels": true,
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

// Defaults per end user, for sessions a gateway created with an end_user.
// Sessions without one count only against the token's own limits.
const (
	agentBoundDefaultEndUserMessagesPerMinute = 20
	agentBoundDefaultEndUserSessionsPerHour   = 30
)

// Codes on a 429, so a gateway can tell its visitor's limit from its own.
const (
	limitCodeTokenMessages     = "token_messages_rate_limited"
	limitCodeTokenSessions     = "token_sessions_rate_limited"
	limitCodeTokenConcurrent   = "token_concurrent_runs_limited"
	limitCodeEndUserMessages   = "end_user_messages_rate_limited"
	limitCodeEndUserSessions   = "end_user_sessions_rate_limited"
	limitCodeEndUserDailyToken = "end_user_daily_tokens_exceeded"
	limitCodeBucketCapacity    = "rate_limit_capacity_reached"
)

// rateBucket is one limit a request must pass: capacity units per window.
type rateBucket struct {
	key      string
	capacity int
	window   time.Duration
	code     string
	scope    string // "token" or "end_user"
	message  string
}

// Rate-limit buckets live in Pebble (pebblestore.RateLimitStore), so a restart
// does not hand every token and end user a fresh allowance. A bucket is
// written only when a request spends from it and is deleted once it has
// refilled (at most one window after its last use), so the table holds only
// recently active tokens and end users; agentBoundMaxBuckets bounds it.
const (
	agentBoundBucketSweepEvery = time.Minute
	agentBoundMaxBuckets       = 500000
)

type agentBoundRateLimiter struct {
	mu        sync.Mutex
	store     *pebblestore.RateLimitStore
	lastSweep time.Time
	live      int
}

func newAgentBoundRateLimiter(store *pebblestore.RateLimitStore) *agentBoundRateLimiter {
	return &agentBoundRateLimiter{store: store}
}

// take spends one unit from every bucket, or from none: it returns the first
// bucket that is empty and how long until it has a unit again. An error means
// the limits could not be read or recorded; the caller must refuse the request.
func (l *agentBoundRateLimiter) take(buckets []rateBucket, now time.Time) (*rateBucket, time.Duration, error) {
	if l == nil || l.store == nil {
		return nil, 0, errors.New("rate limits are not configured")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	nowMs := now.UnixMilli()
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= agentBoundBucketSweepEvery || now.Before(l.lastSweep) {
		live, err := l.store.DeleteExpired(nowMs)
		if err != nil {
			return nil, 0, err
		}
		l.live, l.lastSweep = live, now
	}
	next := make([]pebblestore.RateLimitBucketRecord, 0, len(buckets))
	created := 0
	for i := range buckets {
		bucket := &buckets[i]
		if bucket.capacity <= 0 || bucket.window <= 0 {
			return nil, 0, fmt.Errorf("rate limit %q has no capacity", bucket.key)
		}
		rate := float64(bucket.capacity) / bucket.window.Seconds()
		tokens := float64(bucket.capacity)
		record, found, err := l.store.Get(bucket.key)
		if err != nil {
			return nil, 0, err
		}
		if found && record.ExpiresAtUnixMs > nowMs {
			elapsed := float64(max(0, nowMs-record.LastUnixMs)) / 1000
			left := record.Tokens
			if raised := bucket.capacity - record.Capacity; record.Capacity > 0 && raised > 0 {
				// The owner raised the limit: the extra allowance applies now.
				left += float64(raised)
			}
			tokens = min(float64(bucket.capacity), left+elapsed*rate)
		} else {
			created++
		}
		if tokens < 1 {
			return bucket, time.Duration((1-tokens)/rate*float64(time.Second)) + time.Millisecond, nil
		}
		tokens--
		refill := time.Duration((float64(bucket.capacity) - tokens) / rate * float64(time.Second))
		next = append(next, pebblestore.RateLimitBucketRecord{Key: bucket.key, Capacity: bucket.capacity, Tokens: tokens, LastUnixMs: nowMs, ExpiresAtUnixMs: nowMs + refill.Milliseconds() + 1})
	}
	if created > 0 && l.live+created > agentBoundMaxBuckets {
		return &rateBucket{code: limitCodeBucketCapacity, scope: "daemon", message: "too many active rate limits; try again shortly"}, agentBoundBucketSweepEvery, nil
	}
	if err := l.store.PutAll(next); err != nil {
		return nil, 0, err
	}
	l.live += created
	return nil, 0, nil
}

// writeAgentBoundLimit answers 429 with Retry-After and a code naming the limit.
func writeAgentBoundLimit(w http.ResponseWriter, code, scope, message string, wait time.Duration) {
	seconds := max(1, int(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": message, "code": code, "limit_scope": scope, "retry_after_seconds": seconds})
}

// endUserBucketKey is case-sensitive: end-user ids are hex-encoded.
func endUserBucketKey(accountScopeID, agent, endUser, kind string) string {
	return "end_user/" + accountScopeID + "/" + agent + "/" + pebblestore.EndUserScopeID(endUser) + "/" + kind
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
	_, err := s.agentBoundRequest(r, rec)
	return err
}

// agentBoundBadRequest is a refusal of malformed input (400), not of access.
type agentBoundBadRequest struct{ error }

// agentBoundRequest checks r like agentBoundRequestError and returns the end
// user the request acts for: the create body's end_user, or the stored end
// user of the session it addresses ("" when there is none).
func (s *Server) agentBoundRequest(r *http.Request, rec *pebblestore.ScopedTokenRecord) (string, error) {
	agent := strings.TrimSpace(rec.AgentName)
	if err := s.sealedAgentError(rec.AccountScopeID, agent); err != nil {
		return "", err
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) == 2 && p[0] == "v3" && (p[1] == "sessions:usage-limits" || p[1] == "sessions:end-user-usage") && r.Method == http.MethodGet {
		// Read-only: lets the gateway's budget breaker see today's spend,
		// and its own agent's usage per end user.
		return "", nil
	}
	if len(p) < 2 || p[0] != "v3" || p[1] != "sessions" {
		return "", errors.New("route unavailable to an agent-bound token")
	}
	if len(p) == 2 {
		if rec.AgentOperator {
			return "", errors.New("an operator token cannot list or create sessions")
		}
		if r.Method != http.MethodPost {
			return "", errors.New("an agent-bound token cannot list sessions")
		}
		return checkAgentBoundCreateEndUser(r, agent)
	}
	sessionID := p[2]
	if sessionID == "" || sessionID == "." || sessionID == ".." || s.sessions == nil {
		return "", errors.New("session not found")
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok || session.AccountScopeID != rec.AccountScopeID {
		return "", errors.New("session not found")
	}
	for _, key := range []string{"agent_name", "resolved_agent_name"} {
		if value := sessionsV3MetadataString(session.Metadata, key); value != "" && value != agent {
			return "", errors.New("session not found")
		}
	}
	if sessionsV3MetadataString(session.Metadata, "agent_name") != agent {
		return "", errors.New("session not found")
	}
	binding, _, err := pebblestore.SessionEndUserFromMetadata(session.Metadata)
	if err != nil {
		return "", err
	}
	rest := strings.Join(p[3:], "/")
	if rec.AgentOperator {
		return binding.EndUser, agentOperatorRouteError(r.Method, rest)
	}
	switch {
	case rest == "messages" && r.Method == http.MethodPost:
		return binding.EndUser, checkAgentBoundMessage(r)
	case rest == "" && r.Method == http.MethodGet,
		rest == "messages" && r.Method == http.MethodGet,
		rest == "permissions" && r.Method == http.MethodGet,
		rest == "confirmations" && r.Method == http.MethodGet,
		rest == "run/stop" && r.Method == http.MethodPost:
		return binding.EndUser, nil
	case len(p) == 5 && p[3] == "confirmations" && p[4] != "" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		// The gateway relays the person's approval or decline of a confirmed
		// client tool call in this session (the handler checks the ID belongs
		// to it and that an approval carries the shown arguments' hash).
		return binding.EndUser, nil
	case len(p) == 6 && p[3] == "permissions" && p[5] == "resolve" && r.Method == http.MethodPost:
		return binding.EndUser, s.checkAgentBoundResolve(rec.AccountScopeID, sessionID, p[4])
	}
	return "", errors.New("route unavailable to an agent-bound token")
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
	_, err := checkAgentBoundCreateEndUser(r, agent)
	return err
}

// checkAgentBoundCreateEndUser checks a gateway's create body and returns its
// end_user, which must be well formed before it names a rate-limit bucket.
func checkAgentBoundCreateEndUser(r *http.Request, agent string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, agentBoundCreateBodyLimit+1))
	if err != nil || len(raw) > agentBoundCreateBodyLimit {
		return "", errors.New("session create body is invalid")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", errors.New("session create body is invalid")
	}
	if name, _ := body["agent_name"].(string); strings.TrimSpace(name) != agent {
		return "", fmt.Errorf("an agent-bound token can only create %q sessions", agent)
	}
	for key := range body {
		if !agentBoundCreateFields[key] {
			return "", fmt.Errorf("an agent-bound token cannot set %q", key)
		}
	}
	value, present := body["end_user"]
	if !present {
		return "", nil
	}
	endUser, ok := value.(string)
	if !ok {
		return "", agentBoundBadRequest{errors.New("end_user must be a string")}
	}
	if err := pebblestore.ValidateEndUser(endUser); err != nil {
		return "", agentBoundBadRequest{err}
	}
	return endUser, nil
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
	endUser, err := s.agentBoundRequest(r, rec)
	if err != nil {
		status := http.StatusForbidden
		if errors.As(err, new(agentBoundBadRequest)) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return false
	}
	if r.Method != http.MethodPost {
		return true
	}
	path := strings.Trim(r.URL.Path, "/")
	agent := strings.TrimSpace(rec.AgentName)
	now := time.Now()
	var buckets []rateBucket
	switch {
	case path == "v3/sessions":
		buckets = append(buckets, rateBucket{key: "token/" + rec.ID + "/sessions", capacity: agentBoundRateFor(rec.SessionsPerHour, agentBoundDefaultSessionsPerHour), window: time.Hour,
			code: limitCodeTokenSessions, scope: "token", message: "gateway token new-conversation rate limit reached"})
		if endUser != "" {
			buckets = append(buckets, rateBucket{key: endUserBucketKey(rec.AccountScopeID, agent, endUser, "sessions"), capacity: agentBoundRateFor(rec.EndUserSessionsPerHour, agentBoundDefaultEndUserSessionsPerHour), window: time.Hour,
				code: limitCodeEndUserSessions, scope: "end_user", message: "this end user's new-conversation rate limit reached"})
		}
	case strings.HasSuffix(path, "/messages"):
		if endUser != "" && rec.EndUserMaxTokensPerDay > 0 {
			if s.sessions == nil || s.sessions.Store() == nil {
				writeError(w, http.StatusServiceUnavailable, errors.New("end-user usage is unavailable"))
				return false
			}
			day := now.UTC()
			used, _, err := s.sessions.Store().GetUsageScopeDay(rec.AccountScopeID, pebblestore.UsageScopeKindEndUser, agent, pebblestore.EndUserScopeID(endUser), day.Format("2006-01-02"))
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, fmt.Errorf("end-user usage is unavailable: %w", err))
				return false
			}
			if used.TotalTokens >= rec.EndUserMaxTokensPerDay {
				midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
				writeAgentBoundLimit(w, limitCodeEndUserDailyToken, "end_user", "this end user's model tokens for today are used up", midnight.Sub(day))
				return false
			}
		}
		buckets = append(buckets, rateBucket{key: "token/" + rec.ID + "/messages", capacity: agentBoundRateFor(rec.MessagesPerMinute, agentBoundDefaultMessagesPerMinute), window: time.Minute,
			code: limitCodeTokenMessages, scope: "token", message: "gateway token message rate limit reached"})
		if endUser != "" {
			buckets = append(buckets, rateBucket{key: endUserBucketKey(rec.AccountScopeID, agent, endUser, "messages"), capacity: agentBoundRateFor(rec.EndUserMessagesPerMinute, agentBoundDefaultEndUserMessagesPerMinute), window: time.Minute,
				code: limitCodeEndUserMessages, scope: "end_user", message: "this end user's message rate limit reached"})
		}
	case strings.Contains(path, "/confirmations/"):
		// A decision starts the run that tells the model the outcome: it is
		// held to the concurrent-run limit below, not to the message rate.
	default:
		return true
	}
	limited, wait, err := s.agentBoundLimits.take(buckets, now)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("rate limits are unavailable: %w", err))
		return false
	}
	if limited != nil {
		writeAgentBoundLimit(w, limited.code, limited.scope, limited.message, wait)
		return false
	}
	if path != "v3/sessions" {
		// A message starts a model run: bound how many run at once.
		sessionID := strings.Split(path, "/")[2]
		limit := agentBoundRateFor(rec.MaxConcurrentRuns, agentBoundDefaultConcurrentRuns)
		if !agentBoundRuns.admit(rec.ID, sessionID, s.runningAgentSessions(rec.AccountScopeID, agent), limit, time.Now()) {
			writeAgentBoundLimit(w, limitCodeTokenConcurrent, "token", "gateway token concurrent run limit reached", 2*time.Second)
			return false
		}
	}
	return true
}
