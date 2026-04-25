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

func TestValidateTarGzArchiveAcceptsValidArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.tar.gz")
	writeTestTarGz(t, path, map[string]string{
		"manifest.json": "{}",
		"layer.tar":     "hello world",
	})
	if err := validateTarGzArchive(path); err != nil {
		t.Fatalf("validateTarGzArchive(valid) error = %v", err)
	}
}

func TestValidateTarGzArchiveRejectsTruncatedArchive(t *testing.T) {
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
	if err := validateTarGzArchive(path); err == nil {
		t.Fatalf("validateTarGzArchive(truncated) expected error")
	}
}

func TestRemoteRuntimeSignatureChangesWithInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmd"), "swarmd-a")
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmctl"), "swarmctl-a")
	writeTestFile(t, filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"), "fff")
	writeTestFile(t, filepath.Join(root, ".tools", "go", "VERSION"), "go1.25.0\n")
	writeTestFile(t, filepath.Join(root, ".tools", "go", "go.env"), "GOTOOLCHAIN=local\n")
	writeTestFile(t, filepath.Join(root, "web", "dist", "index.html"), "<html>a</html>")

	first, err := remoteRuntimeSignature(root)
	if err != nil {
		t.Fatalf("first signature: %v", err)
	}
	second, err := remoteRuntimeSignature(root)
	if err != nil {
		t.Fatalf("second signature: %v", err)
	}
	if first != second {
		t.Fatalf("expected stable signature, got %q vs %q", first, second)
	}

	writeTestFile(t, filepath.Join(root, "web", "dist", "index.html"), "<html>b</html>")
	changed, err := remoteRuntimeSignature(root)
	if err != nil {
		t.Fatalf("changed signature: %v", err)
	}
	if changed == first {
		t.Fatalf("expected signature to change after input mutation")
	}
}

func TestRemoteInstallerScriptLaunchesRemoteContainerWithoutPersistence(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "remote-child-test",
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarm/remote-deploy/test",
		RemoteRuntime: "docker",
		ImageRef:      "localhost/swarm-remote-child:test1234",
		SudoMode:      "sudo",
		Payloads: []pebblestore.RemoteDeployPayloadRecord{{
			ArchiveName: "payload-01.tar.gz",
			TargetPath:  "/workspaces",
		}},
	}
	script := remoteInstallerScript(record)
	for _, needle := range []string{
		`remote_root='/var/lib/swarm/remote-deploy/test'`,
		`config_home='/var/lib/swarm/remote-deploy/test/config'`,
		`legacy_credentials_file='/var/lib/swarm/remote-deploy/test/remote-child.credentials.env'`,
		`bootstrap_secret_file='/var/lib/swarm/remote-deploy/test/config/swarm/remote-deploy-bootstrap.secret'`,
		`tailscale_state_dir='/var/lib/swarm/remote-deploy/test/state/tailscale'`,
		`swarmd_state_dir='/var/lib/swarm/remote-deploy/test/state/swarmd'`,
		`runtime='docker'`,
		`image_ref='localhost/swarm-remote-child:test1234'`,
		`image_archive='swarm-remote-tailscale-image.tar'`,
		`use_archive_image='1'`,
		`container_name='swarm-remote-child-remote-child-test'`,
		`if runtime_cmd image inspect "$image_ref" >/dev/null 2>&1; then`,
		`runtime_cmd load -i "$image_archive" >/dev/null`,
		`as_root mkdir -p '/workspaces'`,
		`rm -f "$legacy_credentials_file"`,
		`cat > "$start_script" <<'SCRIPT'`,
		`export XDG_CONFIG_HOME="$config_home"`,
		`export TS_SOCKET="$tailscale_state_dir/tailscaled.sock"`,
		`export TS_OUTBOUND_HTTP_PROXY_LISTEN="127.0.0.1:1055"`,
		`export SWARM_TAILSCALE_OUTBOUND_PROXY="http://127.0.0.1:1055"`,
		`run_args+=(--volume '/workspaces:/workspaces')`,
		`run_args+=(-e TS_AUTHKEY)`,
		`run_args+=(-e SWARM_REMOTE_SYNC_VAULT_PASSWORD)`,
		`exec "$runtime_bin" "${run_args[@]}"`,
		`nohup sudo -E /bin/bash "$start_script" >"$log_file" 2>&1 < /dev/null &`,
		`log_timer_step "start_remote_container" "$step_started_ms"`,
		`runtime_cmd inspect -f '{{.State.Running}}' "$container_name"`,
		`tail -n 200 "$log_file"`,
		`deadline=$((SECONDS + 90))`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("installer script missing %q\n%s", needle, script)
		}
	}
	if strings.Contains(script, `$HOME/.config`) {
		t.Fatalf("installer script should not depend on host HOME\n%s", script)
	}
	for _, unexpected := range []string{
		`systemctl`,
		`journalctl`,
		`WorkingDirectory=`,
		`ExecStart=`,
		`TAILSCALE_AUTHKEY=`,
		`SWARM_REMOTE_SYNC_VAULT_PASSWORD=`,
		`remote_deploy_session_token =`,
		`remote_deploy_invite_token =`,
		`--env-file`,
	} {
		if strings.Contains(script, unexpected) {
			t.Fatalf("installer script should not include %q\n%s", unexpected, script)
		}
	}
}

