package remotedeploy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func writeTestTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
}

func TestValidateImageArchiveAcceptsValidArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.tar.gz")
	writeTestTarGz(t, path, map[string]string{
		"manifest.json": "{}",
		"layer.tar":     "hello world",
	})
	if err := validateImageArchive(path); err != nil {
		t.Fatalf("validateImageArchive(valid) error = %v", err)
	}
}

func TestValidateImageArchiveRejectsTruncatedArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.tar.gz")
	writeTestTarGz(t, path, map[string]string{
		"manifest.json": "{}",
		"layer.tar":     strings.Repeat("x", 64*1024),
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if err := os.Truncate(path, info.Size()-128); err != nil {
		t.Fatalf("truncate archive: %v", err)
	}
	if err := validateImageArchive(path); err == nil {
		t.Fatalf("validateImageArchive(truncated) expected error")
	}
}

func TestRemoteImageSignatureChangesWithInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "deploy", "container-mvp", "Containerfile"), "FROM ubuntu:24.04\n")
	writeTestFile(t, filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmd"), "swarmd-a")
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmctl"), "swarmctl-a")
	writeTestFile(t, filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"), "fff")
	writeTestFile(t, filepath.Join(root, ".tools", "go", "VERSION"), "go1.25.0\n")
	writeTestFile(t, filepath.Join(root, ".tools", "go", "go.env"), "GOTOOLCHAIN=local\n")
	writeTestFile(t, filepath.Join(root, "web", "dist", "index.html"), "<html>a</html>")

	first, err := remoteImageSignature(root)
	if err != nil {
		t.Fatalf("first signature: %v", err)
	}
	second, err := remoteImageSignature(root)
	if err != nil {
		t.Fatalf("second signature: %v", err)
	}
	if first != second {
		t.Fatalf("expected stable signature, got %q vs %q", first, second)
	}

	writeTestFile(t, filepath.Join(root, "web", "dist", "index.html"), "<html>b</html>")
	changed, err := remoteImageSignature(root)
	if err != nil {
		t.Fatalf("changed signature: %v", err)
	}
	if changed == first {
		t.Fatalf("expected signature to change after input mutation")
	}
}

func TestRemoteInstallerScriptUsesVersionedImageAndSkipsLoadWhenPresent(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarm/remote-deploy/test",
		RemoteRuntime: "docker",
		SudoMode:      "sudo",
		SystemdUnit:   "swarm-remote-child-test.service",
		ImageRef:      "localhost/swarm-container-mvp:remote-abc123",
	}
	script := remoteInstallerScript(record)
	for _, needle := range []string{
		`remote_root='/var/lib/swarm/remote-deploy/test'`,
		`runtime='docker'`,
		`container_config_home='/var/lib/swarm-config'`,
		`config_mount_target="$container_config_home/swarm/swarm.conf"`,
		`tailscale_state_dir='/var/lib/swarm/remote-deploy/test/state/tailscale'`,
		`swarmd_state_dir='/var/lib/swarm/remote-deploy/test/state/swarmd'`,
		`sudo mkdir -p '/var/lib/swarm/remote-deploy/test/state/tailscale'`,
		`sudo mkdir -p '/var/lib/swarm/remote-deploy/test/state/swarmd'`,
		`--cap-add=NET_ADMIN \`,
		`--device=/dev/net/tun \`,
		`-e XDG_CONFIG_HOME="$container_config_home" \`,
		`-e TS_TUN_MODE=auto \`,
		`-v "$tailscale_state_dir:/var/lib/tailscale" \`,
		`-v "$swarmd_state_dir:/var/lib/swarmd" \`,
		`image_ref='localhost/swarm-container-mvp:remote-abc123'`,
		`WorkingDirectory=/var/lib/swarm/remote-deploy/test`,
		`ExecStart=/bin/bash /var/lib/swarm/remote-deploy/test/run-remote-child.sh`,
		`cat > '/var/lib/swarm/remote-deploy/test/run-remote-child.sh' <<'SCRIPT'`,
		`sudo docker image inspect "$image_ref"`,
		`elif [ -f swarm-container-mvp.tar.gz ]; then`,
		`sudo docker load`,
		`if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then`,
		`-e TS_AUTHKEY="${TAILSCALE_AUTHKEY}" \`,
		`systemctl enable 'swarm-remote-child-test.service'`,
		`systemctl enable --now 'swarm-remote-child-test.service'`,
		`sudo systemctl set-environment "TAILSCALE_AUTHKEY=${TAILSCALE_AUTHKEY:-}" "SWARM_REMOTE_SYNC_VAULT_PASSWORD=${SWARM_REMOTE_SYNC_VAULT_PASSWORD:-}"`,
		`sudo systemctl start 'swarm-remote-child-test.service'`,
		`sudo systemctl unset-environment TAILSCALE_AUTHKEY SWARM_REMOTE_SYNC_VAULT_PASSWORD`,
		`docker logs --tail 200 swarm-remote-child`,
		`docker inspect -f '{{.State.Running}}' swarm-remote-child`,
		`deadline=$((SECONDS + 30))`,
		`'localhost/swarm-container-mvp:remote-abc123'`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("installer script missing %q\n%s", needle, script)
		}
	}
	if strings.Contains(script, `$HOME/.config`) {
		t.Fatalf("installer script should not depend on host HOME under systemd\n%s", script)
	}
}

func TestRemoteDeployImageRefUsesSignature(t *testing.T) {
	got := remoteDeployImageRef("ABC123")
	want := "localhost/swarm-container-mvp:remote-abc123"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRemotePairingTransportsForModeUsesTailscaleFallbacks(t *testing.T) {
	transports := remotePairingTransportsForMode(startupconfig.NetworkModeTailscale, []swarmruntime.TransportSummary{
		{
			Kind:    startupconfig.NetworkModeLAN,
			Primary: "10.0.0.5",
			All:     []string{"10.0.0.5"},
		},
		{
			Kind:    startupconfig.NetworkModeTailscale,
			Primary: "https://remote-host.tailnet.ts.net",
			All: []string{
				"https://remote-host.tailnet.ts.net",
				"remote-host.tailnet.ts.net",
				"100.64.0.5",
				"100.64.0.5",
			},
		},
	}, "https://fallback.tailnet.ts.net")
	if len(transports) != 1 {
		t.Fatalf("expected one tailscale transport, got %#v", transports)
	}
	got := transports[0]
	if got.Kind != startupconfig.NetworkModeTailscale {
		t.Fatalf("expected tailscale kind, got %#v", got)
	}
	if got.Primary != "https://remote-host.tailnet.ts.net" {
		t.Fatalf("expected primary tailscale url, got %#v", got)
	}
	for _, want := range []string{
		"https://remote-host.tailnet.ts.net",
		"remote-host.tailnet.ts.net",
		"100.64.0.5",
	} {
		if !containsString(got.All, want) {
			t.Fatalf("expected transport values to include %q: %#v", want, got.All)
		}
	}
}

func TestRemotePairingTransportsForModeFallsBackToEndpoint(t *testing.T) {
	transports := remotePairingTransportsForMode(startupconfig.NetworkModeTailscale, nil, "https://remote-host.tailnet.ts.net")
	if len(transports) != 1 {
		t.Fatalf("expected fallback transport, got %#v", transports)
	}
	if transports[0].Primary != "https://remote-host.tailnet.ts.net" {
		t.Fatalf("unexpected fallback transport %#v", transports[0])
	}
	if len(transports[0].All) != 1 || transports[0].All[0] != "https://remote-host.tailnet.ts.net" {
		t.Fatalf("unexpected fallback values %#v", transports[0].All)
	}
}

func TestShouldRequestRemotePairing(t *testing.T) {
	base := pebblestore.RemoteDeploySessionRecord{
		InviteToken:      "invite-token",
		RemoteTailnetURL: "https://child-a.tailnet.ts.net",
	}
	tests := []struct {
		name   string
		record pebblestore.RemoteDeploySessionRecord
		want   bool
	}{
		{
			name:   "first request",
			record: base,
			want:   true,
		},
		{
			name: "same endpoint already requested",
			record: pebblestore.RemoteDeploySessionRecord{
				InviteToken:      "invite-token",
				RemoteTailnetURL: "https://child-a.tailnet.ts.net",
				EnrollmentStatus: "pairing_requested",
				LastPairingURL:   "https://child-a.tailnet.ts.net",
			},
			want: false,
		},
		{
			name: "new tailnet url retries pairing",
			record: pebblestore.RemoteDeploySessionRecord{
				InviteToken:      "invite-token",
				RemoteTailnetURL: "https://child-b.tailnet.ts.net",
				EnrollmentStatus: "pairing_requested",
				LastPairingURL:   "https://child-a.tailnet.ts.net",
			},
			want: true,
		},
		{
			name: "enrollment already exists",
			record: pebblestore.RemoteDeploySessionRecord{
				InviteToken:      "invite-token",
				RemoteTailnetURL: "https://child-b.tailnet.ts.net",
				EnrollmentID:     "enroll-123",
				EnrollmentStatus: "pending",
				LastPairingURL:   "https://child-a.tailnet.ts.net",
			},
			want: false,
		},
		{
			name: "missing tailnet url",
			record: pebblestore.RemoteDeploySessionRecord{
				InviteToken: "invite-token",
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRequestRemotePairing(tc.record); got != tc.want {
				t.Fatalf("shouldRequestRemotePairing(%+v) = %v, want %v", tc.record, got, tc.want)
			}
		})
	}
}

func TestWaitForRemoteSwarmReadyWithClientRetriesUntilReady(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			if attempts.Add(1) < 3 {
				http.Error(w, `{"ok":false}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/healthz":
			http.Error(w, `{"ok":false}`, http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitForRemoteSwarmReadyWithClient(ctx, server.URL, server.Client(), 1500*time.Millisecond, 25*time.Millisecond); err != nil {
		t.Fatalf("waitForRemoteSwarmReadyWithClient() error = %v", err)
	}
	if got := attempts.Load(); got < 3 {
		t.Fatalf("ready probe attempts = %d, want at least 3", got)
	}
}

func TestWaitForRemoteSwarmReadyWithClientTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForRemoteSwarmReadyWithClient(ctx, server.URL, server.Client(), 120*time.Millisecond, 25*time.Millisecond)
	if err == nil {
		t.Fatalf("waitForRemoteSwarmReadyWithClient() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "was not ready within") {
		t.Fatalf("timeout error = %q, want readiness timeout", err.Error())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
