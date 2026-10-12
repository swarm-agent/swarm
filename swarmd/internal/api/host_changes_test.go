package api

// Purpose: host changes are the only way Swarm's view of the machine changes,
// and the machine owner is the only one who decides. These handler-level
// tests over a real store pin the contract of /v1/host/changes
// (handleHostChanges, HostChangeStore, the helper client interface):
//   - a scoped token needs host:request to request, host:request or
//     swarm:read to list, and can never approve or deny (the request stays
//     pending, nothing runs);
//   - an owner approval persists "applying", asks the helper for exactly
//     `op path`, persists applied/failed from the helper's answer, and asks
//     for the daemon restart only after that and only when the helper said
//     restart_required; a helper that is not running or refuses yields a
//     failed record and no restart;
//   - without a configured helper, requests get host_changes_unsupported and
//     listing reports supported=false;
//   - only an absolute, clean path outside Swarm's storage is accepted, an
//     identical pending request is returned instead of duplicated, and a
//     record left applying by a previous process fails as "interrupted".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/hostchange"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type fakeHostHelper struct {
	supported bool
	mu        sync.Mutex
	calls     []string
	reply     func(op, path string) (hostchange.Result, error)
	restarted chan struct{}
}

func newFakeHostHelper(reply func(op, path string) (hostchange.Result, error)) *fakeHostHelper {
	return &fakeHostHelper{supported: true, reply: reply, restarted: make(chan struct{}, 4)}
}

func (f *fakeHostHelper) Supported() bool { return f != nil && f.supported }

func (f *fakeHostHelper) Run(_ context.Context, op, path string) (hostchange.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.TrimSpace(op+" "+path))
	f.mu.Unlock()
	if op == hostchange.OpRestartDaemon {
		f.restarted <- struct{}{}
		return hostchange.Result{OK: true, Op: op}, nil
	}
	return f.reply(op, path)
}

func (f *fakeHostHelper) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newHostChangeTestServer(t *testing.T, helper *fakeHostHelper) (*Server, identity.Principal) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &Server{hostChanges: pebblestore.NewHostChangeStore(store), hostChangeRestartDelay: 10 * time.Millisecond}
	if helper != nil {
		s.hostChangeHelper = helper
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "acct", AccountScopeSource: identity.AccountScopeSourceServerState}
	return s, principal
}

func hostChangeDo(s *Server, r *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	s.handleHostChanges(rec, r)
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	return rec, payload
}

func scopedReq(method, path, body string, principal identity.Principal, scopes ...string) *http.Request {
	r := ownerReq(method, path, body, principal)
	return requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{ID: "tok_1", Name: "setup-ai", Scopes: scopes})
}

func changeOf(payload map[string]any) map[string]any {
	change, _ := payload["change"].(map[string]any)
	return change
}

