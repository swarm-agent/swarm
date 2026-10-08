package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: permission policy (bypass, rules, capability policies and
// rules saved by allow_always/deny_always) belongs to the machine owner.
// Scoped tokens (SDK, Swarm Control relay devices, workers) can never change or
// read it, whatever their scopes, and may only allow or deny a request once.
// When the daemon starts with --lock-permission-policy, nobody can change it
// (the owner included) until a restart, bypass stays off, and one-off
// decisions still work. Threats: a scoped or remote client, or an agent with
// owner-socket access, enabling global bypass or saving an allow-all rule, so
// the owner loses control of approvals. Owners: Server.handlePermissions,
// rejectScopedPersistentResolve on both resolve routes, and the permission
// Service lock enforced in persistPolicyLocked and SetBypassPermissions. The
// full authenticated API handler over real temporary stores is the narrowest
// layer that exercises token authentication, these routes and the service
// together; startup flag parsing is covered by TestParseLockPermissionPolicy.
func TestPermissionPolicyIsOwnerOnlyAndLockable(t *testing.T) {
	s, attach, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	perm := permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	s.perm = perm
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	perm.SetSessionResolver(s.sessions)
	session := pebblestore.SessionSnapshot{ID: "policy-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "p", Mode: "auto"}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Session: &session, IdempotencyKey: "p-create", PayloadHash: "p-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	pending := func(call string) string {
		t.Helper()
		record, err := perm.CreatePending(permission.CreateInput{SessionID: session.ID, RunID: "run-" + call, CallID: call, ToolName: "bash", ToolArguments: `{"command":"ls"}`, Requirement: "approval", Mode: "auto"})
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	scoped, _, err := sec.CreateScopedToken("all-scopes", []string{"admin", "sessions:read", "sessions:write", "settings:write", "automations:write"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:7781"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Swarm-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	rules := func() int {
		t.Helper()
		policy, err := perm.CurrentPolicyForAccount(actor.AccountScopeID)
		if err != nil {
			t.Fatal(err)
		}
		return len(policy.Rules)
	}
	status := func(id string) string {
		t.Helper()
		records, err := perm.ListPermissions(session.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.ID == id {
				return record.Status
			}
		}
		t.Fatalf("permission %s missing", id)
		return ""
	}
	allRule := `{"kind":"tool","decision":"allow","tool":"bash"}`
	baseline := rules()
	resolvePath := func(id string) string { return "/v3/sessions/" + session.ID + "/permissions/" + id + "/resolve" }

	// Scoped tokens: refused everywhere in policy, whatever their scopes.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/permissions/bypass", `{"enabled":true}`},
		{http.MethodPost, "/v1/permissions", allRule},
		{http.MethodGet, "/v1/permissions", ""},
		{http.MethodPost, "/v1/permissions/reset", "{}"},
		{http.MethodPut, "/v1/permissions/capabilities", `{"session_deploy":{"mode":"always_allow"}}`},
	} {
		if w := call(scoped, tc.method, tc.path, tc.body); w.Code != http.StatusForbidden {
			t.Fatalf("scoped %s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	scopedAlways := pending("scoped-always")
	for _, action := range []string{"allow_always", "always_allow", "deny_always"} {
		if w := call(scoped, http.MethodPost, resolvePath(scopedAlways), `{"action":"`+action+`"}`); w.Code != http.StatusForbidden {
			t.Fatalf("scoped %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	if perm.BypassPermissions() || rules() != baseline || status(scopedAlways) != "pending" {
		t.Fatalf("scoped token changed policy: bypass=%v rules=%d status=%s", perm.BypassPermissions(), rules(), status(scopedAlways))
	}
	if w := call(scoped, http.MethodPost, resolvePath(scopedAlways), `{"action":"allow_once"}`); w.Code != http.StatusOK || status(scopedAlways) == "pending" {
		t.Fatalf("scoped one-off decision refused: %d %s", w.Code, w.Body.String())
	}

	// Owner without the lock: policy changes work.
	if w := call(attach, http.MethodPost, "/v1/permissions", allRule); w.Code != http.StatusOK || rules() != baseline+1 {
		t.Fatalf("owner rule refused: %d %s", w.Code, w.Body.String())
	}

	// Locked: nobody changes policy; bypass stays off; one-off decisions work.
	perm.LockPolicy()
	perm.SetBypassPermissions(true)
	if perm.BypassPermissions() {
		t.Fatal("bypass enabled on a locked policy")
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/permissions/bypass", `{"enabled":true}`},
		{http.MethodPost, "/v1/permissions", `{"kind":"tool","decision":"allow","tool":"write"}`},
		{http.MethodPost, "/v1/permissions/reset", "{}"},
	} {
		w := call(attach, tc.method, tc.path, tc.body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "locked") {
			t.Fatalf("locked owner %s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	ownerAlways := pending("owner-always")
	if w := call(attach, http.MethodPost, resolvePath(ownerAlways), `{"action":"allow_always"}`); w.Code != http.StatusForbidden {
		t.Fatalf("locked allow_always: %d %s", w.Code, w.Body.String())
	}
	if perm.BypassPermissions() || rules() != baseline+1 || status(ownerAlways) != "pending" {
		t.Fatalf("locked policy changed: bypass=%v rules=%d status=%s", perm.BypassPermissions(), rules(), status(ownerAlways))
	}
	if _, err := perm.UpdateSubagentPolicyMapForAccount(actor.AccountScopeID, map[string]any{"active_child_limit": 2}); err == nil {
		t.Fatal("agent orchestration policy changed on a locked policy")
	}
	w := call(attach, http.MethodPost, resolvePath(ownerAlways), `{"action":"allow_once"}`)
	var resolved struct {
		Permission pebblestore.PermissionRecord `json:"permission"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resolved) != nil || resolved.Permission.Status == "pending" {
		t.Fatalf("locked one-off decision refused: %d %s", w.Code, w.Body.String())
	}
	if w := call(attach, http.MethodGet, "/v1/permissions", ""); w.Code != http.StatusOK {
		t.Fatalf("owner cannot read a locked policy: %d %s", w.Code, w.Body.String())
	}
}
