package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/auth"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	"swarm/packages/swarmd/internal/security"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/update"
	"swarm/packages/swarmd/internal/workspace"
)

func sessionCookieFromRecorder(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == desktopLocalSessionCookieName {
			if strings.TrimSpace(cookie.Value) == "" {
				t.Fatalf("expected %q cookie to have a value", desktopLocalSessionCookieName)
			}
			return cookie
		}
	}
	t.Fatalf("expected %q cookie to be issued", desktopLocalSessionCookieName)
	return nil
}

func TestDesktopHandlerBootstrapsCookieAndAllowsProtectedAPIWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/app", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1")
	req.Header.Set("Referer", "http://127.0.0.1/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)
	sessionCookie := sessionCookieFromRecorder(t, rec)

	vaultReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/vault", nil)
	vaultReq.RemoteAddr = "127.0.0.1:43210"
	vaultReq.Header.Set("Origin", "http://127.0.0.1")
	vaultReq.Header.Set("Referer", "http://127.0.0.1/app")
	vaultReq.Header.Set("Sec-Fetch-Site", "same-origin")
	vaultReq.AddCookie(sessionCookie)
	vaultRec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(vaultRec, vaultReq)

	if vaultRec.Code != http.StatusOK {
		t.Fatalf("vault status with desktop session = %d, want %d, body=%s", vaultRec.Code, http.StatusOK, vaultRec.Body.String())
	}
	var status auth.VaultStatus
	if err := json.Unmarshal(vaultRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode vault status: %v", err)
	}
	if status.Enabled {
		t.Fatalf("expected test vault to be disabled")
	}
}

func TestMainHandlerDesktopSessionBootstrapAllowsProtectedAPIWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/auth/desktop/session", nil)
	bootstrapReq.RemoteAddr = "127.0.0.1:43210"
	bootstrapReq.Header.Set("Origin", "http://127.0.0.1:5555")
	bootstrapReq.Header.Set("Referer", "http://127.0.0.1:5555/app")
	bootstrapReq.Header.Set("Sec-Fetch-Site", "same-origin")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("desktop session bootstrap status = %d, want %d, body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	sessionCookie := sessionCookieFromRecorder(t, bootstrapRec)

	vaultReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/vault", nil)
	vaultReq.RemoteAddr = "127.0.0.1:43210"
	vaultReq.Header.Set("Origin", "http://127.0.0.1:5555")
	vaultReq.Header.Set("Referer", "http://127.0.0.1:5555/app")
	vaultReq.Header.Set("Sec-Fetch-Site", "same-origin")
	vaultReq.AddCookie(sessionCookie)
	vaultRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(vaultRec, vaultReq)
	if vaultRec.Code != http.StatusOK {
		t.Fatalf("proxied desktop vault status with session = %d, want %d, body=%s", vaultRec.Code, http.StatusOK, vaultRec.Body.String())
	}
}

func TestMainHandlerDesktopSessionBootstrapAllowsSameMachineLANOriginWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)
	lanAddrs := detectLANAddresses()
	if len(lanAddrs) == 0 {
		t.Skip("no LAN addresses available on this machine")
	}
	lanIP := strings.TrimSpace(lanAddrs[0])
	if lanIP == "" {
		t.Skip("empty LAN address")
	}

	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://"+lanIP+":5555/v1/auth/desktop/session", nil)
	bootstrapReq.RemoteAddr = lanIP + ":43210"
	bootstrapReq.Header.Set("Origin", "http://"+lanIP+":5555")
	bootstrapReq.Header.Set("Referer", "http://"+lanIP+":5555/app")
	bootstrapReq.Header.Set("Sec-Fetch-Site", "same-origin")
	bootstrapRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusOK {
		t.Fatalf("desktop LAN session bootstrap status = %d, want %d, body=%s", bootstrapRec.Code, http.StatusOK, bootstrapRec.Body.String())
	}
	sessionCookie := sessionCookieFromRecorder(t, bootstrapRec)

	vaultReq := httptest.NewRequest(http.MethodGet, "http://"+lanIP+":5555/v1/vault", nil)
	vaultReq.RemoteAddr = lanIP + ":43210"
	vaultReq.Header.Set("Origin", "http://"+lanIP+":5555")
	vaultReq.Header.Set("Referer", "http://"+lanIP+":5555/app")
	vaultReq.Header.Set("Sec-Fetch-Site", "same-origin")
	vaultReq.AddCookie(sessionCookie)
	vaultRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(vaultRec, vaultReq)
	if vaultRec.Code != http.StatusOK {
		t.Fatalf("proxied desktop LAN vault status with session = %d, want %d, body=%s", vaultRec.Code, http.StatusOK, vaultRec.Body.String())
	}
}