func TestHostChangeOwnerApprovalRunsHelperThenRestarts(t *testing.T) {
	helper := newFakeHostHelper(func(op, path string) (hostchange.Result, error) {
		return hostchange.Result{OK: true, Op: op, Path: path, Owner: "person", RestartRequired: true, Raw: `{"ok":true,"restart_required":true}`}, nil
	})
	s, owner := newHostChangeTestServer(t, helper)

	rec, payload := hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes", `{"op":"mount-workspace","path":"/home/person/app","reason":"the person's repository"}`, owner, "host:request", "sessions:write"))
	if rec.Code != http.StatusCreated || payload["ok"] != true {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	change := changeOf(payload)
	id, _ := change["id"].(string)
	if id == "" || change["status"] != "pending" || change["requested_by"] != "token:setup-ai" || change["path"] != "/home/person/app" {
		t.Fatalf("unexpected record: %v", change)
	}
	// The same pending request is returned, not duplicated.
	rec, payload = hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes", `{"op":"mount-workspace","path":"/home/person/app","reason":"again"}`, owner, "host:request"))
	if rec.Code != http.StatusOK || changeOf(payload)["id"] != id {
		t.Fatalf("duplicate request: %d %s", rec.Code, rec.Body)
	}
	if pending, _ := s.hostChanges.List("acct", "pending"); len(pending) != 1 {
		t.Fatalf("pending records: %+v", pending)
	}
	rec, payload = hostChangeDo(s, ownerReq(http.MethodGet, "/v1/host/changes?status=pending", "", owner))
	if rec.Code != http.StatusOK || payload["supported"] != true {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if listed, _ := payload["changes"].([]any); len(listed) != 1 {
		t.Fatalf("list: %s", rec.Body)
	}

	rec, payload = hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/"+id+"/approve", `{"reason":"yes"}`, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	change = changeOf(payload)
	if change["status"] != "applied" || change["result"] != `{"ok":true,"restart_required":true}` || change["error"] != nil || change["applied_at"] == nil {
		t.Fatalf("approved record: %v", change)
	}
	// Exactly `op path` reached the helper; the restart follows the reply.
	if got := helper.received(); len(got) != 1 || got[0] != "mount-workspace /home/person/app" {
		t.Fatalf("helper calls before restart: %q", got)
	}
	select {
	case <-helper.restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("restart-daemon was not requested")
	}
	if got := helper.received(); len(got) != 2 || got[1] != "restart-daemon" {
		t.Fatalf("helper calls: %q", got)
	}
	stored, _, _ := s.hostChanges.Get("acct", id)
	if stored.Status != "applied" {
		t.Fatalf("stored record: %+v", stored)
	}
	// A decided record cannot be decided again.
	if rec, _ := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/"+id+"/deny", "", owner)); rec.Code != http.StatusConflict {
		t.Fatalf("second decision: %d %s", rec.Code, rec.Body)
	}
	// Deny settles without the helper.
	rec, payload = hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes", `{"op":"unmount-workspace","path":"/home/person/old"}`, owner))
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner request: %d %s", rec.Code, rec.Body)
	}
	other, _ := changeOf(payload)["id"].(string)
	rec, payload = hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/"+other+"/deny", `{"reason":"not that one"}`, owner))
	if rec.Code != http.StatusOK || changeOf(payload)["status"] != "denied" || changeOf(payload)["error"] != "not that one" {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
	if got := helper.received(); len(got) != 2 {
		t.Fatalf("deny reached the helper: %q", got)
	}
	if rec, _ := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/hc_missing/approve", "", owner)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
}

func TestHostChangeHelperFailuresBecomeFailedRecordsWithoutRestart(t *testing.T) {
	cases := []struct {
		name  string
		reply func(op, path string) (hostchange.Result, error)
		want  string
	}{
		{"refused", func(op, path string) (hostchange.Result, error) {
			return hostchange.Result{OK: false, Error: "folder does not exist", Raw: `{"ok":false,"error":"folder does not exist"}`}, nil
		}, "folder does not exist"},
		{"not running", func(op, path string) (hostchange.Result, error) {
			return hostchange.Result{}, fmt.Errorf("%w: dial unix: connection refused", hostchange.ErrNotRunning)
		}, "host-change helper is not running on this machine"},
		{"restart not needed", func(op, path string) (hostchange.Result, error) {
			return hostchange.Result{OK: true, Raw: `{"ok":true}`}, nil
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			helper := newFakeHostHelper(tc.reply)
			s, owner := newHostChangeTestServer(t, helper)
			rec, payload := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes", `{"op":"mount-workspace","path":"/home/person/app"}`, owner))
			if rec.Code != http.StatusCreated {
				t.Fatalf("request: %d %s", rec.Code, rec.Body)
			}
			id, _ := changeOf(payload)["id"].(string)
			rec, payload = hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/"+id+"/approve", "", owner))
			if rec.Code != http.StatusOK {
				t.Fatalf("approve: %d %s", rec.Code, rec.Body)
			}
			change := changeOf(payload)
			if tc.want == "" {
				if change["status"] != "applied" {
					t.Fatalf("record: %v", change)
				}
			} else if change["status"] != "failed" || !strings.Contains(fmt.Sprint(change["error"]), tc.want) {
				t.Fatalf("record: %v", change)
			}
			select {
			case <-helper.restarted:
				t.Fatal("restart requested although nothing requires it")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestHostChangeScopedTokensRequestButNeverDecide(t *testing.T) {
	helper := newFakeHostHelper(func(op, path string) (hostchange.Result, error) {
		return hostchange.Result{OK: true}, nil
	})
	s, owner := newHostChangeTestServer(t, helper)
	body := `{"op":"mount-workspace","path":"/home/person/app"}`

	// No host:request: neither request nor list (admin implies every scope
	// and may request; it still cannot decide, below).
	if rec, _ := hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes", body, owner, "sessions:write", "workspaces:write")); rec.Code != http.StatusForbidden {
		t.Fatalf("request without scope: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := hostChangeDo(s, scopedReq(http.MethodGet, "/v1/host/changes", "", owner, "sessions:read")); rec.Code != http.StatusForbidden {
		t.Fatalf("list without scope: %d %s", rec.Code, rec.Body)
	}
	if all, _ := s.hostChanges.List("acct", ""); len(all) != 0 {
		t.Fatalf("refused request was recorded: %+v", all)
	}
	// swarm:read lists but does not request.
	if rec, _ := hostChangeDo(s, scopedReq(http.MethodGet, "/v1/host/changes", "", owner, "swarm:read")); rec.Code != http.StatusOK {
		t.Fatalf("list with swarm:read: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes", body, owner, "swarm:read", "swarm:write", "swarm:manage")); rec.Code != http.StatusForbidden {
		t.Fatalf("request with AI levels only: %d %s", rec.Code, rec.Body)
	}

	rec, payload := hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes", body, owner, "host:request"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	id, _ := changeOf(payload)["id"].(string)
	for _, decision := range []string{"approve", "deny"} {
		rec, _ := hostChangeDo(s, scopedReq(http.MethodPost, "/v1/host/changes/"+id+"/"+decision, "", owner, "host:request", "admin", "swarm:approve", "swarm:manage"))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "machine owner") {
			t.Fatalf("%s with scoped token: %d %s", decision, rec.Code, rec.Body)
		}
	}
	if stored, _, _ := s.hostChanges.Get("acct", id); stored.Status != "pending" {
		t.Fatalf("scoped decision changed the record: %+v", stored)
	}
	if got := helper.received(); len(got) != 0 {
		t.Fatalf("scoped decision reached the helper: %q", got)
	}
}

func TestHostChangeUnsupportedWithoutHelper(t *testing.T) {
	for _, helper := range []*fakeHostHelper{nil, {supported: false}} {
		s, owner := newHostChangeTestServer(t, helper)
		rec, payload := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes", `{"op":"mount-workspace","path":"/home/person/app"}`, owner))
		if rec.Code != http.StatusConflict || payload["code"] != "host_changes_unsupported" || !strings.Contains(fmt.Sprint(payload["error"]), "host-helper step") {
			t.Fatalf("request: %d %s", rec.Code, rec.Body)
		}
		rec, payload = hostChangeDo(s, ownerReq(http.MethodGet, "/v1/host/changes", "", owner))
		if rec.Code != http.StatusOK || payload["supported"] != false {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		if all, _ := s.hostChanges.List("acct", ""); len(all) != 0 {
			t.Fatalf("unsupported request was recorded: %+v", all)
		}
	}
}

func TestHostChangeValidationAndInterruptedRecords(t *testing.T) {
	helper := newFakeHostHelper(func(op, path string) (hostchange.Result, error) { return hostchange.Result{OK: true}, nil })
	s, owner := newHostChangeTestServer(t, helper)
	storage := filepath.Join(t.TempDir(), "var", "lib", "swarmd")
	if err := os.MkdirAll(storage, 0o700); err != nil {
		t.Fatal(err)
	}
	tool.SetProtectedPaths([]string{storage}, nil)
	t.Cleanup(func() { tool.SetProtectedPaths(nil, nil) })

	for _, body := range []string{
		`{"op":"mount-workspace","path":"relative/app"}`,
		`{"op":"mount-workspace","path":"/home/person/../root"}`,
		`{"op":"mount-workspace","path":"/home/person/app/"}`,
		`{"op":"mount-workspace","path":"/"}`,
		`{"op":"mount-workspace","path":"/` + strings.Repeat("a", 520) + `"}`,
		`{"op":"mount-workspace","path":"` + filepath.Join(storage, "inside") + `"}`,
		`{"op":"mount-workspace","path":"` + storage + `"}`,
		`{"op":"format-disk","path":"/home/person/app"}`,
		`{"op":"mount-workspace","path":"/home/person/app","reason":"` + strings.Repeat("r", 501) + `"}`,
		`{"op":"mount-workspace","path":"/home/person/app","extra":1}`,
	} {
		rec, _ := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes", body, owner))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d %s", body, rec.Code, rec.Body)
		}
	}
	if all, _ := s.hostChanges.List("acct", ""); len(all) != 0 {
		t.Fatalf("invalid requests were recorded: %+v", all)
	}
	if rec, _ := hostChangeDo(s, ownerReq(http.MethodGet, "/v1/host/changes?status=bogus", "", owner)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status filter: %d", rec.Code)
	}
	if rec, _ := hostChangeDo(s, ownerReq(http.MethodGet, "/v1/host/changes/x/y/z", "", owner)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route: %d", rec.Code)
	}

	// A record left applying by a previous process fails at startup.
	change, err := s.hostChanges.Create("acct", "mount-workspace", "/home/person/app", "", "owner:owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.hostChanges.MarkApplying("acct", change.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.hostChanges.FailInterrupted(); err != nil || n != 1 {
		t.Fatalf("fail interrupted: %d %v", n, err)
	}
	stored, _, _ := s.hostChanges.Get("acct", change.ID)
	if stored.Status != "failed" || stored.Error != "interrupted" {
		t.Fatalf("interrupted record: %+v", stored)
	}
	if n, _ := s.hostChanges.FailInterrupted(); n != 0 {
		t.Fatalf("second pass found %d", n)
	}
	// Approving it later is refused; the helper is not asked.
	if rec, _ := hostChangeDo(s, ownerReq(http.MethodPost, "/v1/host/changes/"+change.ID+"/approve", "", owner)); rec.Code != http.StatusConflict {
		t.Fatalf("approve failed record: %d %s", rec.Code, rec.Body)
	}
	if got := helper.received(); len(got) != 0 {
		t.Fatalf("helper called: %q", got)
	}

	// The history keeps the newest 200 per account.
	for i := 0; i < 205; i++ {
		if _, err := s.hostChanges.Create("acct", "mount-workspace", fmt.Sprintf("/home/person/p%03d", i), "", "owner:owner"); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.hostChanges.List("acct", "")
	if len(all) != 200 || all[0].Path == "/home/person/p000" || all[len(all)-1].Path != "/home/person/p204" {
		t.Fatalf("history: %d records, first %s, last %s", len(all), all[0].Path, all[len(all)-1].Path)
	}
	if _, ok, _ := s.hostChanges.Get("acct", change.ID); ok {
		t.Fatal("oldest record survived trimming")
	}
	if errors.Is(err, pebblestore.ErrHostChangeNotFound) {
		t.Fatal("unexpected")
	}
}

// Purpose: registering a folder the daemon cannot see must answer with the
// host change that fixes it (handleWorkspaceAdd → writeWorkspaceNotVisible),
// not a dead "no such directory", for both coding and context-only
// registration; nothing is registered. A folder that exists keeps its usual
// repository checks, and Swarm's own storage never becomes a host change.
func TestWorkspaceAddAnswersNotVisibleWithHostChange(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	hidden := filepath.Join(t.TempDir(), "home", "person", "app")

	var payload map[string]any
	rec := postWorkspaceAdd(t, server, hidden, "app", &payload)
	if rec.Code != http.StatusConflict || payload["code"] != "workspace_not_visible" || payload["supported"] != false {
		t.Fatalf("status=%d payload=%#v", rec.Code, payload)
	}
	fix, _ := payload["host_change"].(map[string]any)
	if fix["op"] != "mount-workspace" || fix["path"] != hidden || !strings.Contains(fmt.Sprint(payload["error"]), "not visible to Swarm") {
		t.Fatalf("host_change=%#v error=%v", fix, payload["error"])
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)

	// With the helper installed the answer says so.
	server.hostChangeHelper = newFakeHostHelper(nil)
	payload = nil
	if rec := postWorkspaceAdd(t, server, hidden, "app", &payload); rec.Code != http.StatusConflict || payload["supported"] != true {
		t.Fatalf("status=%d payload=%#v", rec.Code, payload)
	}

	// Context-only registration answers the same way.
	body, _ := json.Marshal(map[string]any{"path": hidden, "name": "app", "context_only": true, "make_current": false})
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/add", strings.NewReader(string(body))), "workspace-user", "workspace-account")
	rr := httptest.NewRecorder()
	server.handleWorkspaceAdd(rr, req)
	payload = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &payload)
	if rr.Code != http.StatusConflict || payload["code"] != "workspace_not_visible" {
		t.Fatalf("context_only status=%d payload=%#v", rr.Code, payload)
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)

	// A folder that exists is not "not visible": the usual repository checks apply.
	existing := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	payload = nil
	if rec := postWorkspaceAdd(t, server, existing, "plain", &payload); payload["code"] == "workspace_not_visible" {
		t.Fatalf("existing folder reported not visible: %d %#v", rec.Code, payload)
	}
	// Swarm's own storage is never offered as a host change.
	storage := filepath.Join(t.TempDir(), "var", "lib", "swarmd")
	tool.SetProtectedPaths([]string{storage}, nil)
	t.Cleanup(func() { tool.SetProtectedPaths(nil, nil) })
	payload = nil
	if rec := postWorkspaceAdd(t, server, filepath.Join(storage, "db"), "db", &payload); payload["code"] == "workspace_not_visible" {
		t.Fatalf("storage reported not visible: %d %#v", rec.Code, payload)
	}
	// A relative path is not a host change either.
	payload = nil
	if rec := postWorkspaceAdd(t, server, "relative/app", "app", &payload); payload["code"] == "workspace_not_visible" {
		t.Fatalf("relative path reported not visible: %d %#v", rec.Code, payload)
	}
}
