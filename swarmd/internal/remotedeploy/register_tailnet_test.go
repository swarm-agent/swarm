package remotedeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestDeriveRemoteChildBackendURLPrefersRawTailnetBackend(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "laptop-c5d07eb0",
		Name:          "Laptop Child",
		TransportMode: startupconfig.NetworkModeTailscale,
	}
	want := "http://laptop-child:" + strconv.Itoa(remoteChildPorts(record.ID).Backend)
	if got := deriveRemoteChildBackendURL(record, "https://laptop-child.tailnet.ts.net"); got != want {
		t.Fatalf("deriveRemoteChildBackendURL(serve URL) = %q, want %q", got, want)
	}
	if got := deriveRemoteChildBackendURL(record, ""); got != want {
		t.Fatalf("deriveRemoteChildBackendURL(empty) = %q, want %q", got, want)
	}
	raw := "http://laptop-child:21234"
	if got := deriveRemoteChildBackendURL(record, raw); got != raw {
		t.Fatalf("deriveRemoteChildBackendURL(raw) = %q, want %q", got, raw)
	}
}

func TestParseRemoteBootstrapURLsPrefersTailnetBackend(t *testing.T) {
	authURL, endpoint := parseRemoteBootstrapURLs(strings.Join([]string{
		"TAILSCALE_AUTH_URL=https://login.tailscale.com/a/example",
		"SWARM_TAILNET_URL=https://laptop-child.tailnet.ts.net",
		"SWARM_TAILNET_BACKEND_URL=http://laptop-child:7781",
	}, "\n"))
	if authURL != "https://login.tailscale.com/a/example" {
		t.Fatalf("authURL = %q", authURL)
	}
	if endpoint != "http://laptop-child:7781" {
		t.Fatalf("endpoint = %q, want raw backend", endpoint)
	}
}

