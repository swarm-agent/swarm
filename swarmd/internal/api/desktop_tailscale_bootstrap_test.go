package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscale"
)

func TestTailscaleDesktopBootstrapApprovesExactOwnerServeOriginOnce(t *testing.T) {
	server, policy, detector := newDesktopBoundaryTailscaleServer(t)
	server.SetStartupConfigPath(t.TempDir() + "/swarm.conf")
	detector.snapshot.SelfOrigin = "https://node.tailnet.ts.net"
	detector.snapshot.OwnerLogin = "owner@example.test"

	page := newTailscaleBootstrapRequest(http.MethodGet, "/", "owner@example.test")
	pageRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(pageRec, page)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("approval page status=%d body=%s", pageRec.Code, pageRec.Body.String())
	}
	body := pageRec.Body.String()
	if !strings.Contains(body, "https://node.tailnet.ts.net") || !strings.Contains(body, tailscaleDesktopBootstrapPath) {
		t.Fatalf("approval page omitted server-derived origin or action: %s", body)
	}
	if strings.Contains(body, `name="origin"`) || strings.Contains(body, "/v1/") {
		t.Fatalf("approval page exposed a client origin input or normal API: %s", body)
	}

	api := newTailscaleBootstrapRequest(http.MethodGet, "/v3/sessions", "owner@example.test")
	api.Header.Set("Accept", "application/json")
	apiRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(apiRec, api)
	if apiRec.Code != http.StatusForbidden {
		t.Fatalf("normal API before approval status=%d want=%d body=%s", apiRec.Code, http.StatusForbidden, apiRec.Body.String())
	}

	asset := newTailscaleBootstrapRequest(http.MethodGet, "/favicon.svg", "owner@example.test")
	asset.Header.Set("Accept", "image/svg+xml")
	assetRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(assetRec, asset)
	if assetRec.Code != http.StatusForbidden {
		t.Fatalf("normal asset before approval status=%d want=%d body=%s", assetRec.Code, http.StatusForbidden, assetRec.Body.String())
	}

	deepLink := newTailscaleBootstrapRequest(http.MethodGet, "/sessions/example", "owner@example.test")
	deepLinkRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(deepLinkRec, deepLink)
	if deepLinkRec.Code != http.StatusForbidden {
		t.Fatalf("normal Desktop route before approval status=%d want=%d body=%s", deepLinkRec.Code, http.StatusForbidden, deepLinkRec.Body.String())
	}

	approve := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "owner@example.test")
	approveRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(approveRec, approve)
	if approveRec.Code != http.StatusSeeOther || approveRec.Header().Get("Location") != "/" {
		t.Fatalf("approval status=%d location=%q body=%s", approveRec.Code, approveRec.Header().Get("Location"), approveRec.Body.String())
	}
	record, ok, err := policy.Get()
	if err != nil || !ok || len(record.Origins) != 1 || record.Origins[0] != "https://node.tailnet.ts.net" {
		t.Fatalf("allowlist after approval ok=%t err=%v record=%+v", ok, err, record)
	}

	admission, err := server.admitDesktopRequest(newTailscaleBootstrapRequest(http.MethodGet, "/", "owner@example.test"))
	if err != nil || admission.origin != "https://node.tailnet.ts.net" {
		t.Fatalf("normal admission after approval=%+v err=%v", admission, err)
	}

	replay := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "owner@example.test")
	replayRec := httptest.NewRecorder()
	server.withDesktopBoundary(http.NotFoundHandler()).ServeHTTP(replayRec, replay)
	if replayRec.Code != http.StatusNotFound {
		t.Fatalf("bootstrap route replay status=%d want=%d body=%s", replayRec.Code, http.StatusNotFound, replayRec.Body.String())
	}
}

func TestTailscaleDesktopBootstrapUsesProductOwnerWhenIdentityExists(t *testing.T) {
	server, policy, detector := newDesktopBoundaryTailscaleServer(t)
	server.SetStartupConfigPath(t.TempDir() + "/swarm.conf")
	detector.snapshot.SelfOrigin = "https://node.tailnet.ts.net"
	detector.snapshot.OwnerLogin = "node-owner@example.test"
	setTailscaleBootstrapProductOwner(t, server, "product-owner@example.test")

	member := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "node-owner@example.test")
	memberRec := httptest.NewRecorder()
	server.withDesktopBoundary(http.NotFoundHandler()).ServeHTTP(memberRec, member)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("non-product owner status=%d want=%d body=%s", memberRec.Code, http.StatusForbidden, memberRec.Body.String())
	}

	owner := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "product-owner@example.test")
	ownerRec := httptest.NewRecorder()
	server.withDesktopBoundary(http.NotFoundHandler()).ServeHTTP(ownerRec, owner)
	if ownerRec.Code != http.StatusSeeOther {
		t.Fatalf("product owner status=%d want=%d body=%s", ownerRec.Code, http.StatusSeeOther, ownerRec.Body.String())
	}
	record, ok, err := policy.Get()
	if err != nil || !ok || len(record.Origins) != 1 {
		t.Fatalf("product owner approval ok=%t err=%v record=%+v", ok, err, record)
	}
}

