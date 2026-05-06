package runtime

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/config"
)

func TestLocalTransportSocketPerm(t *testing.T) {
	if got := localTransportSocketPerm(); got != 0o666 {
		t.Fatalf("localTransportSocketPerm() = %04o, want %04o", got, 0o666)
	}
}

func TestLocalTransportSocketDirPerm(t *testing.T) {
	if got := localTransportSocketDirPerm(); got != 0o711 {
		t.Fatalf("localTransportSocketDirPerm() = %04o, want %04o", got, 0o711)
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

func TestDaemonArtifactPathsStayUnderConfiguredDaemonRoots(t *testing.T) {
	cfg := config.Config{
		DataDir:  "/var/lib/swarmd",
		DBPath:   "/var/lib/swarmd/swarmd.pebble",
		LockPath: "/run/swarmd/swarmd.lock",
	}
	if got, want := filepath.Clean(cfg.DBPath), filepath.Join(cfg.DataDir, "swarmd.pebble"); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if got, want := daemonSecretStorePath(cfg), filepath.Join(cfg.DataDir, "swarmd-secrets.pebble"); got != want {
		t.Fatalf("secret store path = %q, want %q", got, want)
	}
	if got, want := filepath.Clean(cfg.LockPath), filepath.Join("/run/swarmd", "swarmd.lock"); got != want {
		t.Fatalf("LockPath = %q, want %q", got, want)
	}
}
