package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the existing LocalTransportHandler/withAuth/updateOnboarding boundary
// must bootstrap a headless owner without browser headers, then recover the same
// product principal on later CLI requests. TCP headers cannot forge that trusted
// transport, and a second identity must not replace the first. Real registered
// handlers and an isolated identity store are the narrowest authority layer;
// this does not claim a live container/provider or restart qualification.
func TestHeadlessSetupLocalIdentityBoundary(t *testing.T) {
	server, store := newOnboardingIdentityTestServer(t, false)
	request := func(username string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://swarm-local-transport/v1/onboarding", strings.NewReader(`{"username":"`+username+`","swarm_name":"Headless"}`))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	for _, header := range []string{"", "X-Swarm-Local-Transport"} {
		req := request("owner")
		if header != "" {
			req.Header.Set(header, "true")
		}
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		counts, err := store.IdentityCounts()
		if rec.Code != http.StatusUnauthorized || err != nil || counts != (pebblestore.IdentityCounts{}) {
			t.Fatal("TCP request bypassed local identity authority")
		}
	}
	rec := httptest.NewRecorder()
	server.LocalTransportHandler().ServeHTTP(rec, request("owner"))
	if rec.Code != http.StatusOK {
		t.Fatalf("local bootstrap status %d", rec.Code)
	}
	var status onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.Identity.Bootstrapped || !status.NeedsOnboarding {
		t.Fatal("owner bootstrap did not leave setup pending")
	}
	before, err := store.IdentityCounts()
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	server.LocalTransportHandler().ServeHTTP(rec, request("replacement"))
	after, err := store.IdentityCounts()
	if rec.Code < 400 || err != nil || after != before {
		t.Fatal("repeated bootstrap replaced identity")
	}
	user, ok, err := store.GetUser(status.Identity.UserID)
	if err != nil || !ok || user.Username != "owner" {
		t.Fatal("owner changed after rejected bootstrap")
	}
	// No cookie/token is carried from bootstrap; the private transport resolves
	// the persisted owner for this later protected operation.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://swarm-local-transport/v1/onboarding", strings.NewReader(`{"desktop_onboarding_complete":true}`))
	req.Header.Set("Content-Type", "application/json")
	server.LocalTransportHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("later private request status %d", rec.Code)
	}
	cfg, err := startupconfig.Load(server.startupConfigPath)
	if err != nil || !cfg.DesktopOnboardingComplete {
		t.Fatal("setup completion not persisted")
	}
}
