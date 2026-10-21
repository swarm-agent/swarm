package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// endUserGatewayFixture is a server with two sealed agents (frontdesk,
// backdesk) and seeded sessions; minting goes through the owner-only handler.
type endUserGatewayFixture struct {
	s       *Server
	account string
	userID  string
}

func newEndUserGatewayFixture(t *testing.T) *endUserGatewayFixture {
	t.Helper()
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	t.Cleanup(cleanup)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "end-user-agents"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	f := &endUserGatewayFixture{s: s, account: actor.AccountScopeID, userID: actor.UserID}
	if _, err := s.agents.PutCustomToolForAccount(f.account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frontdesk", "backdesk"} {
		if _, _, _, err := s.agents.UpsertForAccount(f.account, agentruntime.UpsertInput{Name: name, Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// seed stores a session of agent, bound to endUser when it is not empty, the
// way the create handler stores it.
func (f *endUserGatewayFixture) seed(t *testing.T, id, agent, endUser string, labels map[string]string) {
	t.Helper()
	metadata := map[string]any{"agent_name": agent}
	if endUser != "" || len(labels) > 0 {
		metadata[pebblestore.SessionEndUserMetadataKey] = pebblestore.EndUserMetadata(agent, endUser, labels)
	}
	snapshot := pebblestore.SessionSnapshot{ID: id, UserID: f.userID, AccountScopeID: f.account, Title: id, Mode: "auto", Metadata: metadata}
	if _, err := f.s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: f.userID, AccountScopeID: f.account, Session: &snapshot, IdempotencyKey: id, PayloadHash: id, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
}

func (f *endUserGatewayFixture) mint(t *testing.T, body string) (int, pebblestore.ScopedTokenRecord) {
	t.Helper()
	w := httptest.NewRecorder()
	f.s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), f.userID, f.account))
	var out struct {
		Record pebblestore.ScopedTokenRecord `json:"record"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Record
}

// gate runs only the agent-bound gate, which owns every limit decision.
func (f *endUserGatewayFixture) gate(rec pebblestore.ScopedTokenRecord, method, path, body string) (bool, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	return f.s.gateAgentBoundToken(w, httptest.NewRequest(method, path, strings.NewReader(body)), &rec), w
}

func message(n int) string {
	return `{"role":"user","content":"hi","client_request_id":"m` + strconv.Itoa(n) + `"}`
}

func limitCode(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Code       string `json:"code"`
		LimitScope string `json:"limit_scope"`
		RetryAfter int    `json:"retry_after_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body: %v %s", err, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" || body.RetryAfter < 1 || strconv.Itoa(body.RetryAfter) != w.Header().Get("Retry-After") {
		t.Fatalf("429 without a matching Retry-After: header=%q body=%s", w.Header().Get("Retry-After"), w.Body.String())
	}
	return body.Code, body.LimitScope
}

// Purpose: a gateway token's per-end-user limits are enforced server-side and
// told apart from its own: one visitor hitting their message or new-session
// limit gets 429 with code end_user_* and limit_scope end_user, other visitors
// and sessions without an end_user carry on, and a refused request spends
// nothing from the token's own bucket. The token's limit still caps everyone
// with code token_*. Malformed end_user ids are 400 before naming a bucket.
// Threat: one visitor (or a script behind one visitor) using up a public
// agent's whole allowance or cost. Boundary: Server.gateAgentBoundToken,
// agentBoundRateLimiter.take. Narrowest layer: the gate with real minted
// tokens and seeded sessions; handlers and model runs are not needed.
func TestAgentBoundEndUserLimitsAreEnforcedAndDistinct(t *testing.T) {
	f := newEndUserGatewayFixture(t)
	f.seed(t, "alice-1", "frontdesk", "alice", nil)
	f.seed(t, "alice-2", "frontdesk", "alice", nil)
	f.seed(t, "bob-1", "frontdesk", "bob", nil)
	f.seed(t, "carol-1", "frontdesk", "carol", nil)
	f.seed(t, "anon-1", "frontdesk", "", nil)

	code, rec := f.mint(t, `{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600,"messages_per_minute":3,"max_concurrent_runs":64,"end_user_messages_per_minute":1,"end_user_sessions_per_hour":1}`)
	if code != http.StatusOK || rec.EndUserMessagesPerMinute != 1 || rec.EndUserSessionsPerHour != 1 {
		t.Fatalf("mint: %d %+v", code, rec)
	}
	if ok, w := f.gate(rec, "POST", "/v3/sessions/alice-1/messages", message(1)); !ok {
		t.Fatalf("alice's first message refused: %d %s", w.Code, w.Body.String())
	}
	// Alice's second message, in her other conversation too, is over her limit.
	ok, w := f.gate(rec, "POST", "/v3/sessions/alice-2/messages", message(2))
	if ok || w.Code != http.StatusTooManyRequests {
		t.Fatalf("alice's second message: ok=%v %d %s", ok, w.Code, w.Body.String())
	}
	if code, scope := limitCode(t, w); code != limitCodeEndUserMessages || scope != "end_user" {
		t.Fatalf("alice's refusal is %s/%s", code, scope)
	}
	// The refusal spent nothing from the token: bob and carol still fit in 3.
	for i, session := range []string{"bob-1", "carol-1"} {
		if ok, w := f.gate(rec, "POST", "/v3/sessions/"+session+"/messages", message(10+i)); !ok {
			t.Fatalf("%s refused after alice's refusal: %d %s", session, w.Code, w.Body.String())
		}
	}
	ok, w = f.gate(rec, "POST", "/v3/sessions/anon-1/messages", message(20))
	if ok || w.Code != http.StatusTooManyRequests {
		t.Fatalf("a fourth message over the token's 3/min: ok=%v %d", ok, w.Code)
	}
	if code, scope := limitCode(t, w); code != limitCodeTokenMessages || scope != "token" {
		t.Fatalf("the token's refusal is %s/%s", code, scope)
	}

	// New conversations: one per end user per hour; none without end_user here
	// beyond the token's default 300.
	create := func(endUser string) (bool, *httptest.ResponseRecorder) {
		body := `{"agent_name":"frontdesk","client_request_id":"c-` + endUser + `"`
		if endUser != "" {
			body += `,"end_user":"` + endUser + `"`
		}
		return f.gate(rec, "POST", "/v3/sessions", body+`}`)
	}
	if ok, w := create("dave"); !ok {
		t.Fatalf("dave's first conversation refused: %d %s", w.Code, w.Body.String())
	}
	ok, w = create("dave")
	if ok || w.Code != http.StatusTooManyRequests {
		t.Fatalf("dave's second conversation in an hour: ok=%v %d", ok, w.Code)
	}
	if code, scope := limitCode(t, w); code != limitCodeEndUserSessions || scope != "end_user" {
		t.Fatalf("dave's refusal is %s/%s", code, scope)
	}
	if ok, w := create("erin"); !ok {
		t.Fatalf("another end user was refused: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`"a b"`, `"` + strings.Repeat("x", 129) + `"`, `"../x"`, `""`, `7`} {
		ok, w := f.gate(rec, "POST", "/v3/sessions", `{"agent_name":"frontdesk","end_user":`+bad+`}`)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("malformed end_user %s: ok=%v %d %s", bad, ok, w.Code, w.Body.String())
		}
	}

	// Another token for the same agent shares the agent's end-user buckets:
	// rotating the gateway token does not hand alice a fresh allowance.
	_, rotated := f.mint(t, `{"name":"gw2","agent_name":"frontdesk","expires_in_seconds":3600,"end_user_messages_per_minute":1}`)
	if ok, w := f.gate(rotated, "POST", "/v3/sessions/alice-1/messages", message(30)); ok || w.Code != http.StatusTooManyRequests {
		t.Fatalf("a rotated token reset alice's limit: ok=%v %d", ok, w.Code)
	}
}

// Purpose: per-end-user and per-token rate-limit counters live in the daemon
// store, so a restart (closing and reopening Pebble) keeps every bucket's
// level; a bucket refills with time, and buckets that have refilled are
// deleted so the table stays bounded. Threat: restarting (or crashing) the
// daemon resets every limit, letting a burst through again. Boundary:
// agentBoundRateLimiter.take, pebblestore.RateLimitStore. Narrowest layer: the
// limiter on a real Pebble store with a fixed clock, reopened.
func TestAgentBoundRateLimitsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limits")
	open := func() (*pebblestore.Store, *agentBoundRateLimiter) {
		db, err := pebblestore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return db, newAgentBoundRateLimiter(pebblestore.NewRateLimitStore(db))
	}
	buckets := func() []rateBucket {
		return []rateBucket{
			{key: "token/tok_a/messages", capacity: 5, window: time.Minute, code: limitCodeTokenMessages, scope: "token"},
			{key: endUserBucketKey("acct", "frontdesk", "Alice", "messages"), capacity: 2, window: time.Minute, code: limitCodeEndUserMessages, scope: "end_user"},
		}
	}
	now := time.UnixMilli(1_800_000_000_000)
	db, limiter := open()
	for i := 0; i < 2; i++ {
		if limited, _, err := limiter.take(buckets(), now); err != nil || limited != nil {
			t.Fatalf("take %d: %v %+v", i, err, limited)
		}
	}
	if limited, _, err := limiter.take(buckets(), now); err != nil || limited == nil || limited.code != limitCodeEndUserMessages {
		t.Fatalf("third take within the minute: %v %+v", err, limited)
	}
	// A case-different end user is someone else (ids are case-sensitive).
	other := []rateBucket{{key: endUserBucketKey("acct", "frontdesk", "alice", "messages"), capacity: 2, window: time.Minute}}
	if limited, _, err := limiter.take(other, now); err != nil || limited != nil {
		t.Fatalf("a case-different end user shared Alice's bucket: %v %+v", err, limited)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, limiter = open()
	limited, wait, err := limiter.take(buckets(), now.Add(time.Second))
	if err != nil || limited == nil || limited.code != limitCodeEndUserMessages || wait <= 0 || wait > time.Minute {
		t.Fatalf("after a restart the end user's limit was reset: %v %+v wait=%v", err, limited, wait)
	}
	// The token's bucket survived too: 2 of 5 spent, so 3 remain.
	tokenOnly := func() []rateBucket {
		return []rateBucket{{key: "token/tok_a/messages", capacity: 5, window: time.Minute}}
	}
	for i := 0; i < 3; i++ {
		if limited, _, err := limiter.take(tokenOnly(), now.Add(time.Second)); err != nil || limited != nil {
			t.Fatalf("token take %d after restart: %v %+v", i, err, limited)
		}
	}
	if limited, _, err := limiter.take(tokenOnly(), now.Add(time.Second)); err != nil || limited == nil {
		t.Fatalf("the token's bucket was reset by the restart: %v %+v", err, limited)
	}
	// After a full window everything has refilled: the next take is admitted and
	// the sweep leaves only the buckets that take just wrote.
	later := now.Add(2*time.Minute + time.Second)
	if limited, _, err := limiter.take(buckets(), later); err != nil || limited != nil {
		t.Fatalf("after the window: %v %+v", err, limited)
	}
	live, err := pebblestore.NewRateLimitStore(db).DeleteExpired(later.UnixMilli())
	if err != nil || live != 2 {
		t.Fatalf("live buckets after the sweep: %d %v (want the 2 just spent)", live, err)
	}
	if live, err := pebblestore.NewRateLimitStore(db).DeleteExpired(later.Add(2 * time.Minute).UnixMilli()); err != nil || live != 0 {
		t.Fatalf("refilled buckets were kept: %d %v", live, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Without a store the limiter refuses instead of allowing.
	if _, _, err := (*agentBoundRateLimiter)(nil).take(buckets(), now); err == nil {
		t.Fatal("a limiter without a store admitted a request")
	}
}

// Purpose: an end user's daily model-token cap is enforced from real receipts:
// once the receipts of that end user's sessions today reach the token's
// end_user_max_tokens_per_day, their next message is refused (429
// end_user_daily_tokens_exceeded, Retry-After until UTC midnight) while other
// end users carry on; GET /v3/sessions:end-user-usage reports the same totals
// grouped by end user, only for the token's own agent. Threat: one visitor
// running up the account's spend. Boundary: pebblestore.resolveUsageScopes
// (end_user scope), Server.gateAgentBoundToken, handleSessionsV3EndUserUsage.
// Narrowest layer: the receipt writer, the gate and the usage handler on one
// store; no provider is called.
func TestAgentBoundEndUserDailyTokensAndUsage(t *testing.T) {
	f := newEndUserGatewayFixture(t)
	f.seed(t, "alice-1", "frontdesk", "alice", nil)
	f.seed(t, "alice-2", "frontdesk", "alice", nil)
	f.seed(t, "bob-1", "frontdesk", "bob", nil)
	f.seed(t, "anon-1", "frontdesk", "", nil)
	f.seed(t, "back-1", "backdesk", "alice", nil)
	_, rec := f.mint(t, `{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600,"end_user_max_tokens_per_day":100}`)
	_, backdesk := f.mint(t, `{"name":"gw-back","agent_name":"backdesk","expires_in_seconds":3600}`)
	receipt := func(session, run string, tokens int64) {
		t.Helper()
		if err := f.s.sessions.Store().PutTurnUsage(pebblestore.SessionTurnUsageSnapshot{SessionID: session, RunID: run, AccountScopeID: f.account, UserID: f.userID,
			Provider: "codex", Model: "gpt-5.5", InputTokens: tokens - 10, OutputTokens: 10, TotalTokens: tokens, PriceStatus: "subscription", CreatedAt: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	receipt("alice-1", "r1", 60)
	if ok, w := f.gate(rec, "POST", "/v3/sessions/alice-2/messages", message(1)); !ok {
		t.Fatalf("alice under her cap was refused: %d %s", w.Code, w.Body.String())
	}
	receipt("alice-2", "r2", 50)
	receipt("bob-1", "r3", 30)
	receipt("anon-1", "r4", 500)
	receipt("back-1", "r5", 999)
	ok, w := f.gate(rec, "POST", "/v3/sessions/alice-1/messages", message(2))
	if ok || w.Code != http.StatusTooManyRequests {
		t.Fatalf("alice over her daily cap: ok=%v %d %s", ok, w.Code, w.Body.String())
	}
	if code, scope := limitCode(t, w); code != limitCodeEndUserDailyToken || scope != "end_user" {
		t.Fatalf("daily refusal is %s/%s", code, scope)
	}
	if retry, _ := strconv.Atoi(w.Header().Get("Retry-After")); retry < 1 || retry > 86400 {
		t.Fatalf("Retry-After %d is not within today", retry)
	}
	for _, session := range []string{"bob-1", "anon-1"} {
		if ok, w := f.gate(rec, "POST", "/v3/sessions/"+session+"/messages", message(3)); !ok {
			t.Fatalf("%s refused by alice's cap: %d %s", session, w.Code, w.Body.String())
		}
	}

	usage := func(token *pebblestore.ScopedTokenRecord, query string) (int, map[string]int64, bool) {
		t.Helper()
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/sessions:end-user-usage"+query, nil), f.userID, f.account)
		if token != nil {
			if !f.s.gateAgentBoundToken(httptest.NewRecorder(), r, token) {
				return http.StatusForbidden, nil, false
			}
			r = requestWithScopedToken(r, token)
		}
		w := httptest.NewRecorder()
		f.s.handleSessionsV3EndUserUsage(w, r)
		var out struct {
			EndUsers []struct {
				EndUser  string                      `json:"end_user"`
				Recorded bool                        `json:"recorded"`
				Usage    pebblestore.UsageScopeTotal `json:"usage"`
			} `json:"end_users"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		totals := map[string]int64{}
		recorded := true
		for _, row := range out.EndUsers {
			totals[row.EndUser] = row.Usage.TotalTokens
			recorded = recorded && row.Recorded
		}
		return w.Code, totals, recorded
	}
	code, totals, _ := usage(&rec, "")
	if code != http.StatusOK || len(totals) != 2 || totals["alice"] != 110 || totals["bob"] != 30 {
		t.Fatalf("gateway usage by end user: %d %v (want alice 110, bob 30; backdesk and no-end-user sessions excluded)", code, totals)
	}
	if code, totals, recorded := usage(&rec, "?end_user=nobody"); code != http.StatusOK || totals["nobody"] != 0 || recorded {
		t.Fatalf("an end user with no usage: %d %v recorded=%v", code, totals, recorded)
	}
	if code, _, _ := usage(&backdesk, "?agent_name=frontdesk"); code != http.StatusForbidden {
		t.Fatalf("another agent's gateway read frontdesk usage: %d", code)
	}
	if code, totals, _ := usage(&backdesk, ""); code != http.StatusOK || len(totals) != 1 || totals["alice"] != 999 {
		t.Fatalf("backdesk usage: %d %v", code, totals)
	}
	if code, totals, _ := usage(nil, "?agent_name=frontdesk"); code != http.StatusOK || totals["alice"] != 110 {
		t.Fatalf("owner usage for frontdesk: %d %v", code, totals)
	}
	if code, _, _ := usage(nil, ""); code != http.StatusBadRequest {
		t.Fatalf("owner usage without agent_name: %d", code)
	}
}

// Purpose: the owner can change a gateway token's limits in place
// (POST /v3/auth/tokens/{id}/limits) and the gate uses them from the next
// request; only named fields change, out-of-range values, unknown fields,
// operator tokens and other accounts' or unknown ids are refused with nothing
// changed, and a gateway token cannot reach the route at all. Threat: a leaked
// gateway token raising its own limits; an edit silently resetting others.
// Boundary: handleAuthTokenLimits, security.Service.UpdateAgentBoundLimits,
// agentBoundRequestError. Narrowest layer: the owner handler and the gate.
func TestAgentBoundTokenLimitsAreEditableByTheOwnerOnly(t *testing.T) {
	f := newEndUserGatewayFixture(t)
	f.seed(t, "alice-1", "frontdesk", "alice", nil)
	_, rec := f.mint(t, `{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600,"messages_per_minute":50,"end_user_messages_per_minute":1}`)
	_, operator := f.mint(t, `{"name":"hub","agent_name":"frontdesk","operator":true,"expires_in_seconds":3600}`)
	edit := func(id, body string) (int, pebblestore.ScopedTokenRecord) {
		w := httptest.NewRecorder()
		f.s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens/"+id+"/limits", strings.NewReader(body)), f.userID, f.account))
		var out struct {
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Record
	}
	current := func() pebblestore.ScopedTokenRecord {
		tokens, err := f.s.security.ListScopedTokens(f.account)
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range tokens {
			if token.ID == rec.ID {
				return token
			}
		}
		t.Fatal("token missing")
		return pebblestore.ScopedTokenRecord{}
	}
	if ok, _ := f.gate(rec, "POST", "/v3/sessions/alice-1/messages", message(1)); !ok {
		t.Fatal("first message refused")
	}
	if ok, _ := f.gate(rec, "POST", "/v3/sessions/alice-1/messages", message(2)); ok {
		t.Fatal("second message over 1/min admitted")
	}
	for _, tc := range []struct {
		id, body string
		want     int
	}{
		{rec.ID, `{"end_user_messages_per_minute":6001}`, http.StatusBadRequest},
		{rec.ID, `{"end_user_max_tokens_per_day":-1}`, http.StatusBadRequest},
		{rec.ID, `{"max_concurrent_runs":65}`, http.StatusBadRequest},
		{rec.ID, `{"agent_name":"swarm"}`, http.StatusBadRequest},
		{rec.ID, `{}`, http.StatusBadRequest},
		{operator.ID, `{"end_user_messages_per_minute":5}`, http.StatusBadRequest},
		{"tok_missing", `{"end_user_messages_per_minute":5}`, http.StatusNotFound},
	} {
		if code, _ := edit(tc.id, tc.body); code != tc.want {
			t.Fatalf("edit %s %s: %d, want %d", tc.id, tc.body, code, tc.want)
		}
	}
	if got := current(); got.EndUserMessagesPerMinute != 1 || got.MessagesPerMinute != 50 || got.AgentName != "frontdesk" {
		t.Fatalf("a refused edit changed the token: %+v", got)
	}
	code, updated := edit(rec.ID, `{"end_user_messages_per_minute":600,"end_user_max_tokens_per_day":5000}`)
	if code != http.StatusOK || updated.EndUserMessagesPerMinute != 600 || updated.EndUserMaxTokensPerDay != 5000 || updated.MessagesPerMinute != 50 {
		t.Fatalf("edit: %d %+v", code, updated)
	}
	// The gate reads the stored record per request; use the edited one.
	edited := current()
	if ok, w := f.gate(edited, "POST", "/v3/sessions/alice-1/messages", message(3)); !ok {
		t.Fatalf("the raised limit was not applied: %d %s", w.Code, w.Body.String())
	}
	// A gateway token cannot reach the token routes, so it cannot raise itself.
	if f.s.agentBoundRequestError(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens/"+rec.ID+"/limits", strings.NewReader(`{"messages_per_minute":6000}`)), &edited) == nil {
		t.Fatal("a gateway token reached its own limits route")
	}
	// Another account cannot edit this token.
	w := httptest.NewRecorder()
	f.s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens/"+rec.ID+"/limits", strings.NewReader(`{"messages_per_minute":1}`)), f.userID, "other-account"))
	if w.Code != http.StatusNotFound || current().MessagesPerMinute != 50 {
		t.Fatalf("another account edited the token: %d %s", w.Code, w.Body.String())
	}
}

// Purpose: a gateway answering client tool calls gets the session's end user
// and labels with each delivery (GET .../permissions, what pendingCalls reads),
// taken from the stored binding, so a model that puts an end_user into its
// tool arguments changes nothing; a session without a binding has none; the
// agent-bound gate hides another agent's session and its binding. Threat: the
// model (or a visitor through it) choosing whose data a tool call sees.
// Boundary: handleSessionV3PrimaryPermissions, addSessionEndUserFields,
// agentBoundRequestError. Narrowest layer: the permissions handler over a real
// permission store with a pending client tool call.
func TestClientToolDeliveryCarriesTheStoredEndUser(t *testing.T) {
	f := newEndUserGatewayFixture(t)
	f.seed(t, "alice-1", "frontdesk", "alice", map[string]string{"plan": "pro"})
	f.seed(t, "anon-1", "frontdesk", "", nil)
	_, rec := f.mint(t, `{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600}`)
	_, backdesk := f.mint(t, `{"name":"gw-back","agent_name":"backdesk","expires_in_seconds":3600}`)
	for _, session := range []string{"alice-1", "anon-1"} {
		if _, err := f.s.perm.(*permission.Service).CreatePending(permission.CreateInput{SessionID: session, RunID: "run-1", CallID: "call-1", ToolName: "lookup_order",
			ToolArguments: `{"order_id":"ABC123","end_user":"mallory"}`, ToolCallArguments: `{"order_id":"ABC123","end_user":"mallory"}`, Requirement: permission.RequirementClientTool, Mode: "auto"}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(session string) map[string]any {
		t.Helper()
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/sessions/"+session+"/permissions?status=pending", nil), f.userID, f.account)
		if !f.s.gateAgentBoundToken(httptest.NewRecorder(), r, &rec) {
			t.Fatalf("gateway refused its own session's calls")
		}
		w := httptest.NewRecorder()
		f.s.handleSessionV3PrimaryPermissions(w, requestWithScopedToken(r, &rec), identity.Principal{UserID: f.userID, AccountScopeID: f.account}, session)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("permissions %s: %d %s", session, w.Code, w.Body.String())
		}
		return out
	}
	got := read("alice-1")
	if got["end_user"] != "alice" || got["count"].(float64) != 1 {
		t.Fatalf("delivery end_user = %v (count %v), want the stored alice", got["end_user"], got["count"])
	}
	if labels, _ := got["labels"].(map[string]any); labels["plan"] != "pro" {
		t.Fatalf("delivery labels = %v", got["labels"])
	}
	if got := read("anon-1"); got["end_user"] != nil || got["labels"] != nil {
		t.Fatalf("a session without an end user delivered %v / %v", got["end_user"], got["labels"])
	}
	if f.s.agentBoundRequestError(httptest.NewRequest(http.MethodGet, "/v3/sessions/alice-1/permissions", nil), &backdesk) == nil {
		t.Fatal("another agent's gateway read alice's tool calls")
	}
	if f.s.agentBoundRequestError(httptest.NewRequest(http.MethodGet, "/v3/sessions/alice-1", nil), &backdesk) == nil {
		t.Fatal("another agent's gateway read alice's session")
	}
}