func TestRemoteBundleStartScriptDeliversSecretsViaSSHStdinWithoutCredentialsFile(t *testing.T) {
	record := &pebblestore.RemoteDeploySessionRecord{
		ID:           "remote-child-test",
		RemoteRoot:   "/var/lib/swarm/remote-deploy/test",
		SessionToken: "session-secret",
		InviteToken:  "invite-secret",
	}
	childCfgText := "remote_deploy_enabled = true\nremote_deploy_session_id = remote-child-test\nremote_deploy_host_api_base_url = https://host.example\n"
	script, err := remoteBundleStartScript(record, childCfgText, "ts-auth-key", "vault-pass")
	if err != nil {
		t.Fatalf("remoteBundleStartScript error = %v", err)
	}
	for _, needle := range []string{
		`umask 077`,
		`remote_dir='/var/lib/swarm/remote-deploy/test'`,
		`config_path='/var/lib/swarm/remote-deploy/test/config/swarm/swarm.conf'`,
		`bootstrap_secret_path='/var/lib/swarm/remote-deploy/test/config/swarm/remote-deploy-bootstrap.secret'`,
		`installer_path='/var/lib/swarm/remote-deploy/test/install-remote-child.sh'`,
		`legacy_credentials_file='/var/lib/swarm/remote-deploy/test/remote-child.credentials.env'`,
		`trap 'rm -f "$installer_path" "$legacy_credentials_file"' EXIT`,
		`cat > "$config_path" <<'SWARM_REMOTE_CONFIG_EOF'`,
		childCfgText,
		`chmod 0600 "$config_path"`,
		`cat > "$bootstrap_secret_path" <<'SWARM_REMOTE_SECRET_EOF'`,
		"remote_deploy_session_token = session-secret",
		"remote_deploy_invite_token = invite-secret",
		`chmod 0600 "$bootstrap_secret_path"`,
		`cat > "$installer_path" <<'SWARM_REMOTE_INSTALL_EOF'`,
		`chmod 0700 "$installer_path"`,
		`TS_AUTHKEY='ts-auth-key' SWARM_REMOTE_SYNC_VAULT_PASSWORD='vault-pass' "$installer_path"`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("start script missing %q\n%s", needle, script)
		}
	}
	for _, unexpected := range []string{
		`TAILSCALE_AUTHKEY=`,
		`cat > "$credentials_file"`,
		`--env-file`,
		`chmod 0600 "$credentials_file"`,
	} {
		if strings.Contains(script, unexpected) {
			t.Fatalf("start script should not include %q\n%s", unexpected, script)
		}
	}
}

