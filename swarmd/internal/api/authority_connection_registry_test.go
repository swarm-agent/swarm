package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestAuthorityConnectionRegistryRejectsExpiredConnection(t *testing.T) {
	registry := newAuthorityConnectionRegistry()
	registry.Upsert(AuthorityConnection{
		AccountScopeID:       "account-1",
		AuthorityHostSwarmID: "managed-host",
		TransportKind:        authorityConnectionTransportHTTP,
		TransportRef:         "https://managed.example.test",
		Health:               AuthorityConnectionHealthOnline,
		LastSeenAt:           time.Now().Add(-time.Hour),
		ExpiresAt:            time.Now().Add(-time.Minute),
	})
	if conn, ok := registry.Resolve("account-1", "managed-host"); ok {
		t.Fatalf("expired connection resolved: %+v", conn)
	}
}

func TestRoutedSessionTargetResolvesCurrentAuthorityConnectionWithoutMutatingSessionExecution(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer second.Close()

	sessionID := "session-authority-dynamic"
	routeRecord := pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "managed-container",
		HostSwarmID:          "managed-host",
		HostContainerID:      "managed-container-1",
		RuntimeWorkspacePath: "/workspace",
	}
	if _, err := routeStore.Put(routeRecord); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(routeRecord); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-container", Name: "Managed Container", Relationship: "child", OwnerHostSwarmID: "managed-host", OwnerHostContainerID: "managed-container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-host", Name: "Managed Host", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if err := server.RegisterAuthorityConnection(AuthorityConnection{AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-host", TransportKind: authorityConnectionTransportHTTP, TransportRef: first.URL}); err != nil {
		t.Fatalf("register first authority: %v", err)
	}
	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil || !ok {
		t.Fatalf("resolve first target ok=%t err=%v", ok, err)
	}
	if target.BackendURL != first.URL {
		t.Fatalf("backend url = %q, want first authority %q", target.BackendURL, first.URL)
	}
	if err := server.RegisterAuthorityConnection(AuthorityConnection{AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "managed-host", TransportKind: authorityConnectionTransportHTTP, TransportRef: second.URL}); err != nil {
		t.Fatalf("register second authority: %v", err)
	}
	target, ok, err = server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil || !ok {
		t.Fatalf("resolve second target ok=%t err=%v", ok, err)
	}
	if target.BackendURL != second.URL {
		t.Fatalf("backend url = %q, want second authority %q", target.BackendURL, second.URL)
	}
	record, ok, err := routeStore.Get(sessionID)
	if err != nil || !ok {
		t.Fatalf("get route ok=%t err=%v", ok, err)
	}
	if record.ChildBackendURL != "" || record.HostWorkspacePath != "" || record.RuntimeWorkspacePath != "/workspace" {
		t.Fatalf("session execution mutated transport/path fields: %+v", record)
	}
}

func TestRoutedSessionTargetFailsWhenAuthorityConnectionMissing(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-authority-missing"
	routeRecord := pebblestore.SessionRouteRecord{SessionID: sessionID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "managed-container", HostSwarmID: "managed-host", RuntimeWorkspacePath: "/workspace"}
	if _, err := routeStore.Put(routeRecord); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(routeRecord); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-container", Name: "Managed Container", Relationship: "child", OwnerHostSwarmID: "managed-host", OwnerHostContainerID: "managed-container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SwarmID: "managed-host", Name: "Managed Host", Relationship: "managed", Status: "online"}); err != nil {
		t.Fatalf("upsert authority runtime: %v", err)
	}
	if _, ok, err := server.routedSessionTarget(testPrincipal(), sessionID); err == nil || ok {
		t.Fatalf("routed target ok=%t err=%v, want missing authority error", ok, err)
	}
}

func TestTopologyRuntimeTargetSelectableWithoutBackendWhenAuthorityConnectionExists(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "authority-targets.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	topologyStore := pebblestore.NewTopologyStore(store)
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{SwarmID: "managed-host", AccountScopeID: "account-1", UserID: "user-1", Name: "Managed Host", Relationship: "managed", BackendURL: "http://managed.example.test", Status: "online"}); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{SwarmID: "managed-container", AccountScopeID: "account-1", UserID: "user-1", Name: "Managed Container", Relationship: "child", OwnerHostSwarmID: "managed-host", OwnerHostContainerID: "container-1", Status: "attached"}); err != nil {
		t.Fatalf("upsert container: %v", err)
	}
	server := &Server{topology: topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, nil), authorityConnections: newAuthorityConnectionRegistry()}
	if err := server.RegisterAuthorityConnection(AuthorityConnection{AccountScopeID: "account-1", AuthorityHostSwarmID: "managed-host", TransportKind: authorityConnectionTransportHTTP, TransportRef: "http://managed.example.test"}); err != nil {
		t.Fatalf("register authority: %v", err)
	}
	targets, err := listRemoteDeployTargetsForAccount(server, nil, "account-1")
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("target count = %d, targets=%+v", len(targets), targets)
	}
	if targets[0].SwarmID != "managed-container" || targets[0].BackendURL != "" || !targets[0].Selectable {
		t.Fatalf("unexpected target: %+v", targets[0])
	}
}
