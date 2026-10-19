package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a run step runs its command on the host as the daemon's user,
// outside the sandbox and without tool approvals, with free-form command and
// args. So only the machine's owner may save one: the local owner (no scoped
// token) or a key with the admin scope (a full AI key). A write-level AI key
// and a remote device token with write access hold automations:write but not
// admin; they are refused on every path that stores steps (the HTTP route,
// swarm_set_automation, and a then on swarm_give_job or swarm_schedule_job),
// with the reason, and nothing is stored or started. They may still save
// agent steps.
// Threat: a weaker-than-full key (designed not to approve tool calls) or a
// remote device saving {"command":"/bin/sh","args":["-c",...]} and so running
// any command as the daemon's user, which can read daemon storage and
// provider credentials.
// Owners: runStepOwnerError, called by handleJobAutomations and
// controlMCPCall.automationSteps (swarm_set_automation, controlMCPValidateThen
// and controlMCPThen); jobautomation.HasRunStep.
// Layer: the handlers and MCP tool functions over a real Pebble-backed
// jobautomation.Store are the narrowest layer that shows both the refusal and
// that nothing was stored; the then case drives the real tools with a
// recording fake of the internal routes, so it also shows no job was started.

// remoteWriteDeviceScopes are the scopes remote.Service.Pair mints for a
// device with AllowWrite.
var remoteWriteDeviceScopes = []string{"sessions:read", "automations:read", "agents:read", "signals:read", "secrets:read", "sessions:write", "automations:write", "settings:write"}

func scopesForLevel(t *testing.T, level string) []string {
	t.Helper()
	scopes, err := aiKeyScopes(level)
	if err != nil {
		t.Fatal(err)
	}
	return scopes
}

func newRunOwnerServer(t *testing.T) (*Server, *jobautomation.Store) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "run-owner.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	automations := jobautomation.NewStore(store)
	server := &Server{}
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: automations}))
	return server, automations
}

func storedAutomations(t *testing.T, store *jobautomation.Store) []jobautomation.Automation {
	t.Helper()
	list, err := store.List(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

const shellRunStep = `{"type":"run","command":"/bin/sh","args":["-c","id"]}`

func TestRunStepNeedsTheOwnerOverHTTP(t *testing.T) {
	server, store := newRunOwnerServer(t)
	post := func(body string, scopes []string) *httptest.ResponseRecorder {
		r := withTestPrincipal(httptest.NewRequest(http.MethodPost, JobAutomationsPath, strings.NewReader(body)))
		if scopes != nil {
			r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{Scopes: scopes})
		}
		w := httptest.NewRecorder()
		server.handleJobAutomations(w, r)
		return w
	}
	runBody := `{"name":"shell","scope":{"folder":"/srv/site"},"steps":[` + shellRunStep + `]}`
	mixedBody := `{"name":"shell","scope":{"folder":"/srv/site"},"steps":[{"type":"agent","worker":"reviewer","brief":"review"},` + shellRunStep + `]}`
	for name, scopes := range map[string][]string{"write AI key": scopesForLevel(t, "write"), "remote write device": remoteWriteDeviceScopes} {
		for _, body := range []string{runBody, mixedBody} {
			w := post(body, scopes)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "only the machine's owner may save one") {
				t.Fatalf("%s: run step create = %d %s", name, w.Code, w.Body)
			}
		}
		for _, a := range storedAutomations(t, store) {
			if jobautomation.HasRunStep(a.Steps) {
				t.Fatalf("%s: a refused run step was stored: %+v", name, a)
			}
		}
		// The same key may still save agent steps.
		if w := post(`{"name":"review","scope":{"folder":"/srv/site"},"steps":[{"type":"agent","worker":"reviewer","brief":"review"}]}`, scopes); w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("%s: agent step create = %d %s", name, w.Code, w.Body)
		}
	}

	// The owner: a full AI key (admin), and the local owner (no scoped token).
	if w := post(`{"name":"push","scope":{"folder":"/srv/site"},"steps":[{"type":"run","command":"/usr/local/libexec/push.sh"}]}`, scopesForLevel(t, "full")); w.Code != http.StatusCreated {
		t.Fatalf("admin run step create = %d %s", w.Code, w.Body)
	}
	if w := post(`{"name":"merge","scope":{"folder":"/srv/site"},"steps":[{"type":"run","command":"deliver/merge.sh"}]}`, nil); w.Code != http.StatusCreated {
		t.Fatalf("local owner run step create = %d %s", w.Code, w.Body)
	}

	// A write key cannot replace the owner's run step with its own command.
	if w := post(`{"name":"push","scope":{"folder":"/srv/site"},"steps":[`+shellRunStep+`]}`, scopesForLevel(t, "write")); w.Code != http.StatusForbidden {
		t.Fatalf("write key replacing a run step = %d %s", w.Code, w.Body)
	}
	push, err := store.FindByName(testPrincipal().AccountScopeID, "push")
	if err != nil || push.Revision != 1 || len(push.Steps) != 1 || push.Steps[0].Command != "/usr/local/libexec/push.sh" || len(push.Steps[0].Args) != 0 {
		t.Fatalf("the owner's run step changed: %+v %v", push, err)
	}
}