func TestTryRegisterTailscaleRemoteNodeWritesNodeRegistry(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarmd.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()

	finalizeCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/remote-pairing/finalize":
			finalizeCalled = true
			if r.Header.Get("X-Swarm-Peer-ID") != "host-swarm" || r.Header.Get("X-Swarm-Peer-Token") != "invite-secret" {
				t.Fatalf("peer auth headers = %q/%q", r.Header.Get("X-Swarm-Peer-ID"), r.Header.Get("X-Swarm-Peer-Token"))
			}
			var req remotePairingFinalizeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode finalize request: %v", err)
			}
			if req.PrimarySwarmID != "host-swarm" || req.PrimaryPublicKey == "" || req.PrimaryFingerprint == "" {
				t.Fatalf("finalize request = %+v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/state":
			if r.Header.Get("X-Swarm-Peer-ID") != "host-swarm" || r.Header.Get("X-Swarm-Peer-Token") != "invite-secret" {
				t.Fatalf("state peer auth headers = %q/%q", r.Header.Get("X-Swarm-Peer-ID"), r.Header.Get("X-Swarm-Peer-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"state":{"node":{"swarm_id":"child-real","name":"Real Child","role":"child","public_key":"child-pub","fingerprint":"child-fp","transports":[{"kind":"tailscale","primary":"http://real-child:7781","all":["http://real-child:7781"]}]},"pairing":{"pairing_state":"paired","parent_swarm_id":"host-swarm"},"trusted_peers":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutGroup(pebblestore.SwarmGroupRecord{ID: "group-1", Name: "Group 1", HostSwarmID: "host-swarm"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	swarmSvc := swarmruntime.NewService(swarmStore, nil, nil)
	if _, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{
		SwarmID:     "host-swarm",
		Name:        "Host",
		Role:        "master",
		SwarmMode:   true,
		PublicKey:   "host-pub",
		Fingerprint: "host-fp",
		Transports:  []swarmruntime.TransportSummary{{Kind: "tailscale", Primary: "https://host.example", All: []string{"https://host.example"}}},
	}); err != nil {
		t.Fatalf("ensure host state: %v", err)
	}
	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), swarmSvc, swarmStore, nil, nil, nil, "", "")
	record := pebblestore.RemoteDeploySessionRecord{
		ID:               "remote-child-1",
		Name:             "Remote Child",
		TransportMode:    startupconfig.NetworkModeTailscale,
		RemoteEndpoint:   server.URL,
		RemoteTailnetURL: server.URL,
		GroupID:          "group-1",
		SessionToken:     "session-secret",
		InviteToken:      "invite-secret",
	}
	registered, err := svc.tryRegisterTailscaleRemoteNode(context.Background(), &record, time.Second)
	if err != nil {
		t.Fatalf("tryRegisterTailscaleRemoteNode() error = %v", err)
	}
	if !registered {
		t.Fatalf("tryRegisterTailscaleRemoteNode() registered = false")
	}
	if !finalizeCalled {
		t.Fatalf("remote pairing finalize was not called")
	}
	if record.Status != "attached" {
		t.Fatalf("record.Status = %q, want attached", record.Status)
	}
	if record.EnrollmentStatus != "" {
		t.Fatalf("record.EnrollmentStatus = %q, want empty", record.EnrollmentStatus)
	}
	if !strings.Contains(record.LastProgress, "registered node") {
		t.Fatalf("record.LastProgress = %q, want registered node", record.LastProgress)
	}

	node, ok, err := pebblestore.NewSwarmNodeStore(store).Get("child-real")
	if err != nil || !ok {
		t.Fatalf("get registered node ok=%t err=%v", ok, err)
	}
	if node.BackendURL != server.URL {
		t.Fatalf("node.BackendURL = %q, want %q", node.BackendURL, server.URL)
	}
	if node.Role != "child" || node.Kind != "remote" || node.Transport != "tailscale" || node.Source != "remote-deploy" || node.Status != "online" {
		t.Fatalf("node registry fields = %+v", node)
	}
	membership, ok, err := swarmStore.GetGroupMembership("group-1", "child-real")
	if err != nil || !ok {
		t.Fatalf("get registered group membership ok=%t err=%v", ok, err)
	}
	if membership.Name != "Real Child" || membership.SwarmRole != "child" || membership.MembershipRole != "member" {
		t.Fatalf("group membership = %+v", membership)
	}
	peer, ok, err := swarmStore.GetTrustedPeer("child-real")
	if err != nil || !ok {
		t.Fatalf("get trusted peer ok=%t err=%v", ok, err)
	}
	if peer.OutgoingPeerAuthToken != "invite-secret" || peer.IncomingPeerAuthHash != swarmruntime.HashPeerAuthToken("session-secret") {
		t.Fatalf("trusted peer auth = %+v", peer)
	}
	if peer.PublicKey != "child-pub" || peer.Fingerprint != "child-fp" || peer.ParentSwarmID != "host-swarm" {
		t.Fatalf("trusted peer identity = %+v", peer)
	}
}

func TestRefreshRemoteSessionStateRegistersTailscaleEndpointWithoutEnrollment(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarmd.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/remote-pairing/finalize":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/state":
			if r.Header.Get("X-Swarm-Peer-ID") != "host-swarm" || r.Header.Get("X-Swarm-Peer-Token") != "invite-secret" {
				t.Fatalf("state peer auth headers = %q/%q", r.Header.Get("X-Swarm-Peer-ID"), r.Header.Get("X-Swarm-Peer-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"state":{"node":{"swarm_id":"refresh-real","name":"Refresh Child","role":"child"},"pairing":{"pairing_state":"paired","parent_swarm_id":"host-swarm"},"trusted_peers":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	swarmStore := pebblestore.NewSwarmStore(store)
	swarmSvc := swarmruntime.NewService(swarmStore, nil, nil)
	if _, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{SwarmID: "host-swarm", Name: "Host", Role: "master", SwarmMode: true}); err != nil {
		t.Fatalf("ensure host state: %v", err)
	}
	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), swarmSvc, swarmStore, nil, nil, nil, "", "")
	oldRunner := remoteSSHCommandRunner
	remoteSSHCommandRunner = func(context.Context, string, string) (string, error) {
		return "", nil
	}
	defer func() { remoteSSHCommandRunner = oldRunner }()
	record, err := svc.store.Put(pebblestore.RemoteDeploySessionRecord{
		SSHSessionTarget: "test-ssh",
		ID:               "remote-child-refresh",
		Name:             "Refresh Child",
		Status:           "auth_required",
		TransportMode:    startupconfig.NetworkModeTailscale,
		RemoteEndpoint:   server.URL,
		RemoteTailnetURL: server.URL,
		RemoteAuthURL:    "https://login.tailscale.example/abc",
		SessionToken:     "session-secret",
		InviteToken:      "invite-secret",
	})
	if err != nil {
		t.Fatalf("put remote deploy session: %v", err)
	}
	refreshed, err := svc.refreshRemoteSessionState(context.Background(), record)
	if err != nil {
		t.Fatalf("refreshRemoteSessionState() error = %v", err)
	}
	if refreshed.Status != "attached" {
		t.Fatalf("refreshed.Status = %q, want attached", refreshed.Status)
	}
	if refreshed.EnrollmentID != "" || refreshed.EnrollmentStatus != "" {
		t.Fatalf("refresh should not require enrollment: %+v", refreshed)
	}
	if _, ok, err := pebblestore.NewSwarmNodeStore(store).Get("refresh-real"); err != nil || !ok {
		t.Fatalf("registered node ok=%t err=%v", ok, err)
	}
}

func TestRefreshRemoteSessionStateDoesNotResyncPendingEnrollmentAfterTailscaleRegistration(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarmd.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/remote-pairing/finalize":
			if r.Header.Get("X-Swarm-Peer-ID") != "host-swarm" || r.Header.Get("X-Swarm-Peer-Token") != "invite-secret" {
				t.Fatalf("finalize peer auth headers = %q/%q", r.Header.Get("X-Swarm-Peer-ID"), r.Header.Get("X-Swarm-Peer-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/swarm/state":
			if r.Header.Get("X-Swarm-Peer-ID") != "host-swarm" || r.Header.Get("X-Swarm-Peer-Token") != "invite-secret" {
				t.Fatalf("state peer auth headers = %q/%q", r.Header.Get("X-Swarm-Peer-ID"), r.Header.Get("X-Swarm-Peer-Token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"state":{"node":{"swarm_id":"real-child","name":"Real Child","role":"child","public_key":"child-pub","fingerprint":"child-fp"},"pairing":{"pairing_state":"paired","parent_swarm_id":"host-swarm"},"trusted_peers":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutGroup(pebblestore.SwarmGroupRecord{ID: "group-1", Name: "Group 1", HostSwarmID: "host-swarm"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	swarmSvc := swarmruntime.NewService(swarmStore, nil, nil)
	if _, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{SwarmID: "host-swarm", Name: "Host", Role: "master", SwarmMode: true}); err != nil {
		t.Fatalf("ensure host state: %v", err)
	}
	invite, err := swarmSvc.EnsureInvite(swarmruntime.EnsureInviteInput{Token: "invite-secret", PrimarySwarmID: "host-swarm", PrimaryName: "Host", GroupID: "group-1", TransportMode: startupconfig.NetworkModeTailscale, TTL: time.Minute})
	if err != nil {
		t.Fatalf("ensure invite: %v", err)
	}
	enrollment, err := swarmSvc.SubmitEnrollment(swarmruntime.SubmitEnrollmentInput{
		InviteToken:    "invite-secret",
		PrimarySwarmID: "host-swarm",
		GroupID:        "group-1",
		ChildSwarmID:   "pending-child",
		ChildName:      "Pending Child",
		ChildRole:      "child",
		ChildPublicKey: "pending-pub",
		TransportMode:  startupconfig.NetworkModeTailscale,
	})
	if err != nil {
		t.Fatalf("submit enrollment for invite %q: %v", invite.ID, err)
	}

	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), swarmSvc, swarmStore, nil, nil, nil, "", "")
	oldRunner := remoteSSHCommandRunner
	remoteSSHCommandRunner = func(context.Context, string, string) (string, error) {
		return "", nil
	}
	defer func() { remoteSSHCommandRunner = oldRunner }()
	record, err := svc.store.Put(pebblestore.RemoteDeploySessionRecord{
		SSHSessionTarget: "test-ssh",
		ID:               "remote-child-refresh",
		Name:             "Remote Child",
		Status:           "attached",
		TransportMode:    startupconfig.NetworkModeTailscale,
		RemoteEndpoint:   server.URL,
		RemoteTailnetURL: server.URL,
		GroupID:          "group-1",
		EnrollmentID:     enrollment.ID,
		EnrollmentStatus: enrollment.Status,
		ChildSwarmID:     "pending-child",
		ChildName:        "Pending Child",
		SessionToken:     "session-secret",
		InviteToken:      "invite-secret",
	})
	if err != nil {
		t.Fatalf("put remote deploy session: %v", err)
	}

	refreshed, err := svc.refreshRemoteSessionState(context.Background(), record)
	if err != nil {
		t.Fatalf("refreshRemoteSessionState() error = %v", err)
	}
	if refreshed.Status != "attached" {
		t.Fatalf("refreshed.Status = %q, want attached", refreshed.Status)
	}
	if refreshed.EnrollmentID != "" || refreshed.EnrollmentStatus != "" {
		t.Fatalf("refreshed enrollment drift was not cleared: id=%q status=%q", refreshed.EnrollmentID, refreshed.EnrollmentStatus)
	}
	if refreshed.ChildSwarmID != "real-child" {
		t.Fatalf("refreshed.ChildSwarmID = %q, want real child", refreshed.ChildSwarmID)
	}
}