func TestLocalTransportHandlerAllowsProtectedAPIWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://swarm-local-transport/v1/vault", nil)
	req.RemoteAddr = "192.0.2.10:7777"
	rec := httptest.NewRecorder()
	server.LocalTransportHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("local transport vault status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMainHandlerUpdateStatusAllowsLoopbackWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.SetUpdateService(newStaticUpdateService(t, true))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/update/status", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("loopback update status without attach token = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMainHandlerUpdateLocalContainersAllowsLoopbackWithoutAttachTokenAndAcceptsPostRebuildCheck(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.localContainers = fakeLocalContainerUpdatePlanner{plan: localcontainers.UpdatePlan{
		PathID:  localcontainers.PathContainerUpdatePlan,
		Mode:    "dev",
		DevMode: true,
		Target: localcontainers.UpdatePlanTarget{
			ImageRef:               "localhost/swarm-container-mvp:latest",
			Fingerprint:            "before",
			PostRebuildImageRef:    "localhost/swarm-container-mvp:latest",
			PostRebuildFingerprint: "after",
		},
	}}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/update/local-containers?dev_mode=true&post_rebuild_check=true", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("local container update plan = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "post_rebuild_fingerprint") {
		t.Fatalf("expected post rebuild fingerprint in response: %s", rec.Body.String())
	}
}

func TestMainHandlerUpdateStatusRejectsNonLoopbackWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)
	server.SetUpdateService(newStaticUpdateService(t, true))

	req := httptest.NewRequest(http.MethodGet, "http://192.0.2.10:5555/v1/update/status", nil)
	req.RemoteAddr = "192.0.2.10:43210"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-loopback update status without attach token = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestDesktopWebsocketAllowsCookieAuthenticatedHandshake(t *testing.T) {
	server := newLocalAuthTestServer(t)
	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	ts := httptest.NewServer(server.DesktopHandler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	headers := http.Header{}
	headers.Set("Origin", ts.URL)
	headers.Set("Referer", ts.URL+"/app")
	headers.Set("Sec-Fetch-Site", "same-origin")
	headers.Add("Cookie", cookie.String())

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		status := 0
		body := ""
		if resp != nil {
			status = resp.StatusCode
			if raw, readErr := io.ReadAll(resp.Body); readErr == nil {
				body = string(raw)
			}
		}
		t.Fatalf("dial websocket: %v, status=%d, body=%s", err, status, body)
	}
	defer conn.Close()
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("websocket handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
	}
}

func TestMainHandlerWebsocketAllowsCookieAuthenticatedHandshake(t *testing.T) {
	server := newLocalAuthTestServer(t)
	token, expiresAt, err := server.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	cookie := buildDesktopLocalSessionCookie(token, expiresAt, false)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	headers := http.Header{}
	headers.Set("Origin", ts.URL)
	headers.Set("Referer", ts.URL+"/app")
	headers.Set("Sec-Fetch-Site", "same-origin")
	headers.Add("Cookie", cookie.String())

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		status := 0
		body := ""
		if resp != nil {
			status = resp.StatusCode
			if raw, readErr := io.ReadAll(resp.Body); readErr == nil {
				body = string(raw)
			}
		}
		t.Fatalf("dial proxied websocket: %v, status=%d, body=%s", err, status, body)
	}
	defer conn.Close()
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("proxied websocket handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
	}
}

