package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/remote"
)

type fakeRemoteTransport struct{ decided int }

func (f *fakeRemoteTransport) Status() (remote.Status, error) { return remote.Status{}, nil }
func (f *fakeRemoteTransport) Init(remote.InitInput) (remote.Status, error) {
	return remote.Status{Configured: true}, nil
}
func (f *fakeRemoteTransport) SetEnabled(bool) (remote.Status, error) { return remote.Status{}, nil }
func (f *fakeRemoteTransport) Reset() (remote.Status, error)          { return remote.Status{}, nil }
func (f *fakeRemoteTransport) DecideConsent(string, bool, []string) (remote.Consent, error) {
	f.decided++
	return remote.Consent{}, nil
}

// Requirement: relay administration (init, enable, approving an AI client's
// authorization) belongs to the machine owner. A scoped token, which is what
// every remote or SDK client holds, must never reach it, on the daemon API or
// the SDK listener, so a client cannot approve or widen its own access.
// Owners: handleRemoteTransport, ContainerSDKHandler allowlist. In-process
// HTTP through the real auth middleware is the narrowest layer.
func TestRemoteTransportAdminRequiresOwner(t *testing.T) {
	s, attach, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	fake := &fakeRemoteTransport{}
	s.SetRemoteTransportService(fake)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	scoped, _, err := sec.CreateScopedToken("client", []string{"*"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		sdk   bool
		token string
		path  string
		want  int
	}{
		"scoped approve on API":    {false, scoped, "/v1/remote/consents/approve", 403},
		"scoped init on API":       {false, scoped, "/v1/remote/init", 403},
		"scoped approve on SDK":    {true, scoped, "/v1/remote/consents/approve", 403},
		"no credential on API":     {false, "", "/v1/remote/consents/approve", 401},
		"owner attach token works": {false, attach, "/v1/remote/consents/approve", 200},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:7781"+tc.path, strings.NewReader(`{"code":"ABCD-EFGH"}`))
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		if tc.sdk {
			s.ContainerSDKHandler().ServeHTTP(w, r)
		} else {
			s.Handler().ServeHTTP(w, r)
		}
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d: %s", name, w.Code, tc.want, w.Body.String())
		}
	}
	if fake.decided != 1 {
		t.Fatalf("consent decided %d times; only the owner call may reach the service", fake.decided)
	}
}
