package runtime

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNewWorkspaceMapServiceUsesDaemonStore(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "daemon.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := newWorkspaceMapService(store)
	created, err := svc.GetOrCreateDefault("account-a")
	if err != nil {
		t.Fatalf("create default Workspace Map: %v", err)
	}
	persisted, ok, err := pebblestore.NewWorkspaceMapStore(store).GetForAccount("account-a")
	if err != nil || !ok {
		t.Fatalf("load Workspace Map from daemon store: ok=%t err=%v", ok, err)
	}
	if persisted != created {
		t.Fatalf("daemon Workspace Map service used a different store: got %+v want %+v", persisted, created)
	}
}

func TestLocalTransportSocketPerm(t *testing.T) {
	if got := localTransportSocketPerm(); got != 0o600 {
		t.Fatalf("localTransportSocketPerm() = %04o, want %04o", got, 0o600)
	}
}

func TestLocalTransportSocketDirPerm(t *testing.T) {
	if got := localTransportSocketDirPerm(); got != 0o700 {
		t.Fatalf("localTransportSocketDirPerm() = %04o, want %04o", got, 0o700)
	}
}

func TestShouldEnableLocalTransport(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		want       bool
	}{
		{name: "loopback", listenAddr: "127.0.0.1:7781", want: true},
		{name: "private ipv4", listenAddr: "172.17.0.1:7781", want: true},
		{name: "wildcard", listenAddr: "0.0.0.0:7781", want: true},
		{name: "hostname", listenAddr: "swarmbox.local:7781", want: true},
		{name: "missing host", listenAddr: ":7781", want: false},
		{name: "invalid", listenAddr: "not-an-addr", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldEnableLocalTransport(tt.listenAddr); got != tt.want {
				t.Fatalf("shouldEnableLocalTransport(%q) = %t, want %t", tt.listenAddr, got, tt.want)
			}
		})
	}
}
