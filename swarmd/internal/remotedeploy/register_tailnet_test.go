package remotedeploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

func TestTryRegisterTailscaleRemoteNodeWritesNodeRegistry(t *testing.T) {
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
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), nil, nil, nil, nil, nil, "", "")
	record := pebblestore.RemoteDeploySessionRecord{
		ID:               "remote-child-1",
		Name:             "Remote Child",
		TransportMode:    startupconfig.NetworkModeTailscale,
		RemoteEndpoint:   server.URL,
		RemoteTailnetURL: server.URL,
	}
	registered, err := svc.tryRegisterTailscaleRemoteNode(context.Background(), &record, time.Second)
	if err != nil {
		t.Fatalf("tryRegisterTailscaleRemoteNode() error = %v", err)
	}
	if !registered {
		t.Fatalf("tryRegisterTailscaleRemoteNode() registered = false")
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

	node, ok, err := pebblestore.NewSwarmNodeStore(store).Get("remote-deploy:remote-child-1")
	if err != nil || !ok {
		t.Fatalf("get registered node ok=%t err=%v", ok, err)
	}
	if node.BackendURL != server.URL {
		t.Fatalf("node.BackendURL = %q, want %q", node.BackendURL, server.URL)
	}
	if node.Role != "child" || node.Kind != "remote" || node.Transport != "tailscale" || node.Source != "remote-deploy" || node.Status != "online" {
		t.Fatalf("node registry fields = %+v", node)
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
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), nil, nil, nil, nil, nil, "", "")
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
	if _, ok, err := pebblestore.NewSwarmNodeStore(store).Get("remote-deploy:remote-child-refresh"); err != nil || !ok {
		t.Fatalf("registered node ok=%t err=%v", ok, err)
	}
}