func TestRemoteInstallerScriptPullsRegistryImageWithoutArchive(t *testing.T) {
	t.Setenv(remoteImagePrefixEnv, "ghcr.io/swarm-agent/swarm-remote-child")

	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "remote-child-test",
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarm/remote-deploy/test",
		RemoteRuntime: "docker",
		ImageRef:      "ghcr.io/swarm-agent/swarm-remote-child:test1234",
		SudoMode:      "sudo",
	}
	script := remoteInstallerScript(record)
	for _, needle := range []string{
		`image_ref='ghcr.io/swarm-agent/swarm-remote-child:test1234'`,
		`image_archive='swarm-remote-tailscale-image.tar'`,
		`use_archive_image='0'`,
		`elif [ "$use_archive_image" != "1" ]; then`,
		`runtime_cmd pull "$image_ref" >/dev/null`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("installer script missing %q\n%s", needle, script)
		}
	}
	if !strings.Contains(script, `elif [ -f "$image_archive" ]; then`) {
		t.Fatalf("installer script should keep archive fallback branch for archive-based refs\n%s", script)
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

func TestEnsurePendingInviteRestoresMissingHostInvite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "swarmd.pebble")
	store, err := pebblestore.Open(dbPath)
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()

	swarmStore := pebblestore.NewSwarmStore(store)
	swarms := swarmruntime.NewService(swarmStore, nil, nil)

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	if err := startupconfig.Write(startupconfig.FileConfig{
		Path:              startupPath,
		Mode:              "box",
		Host:              "127.0.0.1",
		Port:              17792,
		AdvertisePort:     17792,
		DesktopPort:       15566,
		PeerTransportPort: 17802,
		SwarmName:         "Remote Deploy Test Host",
		SwarmMode:         true,
		NetworkMode:       startupconfig.NetworkModeTailscale,
		TailscaleURL:      "https://host.tailnet.ts.net",
	}); err != nil {
		t.Fatalf("write startup config: %v", err)
	}

	service := NewService(pebblestore.NewRemoteDeploySessionStore(store), swarms, swarmStore, nil, nil, nil, startupPath, t.TempDir())
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                 "remote-test-1",
		Name:               "remote-test-1",
		InviteToken:        "invite-token",
		MasterTailscaleURL: "https://host.tailnet.ts.net",
	}

	changed, err := service.ensurePendingInvite(&record)
	if err != nil {
		t.Fatalf("ensurePendingInvite(first): %v", err)
	}
	if !changed {
		t.Fatalf("ensurePendingInvite(first) expected record changes")
	}
	firstInvite, ok, err := swarmStore.FindInviteByToken("invite-token")
	if err != nil {
		t.Fatalf("FindInviteByToken(first): %v", err)
	}
	if !ok {
		t.Fatalf("expected invite to exist after ensurePendingInvite")
	}
	if strings.TrimSpace(record.GroupID) == "" {
		t.Fatalf("expected ensurePendingInvite to populate group id")
	}
	if err := store.Delete(pebblestore.KeySwarmInvite(firstInvite.ID)); err != nil {
		t.Fatalf("delete invite record: %v", err)
	}
	if err := store.Delete(pebblestore.KeySwarmInviteToken("invite-token")); err != nil {
		t.Fatalf("delete invite token index: %v", err)
	}

	changed, err = service.ensurePendingInvite(&record)
	if err != nil {
		t.Fatalf("ensurePendingInvite(restored): %v", err)
	}
	if changed {
		t.Fatalf("ensurePendingInvite(restored) should not mutate the session record once host metadata is set")
	}
	restoredInvite, ok, err := swarmStore.FindInviteByToken("invite-token")
	if err != nil {
		t.Fatalf("FindInviteByToken(restored): %v", err)
	}
	if !ok {
		t.Fatalf("expected invite to be restored after deletion")
	}
	if strings.TrimSpace(restoredInvite.Token) != "invite-token" {
		t.Fatalf("restored invite token = %q, want %q", restoredInvite.Token, "invite-token")
	}
	if strings.TrimSpace(restoredInvite.GroupID) != strings.TrimSpace(record.GroupID) {
		t.Fatalf("restored invite group = %q, want %q", restoredInvite.GroupID, record.GroupID)
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

func TestPrepareRemoteBundleExcludesGeneratedConfigSecretsAndScripts(t *testing.T) {
	workDir := t.TempDir()
	service := &Service{}
	record := &pebblestore.RemoteDeploySessionRecord{ID: "remote-test-1", SessionToken: "secret", InviteToken: "invite", RemoteRoot: "/var/lib/swarm/remote-deploy/test"}

	if err := service.prepareRemoteBundle(context.Background(), workDir, record); err != nil {
		t.Fatalf("prepareRemoteBundle() error = %v", err)
	}

	remoteBundleDir := filepath.Join(workDir, "bundle", "remote")
	unexpectedPaths := []string{
		filepath.Join(remoteBundleDir, "config", "swarm", "swarm.conf"),
		filepath.Join(remoteBundleDir, "config", "swarm", "remote-deploy-bootstrap.secret"),
		filepath.Join(remoteBundleDir, "install-remote-child.sh"),
		filepath.Join(remoteBundleDir, "remote-child.credentials.env"),
	}
	for _, path := range unexpectedPaths {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("prepareRemoteBundle wrote secret/script path %q", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
	}
}