func TestDesktopProtectedAPIRejectsMissingDesktopSessionAndAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/vault", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Origin", "http://127.0.0.1")
	req.Header.Set("Referer", "http://127.0.0.1/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	server.DesktopHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("desktop protected API without session status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestSwarmEnrollAllowsInviteTokenWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	body := bytes.NewBufferString(`{"invite_token":"invite-123","child_swarm_id":"child-1","child_name":"Child","child_public_key":"pub-key"}`)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/swarm/enroll", body)
	req.RemoteAddr = "198.51.100.20:7777"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("swarm enroll without attach token status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestSwarmEnrollRejectsMissingInviteTokenAndAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)

	body := bytes.NewBufferString(`{"child_swarm_id":"child-1","child_name":"Child","child_public_key":"pub-key"}`)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/swarm/enroll", body)
	req.RemoteAddr = "198.51.100.20:7777"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("swarm enroll missing invite token status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestSwarmRemotePairingRequestAllowsInviteTokenWithoutAttachToken(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = true
	})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/enroll" {
			t.Fatalf("unexpected primary path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"enrollment":{"id":"enroll-remote-1"}}`))
	}))
	defer primary.Close()

	body := bytes.NewBufferString(`{"invite_token":"invite-123","primary_swarm_id":"primary-1","primary_endpoint":"` + primary.URL + `"}`)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/swarm/remote-pairing/request", body)
	req.RemoteAddr = "198.51.100.20:7777"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("swarm remote pairing request without attach token status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func newLocalAuthTestServer(t *testing.T) *Server {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "local-auth-substrate.pebble"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create event log: %v", err)
	}
	authSvc := auth.NewService(pebblestore.NewAuthStore(store), eventLog)
	securitySvc := security.NewService(pebblestore.NewClientAuthStore(store), eventLog)
	if _, err := securitySvc.EnsureAttachAuth(); err != nil {
		t.Fatalf("ensure attach auth: %v", err)
	}
	workspaceSvc := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	server := NewServer("test", authSvc, nil, nil, nil, nil, workspaceSvc, nil, securitySvc, nil, nil, nil, eventLog, stream.NewHub(eventLog))
	server.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "local-auth-test", Name: "Local Auth Test", Role: "standalone", PublicKey: "local-auth-public-key", Fingerprint: "local-auth-fingerprint"}}}

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.Host = startupconfig.DefaultHost
	cfg.Port = startupconfig.DefaultPort
	cfg.DesktopPort = startupconfig.DefaultDesktopPort
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	server.SetSwarmDesktopTargetSelectionStore(nil)
	return server
}

func setLocalAuthTestStartupConfig(t *testing.T, server *Server, mutate func(*startupconfig.FileConfig)) {
	t.Helper()
	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	mutate(&cfg)
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
}

type fakeLocalContainerUpdatePlanner struct {
	plan localcontainers.UpdatePlan
}

func (f fakeLocalContainerUpdatePlanner) RuntimeStatus(context.Context) (localcontainers.RuntimeStatus, error) {
	return localcontainers.RuntimeStatus{}, nil
}

func (f fakeLocalContainerUpdatePlanner) List(context.Context) ([]localcontainers.Container, error) {
	return nil, nil
}

func (f fakeLocalContainerUpdatePlanner) Create(context.Context, localcontainers.CreateInput) (localcontainers.Container, error) {
	return localcontainers.Container{}, nil
}

func (f fakeLocalContainerUpdatePlanner) Act(context.Context, localcontainers.ActionInput) (localcontainers.Container, error) {
	return localcontainers.Container{}, nil
}

func (f fakeLocalContainerUpdatePlanner) BulkDelete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f fakeLocalContainerUpdatePlanner) PruneMissing(context.Context) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f fakeLocalContainerUpdatePlanner) UpdatePlan(ctx context.Context, input localcontainers.UpdatePlanInput) (localcontainers.UpdatePlan, error) {
	plan := f.plan
	if input.PostRebuildCheck && plan.Target.PostRebuildFingerprint == "" {
		plan.Target.PostRebuildFingerprint = "checked"
	}
	return plan, nil
}

func (f fakeLocalContainerUpdatePlanner) RunUpdateJob(context.Context, localcontainers.UpdateJobInput) (localcontainers.UpdateJobResult, error) {
	return localcontainers.UpdateJobResult{}, nil
}

func (f fakeLocalContainerUpdatePlanner) SetHostCallbackURL(string, string) {}

func (f fakeLocalContainerUpdatePlanner) HostCallbackURL(string) (string, bool) { return "", false }

func newStaticUpdateService(t *testing.T, updateAvailable bool) *update.Service {
	t.Helper()
	_ = updateAvailable
	return update.NewService("dev", true)
}

type fakeLocalAuthSwarmService struct {
	state swarmruntime.LocalState
}

func (f fakeLocalAuthSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeLocalAuthSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeLocalAuthSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeLocalAuthSwarmService) DeleteGroup(string) error { return nil }

func (f fakeLocalAuthSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeLocalAuthSwarmService) OutgoingPeerAuthToken(string) (string, bool, error) {
	return "", false, nil
}

func (f fakeLocalAuthSwarmService) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return true, nil
}

func (f fakeLocalAuthSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeLocalAuthSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeLocalAuthSwarmService) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeLocalAuthSwarmService) SubmitEnrollment(input swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{ID: "enroll-1", InviteToken: input.InviteToken, ChildSwarmID: input.ChildSwarmID, ChildName: input.ChildName, Status: swarmruntime.EnrollmentStatusPending}, nil
}

func (f fakeLocalAuthSwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeLocalAuthSwarmService) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeLocalAuthSwarmService) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeLocalAuthSwarmService) FinalizeRemoteBootstrapChildPairing(swarmruntime.FinalizeRemoteBootstrapChildPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeLocalAuthSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeLocalAuthSwarmService) DetachToStandalone(string) error { return nil }
