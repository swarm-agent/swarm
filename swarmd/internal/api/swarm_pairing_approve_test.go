package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/localcontainers"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestSwarmRemotePairingPendingListsManagerApprovalRequests(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	manager.remotePairingPending["pair-list-1"] = swarmRemotePairingPendingRequest{
		ID:                 "pair-list-1",
		ManagerSwarmID:     "manager-swarm-1",
		ManagerName:        "Manager A",
		ManagerEndpoint:    "https://manager-a.example.ts.net",
		ManagedSwarmID:     "managed-swarm-1",
		ManagedName:        "Managed B",
		ManagedFingerprint: "managed-fingerprint",
		ManagedEndpoint:    "https://managed-b.example.ts.net",
		CeremonyCode:       "ABC123",
		TransportMode:      startupconfig.NetworkModeTailscale,
		CreatedAt:          time.Unix(123, 0),
	}

	token, expiresAt, err := manager.desktopLocalSessions.Ensure(time.Now())
	if err != nil {
		t.Fatalf("ensure desktop local session: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5555/v1/swarm/remote-pairing/pending", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "http://127.0.0.1:5555")
	req.Header.Set("Referer", "http://127.0.0.1:5555/app")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(buildDesktopLocalSessionCookie(token, expiresAt, false))
	manager.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingPendingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode pending response: %v", err)
	}
	if !response.OK || response.Count != 1 || len(response.Items) != 1 {
		t.Fatalf("pending response = %+v", response)
	}
	item := response.Items[0]
	if item.RequestID != "pair-list-1" || item.ManagedName != "Managed B" || item.CeremonyCode != "ABC123" {
		t.Fatalf("pending item = %+v", item)
	}
}