func TestTailscaleDesktopBootstrapStopsAfterOnboardingCompletion(t *testing.T) {
	server, policy, detector := newDesktopBoundaryTailscaleServer(t)
	server.SetStartupConfigPath(t.TempDir() + "/swarm.conf")
	detector.snapshot.SelfOrigin = "https://node.tailnet.ts.net"
	detector.snapshot.OwnerLogin = "owner@example.test"
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	cfg.DesktopOnboardingComplete = true
	cfg.DesktopOnboardingCompleteSet = true
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write completed startup config: %v", err)
	}
	req := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "owner@example.test")
	rec := httptest.NewRecorder()
	server.withDesktopBoundary(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("completed onboarding status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	record, ok, err := policy.Get()
	if err != nil || ok || len(record.Origins) != 0 {
		t.Fatalf("completed onboarding mutated allowlist ok=%t err=%v record=%+v", ok, err, record)
	}
}

func TestTailscaleDesktopBootstrapRejectsUntrustedOrUnverifiedRequests(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus int
		mutate     func(*http.Request, *tailscaleSettingsDetectorStub)
	}{
		{name: "non owner", mutate: func(r *http.Request, _ *tailscaleSettingsDetectorStub) {
			r.Header.Set(tailscaleUserLoginHeader, "member@example.test")
		}},
		{name: "spoofed remote", mutate: func(r *http.Request, _ *tailscaleSettingsDetectorStub) { r.RemoteAddr = "192.0.2.10:4444" }},
		{name: "wrong host", mutate: func(r *http.Request, _ *tailscaleSettingsDetectorStub) { r.Host = "other.tailnet.ts.net" }},
		{name: "arbitrary origin", mutate: func(r *http.Request, _ *tailscaleSettingsDetectorStub) {
			r.Header.Set("Origin", "https://other.tailnet.ts.net")
		}},
		{name: "wrong target", mutate: func(_ *http.Request, d *tailscaleSettingsDetectorStub) {
			d.snapshot.Routes[0].Classification = tailscale.ClassificationWrongTarget
		}},
		{name: "funnel", mutate: func(_ *http.Request, d *tailscaleSettingsDetectorStub) {
			d.snapshot.Routes[0].Classification = tailscale.ClassificationFunnelEnabled
		}},
		{name: "browser supplied value", wantStatus: http.StatusBadRequest, mutate: func(r *http.Request, _ *tailscaleSettingsDetectorStub) {
			r.Body = io.NopCloser(strings.NewReader("origin=https://other.tailnet.ts.net"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, policy, detector := newDesktopBoundaryTailscaleServer(t)
			server.SetStartupConfigPath(t.TempDir() + "/swarm.conf")
			detector.snapshot.SelfOrigin = "https://node.tailnet.ts.net"
			detector.snapshot.OwnerLogin = "owner@example.test"
			req := newTailscaleBootstrapRequest(http.MethodPost, tailscaleDesktopBootstrapPath, "owner@example.test")
			test.mutate(req, detector)
			rec := httptest.NewRecorder()
			server.DesktopHandler().ServeHTTP(rec, req)
			wantStatus := test.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusForbidden
			}
			if rec.Code != wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
			}
			record, ok, err := policy.Get()
			if err != nil || ok || len(record.Origins) != 0 {
				t.Fatalf("rejected request mutated allowlist ok=%t err=%v record=%+v", ok, err, record)
			}
		})
	}
}

func setTailscaleBootstrapProductOwner(t *testing.T, server *Server, login string) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "tailscale-bootstrap-identity.pebble"))
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := identity.NewService(pebblestore.NewIdentityStore(store))
	if _, err := service.BootstrapFirstIdentity(login); err != nil {
		t.Fatalf("bootstrap product owner: %v", err)
	}
	server.SetIdentityService(service)
}

func newTailscaleBootstrapRequest(method, path, login string) *http.Request {
	req := httptest.NewRequest(method, "https://node.tailnet.ts.net"+path, nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "https://node.tailnet.ts.net")
	req.Header.Set("Referer", "https://node.tailnet.ts.net/")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set(tailscaleUserLoginHeader, login)
	req.Header.Set(tailscaleUserNameHeader, "Owner")
	req.Header.Set(tailscaleUserProfilePicHeader, "https://example.test/profile.png")
	return req
}