func TestRunStepNeedsTheOwnerOverMCP(t *testing.T) {
	server, store := newRunOwnerServer(t)
	call := func(scopes []string) *controlMCPCall {
		return &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
			&pebblestore.ScopedTokenRecord{Scopes: scopes})}
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(shellRunStep), &step); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"name": "shell", "scope": map[string]any{"folder": "/srv/site"}, "steps": []any{step}}
	for name, scopes := range map[string][]string{"write AI key": scopesForLevel(t, "write"), "remote write device": remoteWriteDeviceScopes} {
		if _, err := controlMCPSetAutomation(call(scopes), args); err == nil || !strings.Contains(err.Error(), "only the machine's owner may save one") {
			t.Fatalf("%s: swarm_set_automation with a run step = %v", name, err)
		}
		if err := controlMCPValidateThen(call(scopes), map[string]any{"steps": []any{step}}); err == nil || !strings.Contains(err.Error(), "only the machine's owner may save one") {
			t.Fatalf("%s: then validation = %v", name, err)
		}
		if _, err := call(scopes).controlMCPThen(map[string]any{"steps": []any{step}}, "then: worker/w/r", "/srv/site", jobautomation.Scope{Job: "worker/w/r"}); err == nil {
			t.Fatalf("%s: then set a run step", name)
		}
		if list := storedAutomations(t, store); len(list) != 0 {
			t.Fatalf("%s: a refused run step was stored: %+v", name, list)
		}
	}
	full := call(scopesForLevel(t, "full"))
	if err := controlMCPValidateThen(full, map[string]any{"steps": []any{step}}); err != nil {
		t.Fatalf("admin then validation = %v", err)
	}
	if _, err := controlMCPSetAutomation(full, args); err != nil {
		t.Fatalf("admin swarm_set_automation = %v", err)
	}
	if list := storedAutomations(t, store); len(list) != 1 || list[0].Steps[0].Command != "/bin/sh" {
		t.Fatalf("admin run step not stored: %+v", list)
	}
}

// The then argument of swarm_give_job and swarm_schedule_job is checked
// before anything starts: a write key's run step is refused, no job or
// schedule is created and no automation is stored.
func TestRunStepInAThenNeedsTheOwner(t *testing.T) {
	server, store := newRunOwnerServer(t)
	folder := t.TempDir()
	var writes []string
	c := &controlMCPCall{server: server, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: scopesForLevel(t, "write")})}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
		}
		switch r.URL.Path {
		case "/v3/workers":
			writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": "wkr-1", "name": "coder", "role": "coder"}}})
		case "/v3/projects":
			writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "proj-1", "name": "Site",
				"workspaces": []any{map[string]any{"path": folder, "workspace_id": "ws-1", "role": "primary_code"}}}}})
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})
	var step map[string]any
	if err := json.Unmarshal([]byte(shellRunStep), &step); err != nil {
		t.Fatal(err)
	}
	then := map[string]any{"steps": []any{step}}
	if _, err := controlMCPGiveJob(c, map[string]any{"worker": "wkr-1", "brief": "add a logo", "then": then}); err == nil || !strings.Contains(err.Error(), "only the machine's owner may save one") {
		t.Fatalf("swarm_give_job with a run step then = %v", err)
	}
	if _, err := controlMCPScheduleJob(c, map[string]any{"worker": "wkr-1", "brief": "nightly check", "schedule": map[string]any{"every": "1d"}, "then": then}); err == nil || !strings.Contains(err.Error(), "only the machine's owner may save one") {
		t.Fatalf("swarm_schedule_job with a run step then = %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("a refused then started something: %v", writes)
	}
	if list := storedAutomations(t, store); len(list) != 0 {
		t.Fatalf("a refused then stored an automation: %+v", list)
	}
}