func TestSwarmRemotePairingApproveManagerApprovesAndReturnsFinalizeMaterial(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	managerPublicKey, _, managerFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate manager keypair: %v", err)
	}
	managedPublicKey, _, managedFingerprint, err := swarmruntime.GenerateNodeKeypair()
	if err != nil {
		t.Fatalf("generate managed keypair: %v", err)
	}
	manager.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A", PublicKey: managerPublicKey, Fingerprint: managerFingerprint}}}
	manager.SetDeployContainerService(fakeManagedHostInitialSyncDeployService{bundle: deployruntime.ManagedHostInitialSyncBundle{UserID: "user_local_auth_test", AccountScopeID: "acct_local_auth_test", CredentialBundle: deployruntime.ContainerSyncCredentialBundle{UserID: "user_local_auth_test", AccountScopeID: "acct_local_auth_test"}}})
	setLocalAuthTestStartupConfig(t, manager, func(cfg *startupconfig.FileConfig) {
		cfg.SwarmName = "Manager A"
		cfg.TailscaleURL = "https://manager-a.example.ts.net"
	})

	requestID := "pair-approve-1"
	offer := mustManagedPairingOfferForTest(t, managedPublicKey, managedFingerprint)
	var finalizeSeen bool
	managedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/remote-pairing/finalize" {
			t.Fatalf("unexpected managed request path %s", r.URL.Path)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "manager-swarm-1" || r.Header.Get(peerAuthTokenHeader) != offer.Token {
			t.Fatalf("finalize peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		var req swarmRemotePairingFinalizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode finalize request: %v", err)
		}
		if req.ManagerSwarmID != "manager-swarm-1" || req.PeerAuthToken != "managed-to-manager-token" || req.IncomingPeerAuthToken != offer.Token {
			t.Fatalf("finalize payload = %+v", req)
		}
		if req.InitialSync.UserID != "user_local_auth_test" || req.InitialSync.AccountScopeID != "acct_local_auth_test" {
			t.Fatalf("initial sync identity = %q/%q", req.InitialSync.UserID, req.InitialSync.AccountScopeID)
		}
		finalizeSeen = true
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer managedServer.Close()
	manager.remotePairingPending[requestID] = swarmRemotePairingPendingRequest{
		ID:                          requestID,
		InviteToken:                 offer.Token,
		ManagerSwarmID:              "manager-swarm-1",
		ManagerName:                 "Manager A",
		ManagerPublicKey:            managerPublicKey,
		ManagerFingerprint:          managerFingerprint,
		ManagerEndpoint:             "https://manager-a.example.ts.net",
		ManagedSwarmID:              "managed-swarm-1",
		ManagedName:                 "Managed B",
		ManagedPublicKey:            managedPublicKey,
		ManagedFingerprint:          managedFingerprint,
		ManagedEndpoint:             managedServer.URL,
		CeremonyCode:                offer.Ceremony.Code,
		TransportMode:               startupconfig.NetworkModeTailscale,
		ManagerRendezvousTransports: []onboardingTransportPayload{{Kind: startupconfig.NetworkModeTailscale, Primary: "https://manager-a.example.ts.net", All: []string{"https://manager-a.example.ts.net"}}},
		ManagedRendezvousTransports: offer.RendezvousTransports,
		ManagerToManagedPeerToken:   offer.Token,
		ManagedToManagerPeerToken:   "managed-to-manager-token",
		CreatedAt:                   time.Now(),
	}

	rec := postRemotePairingJSONWithDesktopSession(t, manager, "/v1/swarm/remote-pairing/approve", map[string]any{
		"request_id": requestID,
		"approve":    true,
		"confirmed":  true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response swarmRemotePairingApprovalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if !response.OK || response.Status != startupconfig.PairingStatePaired {
		t.Fatalf("approve ok/status = %v/%q", response.OK, response.Status)
	}
	if response.Invite.Token != offer.Token || response.Enrollment.ID == "" {
		t.Fatalf("approve response missing invite/enrollment: %+v", response)
	}
	if response.Pairing.PairingState != startupconfig.PairingStatePaired || response.Pairing.ParentSwarmID != "manager-swarm-1" {
		t.Fatalf("managed pairing material missing: %+v", response.Pairing)
	}
	if response.Enrollment.Status != swarmruntime.EnrollmentStatusApproved {
		t.Fatalf("enrollment status = %q, want approved", response.Enrollment.Status)
	}
	if !finalizeSeen {
		t.Fatalf("manager approval did not call managed finalize")
	}
	if _, ok := manager.remotePairingPending[requestID]; ok {
		t.Fatalf("approved pending request was not cleared")
	}
}

func TestSwarmRemotePairingApproveRejectsInitialSyncIdentityMismatch(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	manager.swarm = fakeLocalAuthSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "manager-swarm-1", Name: "Manager A"}}}
	manager.SetDeployContainerService(fakeManagedHostInitialSyncDeployService{bundle: deployruntime.ManagedHostInitialSyncBundle{UserID: "other-user", AccountScopeID: "acct_local_auth_test"}})
	requestID := "pair-identity-mismatch"
	manager.remotePairingPending[requestID] = swarmRemotePairingPendingRequest{
		ID:                        requestID,
		InviteToken:               "invite-token",
		ManagerSwarmID:            "manager-swarm-1",
		ManagedSwarmID:            "managed-swarm-1",
		ManagedEndpoint:           "http://127.0.0.1:1",
		CeremonyCode:              "ABC123",
		ManagerToManagedPeerToken: "manager-to-managed-token",
		ManagedToManagerPeerToken: "managed-to-manager-token",
		CreatedAt:                 time.Now(),
	}

	rec := postRemotePairingJSONWithDesktopSession(t, manager, "/v1/swarm/remote-pairing/approve", map[string]any{
		"request_id":    requestID,
		"approve":       true,
		"confirmed":     true,
		"ceremony_code": "ABC123",
	})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "identity envelope does not match") {
		t.Fatalf("approve identity mismatch status/body = %d/%s", rec.Code, rec.Body.String())
	}
	if _, ok := manager.remotePairingPending[requestID]; !ok {
		t.Fatalf("identity mismatch cleared pending request")
	}
}

