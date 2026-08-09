package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscale"
)

func TestDesktopBoundaryRejectsDNSRebindingBeforeDesktopSurface(t *testing.T) {
	server := newLocalAuthTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "http://attacker.example/app", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://attacker.example")
	req.Header.Set("Referer", "http://attacker.example/app")
	rec := httptest.NewRecorder()

	server.DesktopHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == desktopLocalSessionCookieName {
			t.Fatal("rejected authority issued a desktop session cookie")
		}
	}
}

func TestDesktopBoundaryTailscaleAdmission(t *testing.T) {
	server, policy, detector := newDesktopBoundaryTailscaleServer(t)
	const origin = "https://node.tailnet.ts.net"

	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, origin+"/v3/realtime/stream", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/app")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set(tailscaleUserLoginHeader, "alice@example.test")
		req.Header.Set(tailscaleUserNameHeader, "Alice")
		req.Header.Set(tailscaleUserProfilePicHeader, "https://example.test/alice.png")
		return req
	}

	if _, err := server.admitDesktopRequest(request()); err == nil {
		t.Fatal("unapproved Tailscale origin was admitted")
	}
	if _, _, err := policy.Add(origin); err != nil {
		t.Fatalf("approve origin: %v", err)
	}
	admittedRequest := request()
	admission, err := server.admitDesktopRequest(admittedRequest)
	if err != nil || admission.origin != origin || !admission.tailscaleServe {
		t.Fatalf("approved admission=%+v err=%v", admission, err)
	}
	admittedRequest = admittedRequest.WithContext(context.WithValue(admittedRequest.Context(), desktopAdmittedOriginKey, admission))
	if !isLocalAdministrativeRequest(admittedRequest) {
		t.Fatal("approved Tailscale Desktop admission was not allowed to perform a host administrative action")
	}

	missingIdentity := request()
	missingIdentity.Header.Del(tailscaleUserLoginHeader)
	if _, err := server.admitDesktopRequest(missingIdentity); err == nil {
		t.Fatal("request without Serve identity provenance was admitted")
	}
	if isLocalAdministrativeRequest(missingIdentity) {
		t.Fatal("request without an admitted Serve identity was allowed to perform a host administrative action")
	}
	spoofedRemote := request()
	spoofedRemote.RemoteAddr = "192.0.2.10:43210"
	if _, err := server.admitDesktopRequest(spoofedRemote); err == nil {
		t.Fatal("non-loopback request with spoofed Serve headers was admitted")
	}
	forwardedHost := request()
	forwardedHost.Header.Set("X-Forwarded-Host", "node.tailnet.ts.net")
	if _, err := server.admitDesktopRequest(forwardedHost); err != nil {
		t.Fatalf("matching Tailscale Serve X-Forwarded-Host was rejected: %v", err)
	}
	mismatchedForwardedHost := request()
	mismatchedForwardedHost.Header.Set("X-Forwarded-Host", "other.tailnet.ts.net")
	if _, err := server.admitDesktopRequest(mismatchedForwardedHost); err == nil {
		t.Fatal("request with mismatched X-Forwarded-Host was admitted")
	}
	duplicateForwardedHost := request()
	duplicateForwardedHost.Header["X-Forwarded-Host"] = []string{"node.tailnet.ts.net", "node.tailnet.ts.net"}
	if _, err := server.admitDesktopRequest(duplicateForwardedHost); err == nil {
		t.Fatal("request with duplicate X-Forwarded-Host was admitted")
	}
	crossOrigin := request()
	crossOrigin.Header.Set("Origin", "https://other.tailnet.ts.net")
	if _, err := server.admitDesktopRequest(crossOrigin); err == nil {
		t.Fatal("cross-origin request was admitted")
	}
	malformedReferer := request()
	malformedReferer.Header.Set("Referer", "://not-an-origin")
	if _, err := server.admitDesktopRequest(malformedReferer); err == nil {
		t.Fatal("malformed referer was admitted")
	}
	websocket := request()
	websocket.Header.Set("Upgrade", "websocket")
	websocket.Header.Set("Connection", "Upgrade")
	websocket.Header.Set("Origin", "https://other.tailnet.ts.net")
	websocketRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(websocketRec, websocket)
	if websocketRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin websocket status=%d want=%d body=%s", websocketRec.Code, http.StatusForbidden, websocketRec.Body.String())
	}

	detector.snapshot.Routes[0].Classification = tailscale.ClassificationWrongTarget
	if _, err := server.admitDesktopRequest(request()); err == nil {
		t.Fatal("changed Serve target was admitted")
	}
	detector.snapshot.Routes[0].Classification = tailscale.ClassificationFunnelEnabled
	if _, err := server.admitDesktopRequest(request()); err == nil {
		t.Fatal("Funnel-enabled route was admitted")
	}
	detector.err = errors.New("verification refresh failed")
	if _, err := server.admitDesktopRequest(request()); err == nil {
		t.Fatal("verification failure was admitted")
	}
	detector.err = nil
	detector.snapshot.Routes[0].Classification = tailscale.ClassificationVerifiedSwarmDesktop
	if _, _, err := policy.Remove(origin); err != nil {
		t.Fatalf("revoke origin: %v", err)
	}
	if _, err := server.admitDesktopRequest(request()); err == nil {
		t.Fatal("revoked origin was admitted")
	}
}

