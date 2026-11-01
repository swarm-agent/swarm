package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a run step with in: environment runs a script of the job's
// head sandboxed in a lease of the folder's environment, like an agent's
// bash, so a write-level AI key (no admin scope) may save one, as the AI
// owner managing a machine remotely needs; it is stored with in:
// environment. The same key is still refused a host run step, also when it
// says in: host, and an environment step naming a host path (absolute or
// with ..) is refused as invalid with nothing stored.
// Threat: the sandboxed check needing the machine owner's admin key, or the
// new field opening a way for a write key to save a host command.
// Owners: runStepOwnerError over jobautomation.HasRunStep, and
// NormalizeSteps through handleJobAutomations. The HTTP handler over a real
// Pebble-backed store is the narrowest layer showing both the decision and
// what was stored.
func TestEnvironmentRunStepNeedsNoOwnerOverHTTP(t *testing.T) {
	server, store := newRunOwnerServer(t)
	post := func(body string, scopes []string) *httptest.ResponseRecorder {
		r := withTestPrincipal(httptest.NewRequest(http.MethodPost, JobAutomationsPath, strings.NewReader(body)))
		r = requestWithScopedToken(r, &pebblestore.ScopedTokenRecord{Scopes: scopes})
		w := httptest.NewRecorder()
		server.handleJobAutomations(w, r)
		return w
	}
	write := scopesForLevel(t, "write")
	envBody := `{"name":"console","scope":{"folder":"/srv/site"},"steps":[{"id":"console","type":"run","in":"environment","command":"scripts/check-console.sh","continue_on_failure":true}]}`
	if w := post(envBody, write); w.Code != http.StatusCreated {
		t.Fatalf("write key environment step = %d %s", w.Code, w.Body)
	}
	saved, err := store.FindByName(testPrincipal().AccountScopeID, "console")
	if err != nil || len(saved.Steps) != 1 || saved.Steps[0].In != jobautomation.RunInEnvironment || saved.Steps[0].Command != "scripts/check-console.sh" || !saved.Steps[0].ContinueOnFailure {
		t.Fatalf("stored = %+v, %v", saved, err)
	}

	hostBody := `{"name":"shell","scope":{"folder":"/srv/site"},"steps":[{"type":"run","in":"host","command":"/bin/sh","args":["-c","id"]}]}`
	if w := post(hostBody, write); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "only the machine's owner may save one") {
		t.Fatalf("write key in: host step = %d %s", w.Code, w.Body)
	}
	for _, command := range []string{"/bin/sh", "../outside.sh"} {
		body := `{"name":"escape","scope":{"folder":"/srv/site"},"steps":[{"type":"run","in":"environment","command":"` + command + `"}]}`
		if w := post(body, write); w.Code != http.StatusBadRequest {
			t.Fatalf("environment step %q = %d %s", command, w.Code, w.Body)
		}
	}
	for _, a := range storedAutomations(t, store) {
		if a.Name != "console" {
			t.Fatalf("a refused workflow was stored: %+v", a)
		}
	}
}