func TestSwarmRemotePairingApproveRequiresExplicitConfirmation(t *testing.T) {
	manager := newLocalAuthTestServer(t)
	requestID := "pair-confirm-required"
	manager.remotePairingPending[requestID] = swarmRemotePairingPendingRequest{
		ID:           requestID,
		CeremonyCode: "ABC123",
		CreatedAt:    time.Now(),
	}

	rec := postRemotePairingJSONWithDesktopSession(t, manager, "/v1/swarm/remote-pairing/approve", map[string]any{
		"request_id":    requestID,
		"approve":       true,
		"ceremony_code": "ABC123",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "code confirmation is required") {
		t.Fatalf("approve without confirmation status/body = %d/%s", rec.Code, rec.Body.String())
	}
	if _, ok := manager.remotePairingPending[requestID]; !ok {
		t.Fatalf("unconfirmed approve cleared pending request")
	}
}

func TestSwarmRemotePairingFinalizePersistsManagedStartupConfig(t *testing.T) {
	server := newLocalAuthTestServer(t)
	setLocalAuthTestStartupConfig(t, server, func(cfg *startupconfig.FileConfig) {
		cfg.Child = false
		cfg.SwarmRole = ""
		cfg.ParentSwarmID = ""
		cfg.PairingState = ""
		cfg.TailscaleURL = "https://managed-b.example.ts.net"
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/swarm/remote-pairing/finalize", strings.NewReader(`{"manager_swarm_id":"manager-swarm-1","manager_name":"Manager A","peer_auth_token":"managed-to-manager-token","incoming_peer_auth_token":"manager-to-managed-token","transport_mode":"tailscale"}`))
	req.RemoteAddr = "100.64.0.10:443"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "manager-swarm-1")
	req.Header.Set(peerAuthTokenHeader, "manager-to-managed-token")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("finalize status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := server.loadStartupConfig()
	if err != nil {
		t.Fatalf("load startup config: %v", err)
	}
	if !cfg.Child {
		t.Fatalf("config child = false, want true")
	}
	if cfg.SwarmRole != startupconfig.SwarmRoleManaged {
		t.Fatalf("config SwarmRole = %q, want %q", cfg.SwarmRole, startupconfig.SwarmRoleManaged)
	}
	if cfg.ParentSwarmID != "manager-swarm-1" {
		t.Fatalf("config ParentSwarmID = %q, want manager-swarm-1", cfg.ParentSwarmID)
	}
	if cfg.PairingState != startupconfig.PairingStatePaired {
		t.Fatalf("config PairingState = %q, want paired", cfg.PairingState)
	}
	if cfg.TailscaleURL != "https://managed-b.example.ts.net" {
		t.Fatalf("config tailscale URL changed unexpectedly: %q", cfg.TailscaleURL)
	}
}

func TestSwarmRemotePairingFinalizeRequiresPeerAuth(t *testing.T) {
	server := newLocalAuthTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/swarm/remote-pairing/finalize", strings.NewReader(`{"manager_swarm_id":"manager-swarm-1"}`))
	req.RemoteAddr = "100.64.0.10:443"
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("finalize without peer auth status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

type fakeManagedHostInitialSyncDeployService struct {
	bundle deployruntime.ManagedHostInitialSyncBundle
	err    error
}

func (f fakeManagedHostInitialSyncDeployService) RuntimeStatus(context.Context) (deployruntime.ContainerRuntimeStatus, error) {
	return deployruntime.ContainerRuntimeStatus{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) List(context.Context) ([]deployruntime.ContainerDeployment, error) {
	return nil, nil
}

func (f fakeManagedHostInitialSyncDeployService) Create(context.Context, deployruntime.ContainerCreateInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) Act(context.Context, deployruntime.ContainerActionInput) (deployruntime.ContainerDeployment, error) {
	return deployruntime.ContainerDeployment{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) Delete(context.Context, []string) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) ChildAttachState(context.Context, deployruntime.ContainerAttachStatusInput) (swarmruntime.LocalState, error) {
	return swarmruntime.LocalState{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) AttachRequest(context.Context, deployruntime.ContainerAttachRequestInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) AttachStatus(context.Context, deployruntime.ContainerAttachStatusInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) AttachApprove(context.Context, deployruntime.ContainerAttachApproveInput) (deployruntime.ContainerAttachState, error) {
	return deployruntime.ContainerAttachState{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) FinalizeAttachFromHost(context.Context, deployruntime.ContainerAttachFinalizeInput) error {
	return nil
}

func (f fakeManagedHostInitialSyncDeployService) SyncCredentialBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) SyncAgentBundle(context.Context, deployruntime.ContainerSyncCredentialRequestInput) (deployruntime.ContainerSyncAgentBundle, error) {
	return deployruntime.ContainerSyncAgentBundle{}, nil
}

func (f fakeManagedHostInitialSyncDeployService) WorkspaceBootstrap(context.Context, deployruntime.ContainerWorkspaceBootstrapRequestInput) ([]deployruntime.ContainerWorkspaceBootstrap, error) {
	return nil, nil
}

func (f fakeManagedHostInitialSyncDeployService) AutoAttachChild(context.Context) error {
	return nil
}

func (f fakeManagedHostInitialSyncDeployService) UnlockManagedLocalChildVaults(context.Context) error {
	return nil
}

func (f fakeManagedHostInitialSyncDeployService) ManagedHostInitialSyncBundle(context.Context, string, string) (deployruntime.ManagedHostInitialSyncBundle, error) {
	return f.bundle, f.err
}