func TestDesktopBoundaryAllowsExplicitTailscaleOnboardingApprovalOnly(t *testing.T) {
	server, policy, _ := newDesktopBoundaryTailscaleServer(t)
	const origin = "https://node.tailnet.ts.net"

	request := func(method, path string) *http.Request {
		req := httptest.NewRequest(method, origin+path, nil)
		req.RemoteAddr = "127.0.0.1:43210"
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set(tailscaleUserLoginHeader, "alice@example.test")
		req.Header.Set(tailscaleUserNameHeader, "Alice")
		req.Header.Set(tailscaleUserProfilePicHeader, "https://example.test/alice.png")
		return req
	}

	protectedRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(protectedRec, request(http.MethodGet, "/v1/onboarding"))
	if protectedRec.Code != http.StatusForbidden {
		t.Fatalf("unapproved protected endpoint status=%d want=%d body=%s", protectedRec.Code, http.StatusForbidden, protectedRec.Body.String())
	}

	assetRec := httptest.NewRecorder()
	server.withDesktopBoundary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pendingDesktopOrigin(r); !ok {
			t.Fatal("static bootstrap request did not carry pending Tailscale origin")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(assetRec, request(http.MethodGet, "/"))
	if assetRec.Code != http.StatusNoContent {
		t.Fatalf("unapproved static bootstrap status=%d want=%d body=%s", assetRec.Code, http.StatusNoContent, assetRec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(statusRec, request(http.MethodGet, TailscaleOnboardingApprovalPath))
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"required":true`) || !strings.Contains(statusRec.Body.String(), origin) {
		t.Fatalf("approval status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}

	approveRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(approveRec, request(http.MethodPost, TailscaleOnboardingApprovalPath))
	if approveRec.Code != http.StatusOK {
		t.Fatalf("approval status=%d body=%s", approveRec.Code, approveRec.Body.String())
	}
	record, ok, err := policy.Get()
	if err != nil || !ok || !containsExactString(record.Origins, origin) {
		t.Fatalf("approved policy record=%+v ok=%t err=%v", record, ok, err)
	}
	if _, err := server.admitDesktopRequest(request(http.MethodGet, "/v1/onboarding")); err != nil {
		t.Fatalf("approved origin did not continue to onboarding: %v", err)
	}
}

func TestDesktopBoundaryFailsClosedWithoutPolicyOrVerifier(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "https://node.tailnet.ts.net/app", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set(tailscaleUserLoginHeader, "alice@example.test")
	req.Header.Set(tailscaleUserNameHeader, "Alice")
	req.Header.Set(tailscaleUserProfilePicHeader, "https://example.test/alice.png")
	if _, err := server.admitDesktopRequest(req); err == nil {
		t.Fatal("non-local authority was admitted without policy and verifier")
	}
}

func TestDesktopBoundaryLocalAuthorityRequiresSameMachineSource(t *testing.T) {
	server := newLocalAuthTestServer(t)
	local := httptest.NewRequest(http.MethodGet, "http://localhost:5555/app", nil)
	local.RemoteAddr = "127.0.0.1:43210"
	local.Header.Set("Origin", "http://localhost:5555")
	local.Header.Set("Referer", "http://localhost:5555/app")
	if _, err := server.admitDesktopRequest(local); err != nil {
		t.Fatalf("same-machine localhost rejected: %v", err)
	}

	remote := local.Clone(local.Context())
	remote.RemoteAddr = "192.0.2.10:43210"
	if _, err := server.admitDesktopRequest(remote); err == nil {
		t.Fatal("remote source using localhost authority was admitted")
	}

	forwarded := local.Clone(local.Context())
	forwarded.Header = local.Header.Clone()
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	forwarded.Header.Set("Origin", "https://localhost:5555")
	if _, err := server.admitDesktopRequest(forwarded); err == nil {
		t.Fatal("untrusted forwarded scheme changed local admitted origin")
	}
}

func newDesktopBoundaryTailscaleServer(t *testing.T) (*Server, *pebblestore.TailscaleServeAllowlistStore, *tailscaleSettingsDetectorStub) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "desktop-boundary.pebble"))
	if err != nil {
		t.Fatalf("open policy store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	policy := pebblestore.NewTailscaleServeAllowlistStore(store)
	detector := &tailscaleSettingsDetectorStub{snapshot: tailscale.Snapshot{
		SelfDNSName: "node.tailnet.ts.net",
		Routes: []tailscale.Route{{
			Origin:         "https://node.tailnet.ts.net",
			Authority:      "node.tailnet.ts.net:443",
			Classification: tailscale.ClassificationVerifiedSwarmDesktop,
		}},
	}}
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTailscaleServePolicy(policy, detector)
	return server, policy, detector
}
