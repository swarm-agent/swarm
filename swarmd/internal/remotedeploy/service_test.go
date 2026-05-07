package remotedeploy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/buildinfo"
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

func TestRemoteRequiredDiskBytesIncludesPayloadStagingAndFallback(t *testing.T) {
	payloads := []pebblestore.RemoteDeployPayloadRecord{
		{IncludedBytes: 100},
		{IncludedBytes: 50},
	}
	if got, want := remoteRequiredDiskBytes(1000, payloads), int64(1300); got != want {
		t.Fatalf("remoteRequiredDiskBytes = %d, want %d", got, want)
	}
	if got := remoteRequiredDiskBytes(0, nil); got <= 0 {
		t.Fatalf("remoteRequiredDiskBytes zero-payload fallback = %d, want positive", got)
	}
}

func TestRemotePayloadLinkedTargetWorkspacePathUsesExistingPayloadTarget(t *testing.T) {
	payloads := []pebblestore.RemoteDeployPayloadRecord{
		{
			SourcePath:    "/src/parent",
			WorkspacePath: "/src/parent",
			TargetPath:    "/workspaces/parent",
			Directories: []pebblestore.RemoteDeployPayloadDirectoryRecord{{
				SourcePath:    "/src/child",
				WorkspacePath: "/src/child",
				TargetPath:    "/workspaces/child",
			}},
		},
		{
			SourcePath:    "/src/child",
			WorkspacePath: "/src/child",
			TargetPath:    "/workspaces/child",
		},
	}

	got, ok := remotePayloadLinkedTargetWorkspacePath(payloads, payloads[1])
	if !ok {
		t.Fatal("expected linked target to be found")
	}
	if got != "/workspaces/child" {
		t.Fatalf("linked target = %q, want /workspaces/child", got)
	}
}

func TestResolveRemoteImageDeliveryModeUsesArchiveInDevMode(t *testing.T) {
	if got := resolveRemoteImageDeliveryMode(remoteImageDeliveryRegistry, startupconfig.FileConfig{DevMode: true}); got != remoteImageDeliveryArchive {
		t.Fatalf("resolveRemoteImageDeliveryMode(dev registry) = %q, want %q", got, remoteImageDeliveryArchive)
	}
	if got := resolveRemoteImageDeliveryMode(remoteImageDeliveryRegistry, startupconfig.FileConfig{}); got != remoteImageDeliveryRegistry {
		t.Fatalf("resolveRemoteImageDeliveryMode(release registry) = %q, want %q", got, remoteImageDeliveryRegistry)
	}
}

func TestRemotePreflightRequiredDiskBytesArchiveAllowsDevVersion(t *testing.T) {
	oldVersion := buildinfo.Version
	buildinfo.Version = "dev"
	t.Cleanup(func() { buildinfo.Version = oldVersion })

	required, err := remotePreflightRequiredDiskBytes(context.Background(), remoteImageDeliveryArchive, nil)
	if err != nil {
		t.Fatalf("remotePreflightRequiredDiskBytes(archive dev) error = %v", err)
	}
	if required <= 0 {
		t.Fatalf("remotePreflightRequiredDiskBytes(archive dev) = %d, want positive fallback", required)
	}
}

func TestRemotePreflightSummaryReportsZeroPayloadAndDisk(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		SSHSessionTarget:  "remote.example",
		TransportMode:     startupconfig.NetworkModeTailscale,
		ImageDeliveryMode: remoteImageDeliveryRegistry,
		ImagePrefix:       "ghcr.io/swarm-agent/swarm",
		RemoteDisk: pebblestore.RemoteDeployDiskRecord{
			AvailableBytes: 2 * 1024 * 1024 * 1024,
			RequiredBytes:  512 * 1024 * 1024,
		},
	}
	summary := remotePreflightSummary(record)
	for _, needle := range []string{"no workspace payloads", "Remote disk check", "2.0 GiB available", "512.0 MiB required"} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("summary missing %q: %s", needle, summary)
		}
	}
}

func TestFormatCreatePreflightErrorReportsSelectedRemoteRuntime(t *testing.T) {
	err := formatCreatePreflightError("remote.example", errors.New("remote runtime missing:podman"))
	if err == nil {
		t.Fatalf("formatCreatePreflightError() error = nil")
	}
	message := err.Error()
	for _, needle := range []string{"Podman was selected", "Podman is not installed", "apt install -y podman"} {
		if !strings.Contains(message, needle) {
			t.Fatalf("formatted error missing %q:\n%s", needle, message)
		}
	}
	if strings.Contains(message, "Docker was selected") {
		t.Fatalf("formatted error should not mention Docker selection for podman:\n%s", message)
	}
}

func TestRemoteRootUsesCanonicalSystemDataPath(t *testing.T) {
	if got, want := remoteRoot("remote child/test"), "/var/lib/swarmd/remote-deploy/remote-child-test"; got != want {
		t.Fatalf("remoteRoot() = %q, want %q", got, want)
	}
}

func TestPrepareRemoteWritableDirUsesSudoAwareProvisioning(t *testing.T) {
	oldRunner := remoteSSHCommandRunner
	var gotTarget string
	var gotScript string
	remoteSSHCommandRunner = func(ctx context.Context, target, script string) (string, error) {
		gotTarget = target
		gotScript = script
		return "", nil
	}
	t.Cleanup(func() { remoteSSHCommandRunner = oldRunner })

	if err := prepareRemoteWritableDir(context.Background(), "remote.example", "/var/lib/swarmd/remote-deploy/test", "sudo"); err != nil {
		t.Fatalf("prepareRemoteWritableDir() error = %v", err)
	}
	if gotTarget != "remote.example" {
		t.Fatalf("ssh target = %q, want remote.example", gotTarget)
	}
	for _, needle := range []string{
		`remote_dir='/var/lib/swarmd/remote-deploy/test'`,
		`use_sudo='1'`,
		`if [ "$use_sudo" != "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then`,
		`sudo -n "$@"`,
		`as_root mkdir -p "$remote_dir"`,
		`as_root chown "$remote_uid:$remote_gid" "$remote_dir"`,
		`remote directory is not writable: $remote_dir`,
	} {
		if !strings.Contains(gotScript, needle) {
			t.Fatalf("prepare script missing %q\n%s", needle, gotScript)
		}
	}
}

func TestRemoteRuntimePreflightScriptChecksSelectedRuntimeWithSudo(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "remote-child-test",
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarmd/remote-deploy/test",
		RemoteRuntime: "podman",
		ImageRef:      "localhost/swarm-remote-child:test1234",
		SudoMode:      "sudo",
	}
	script := remoteInstallerScript(record)
	if !strings.Contains(script, `runtime='podman'`) {
		t.Fatalf("installer script missing selected runtime:\n%s", script)
	}
	if !strings.Contains(script, `as_root podman "$@"`) {
		t.Fatalf("installer script does not invoke selected runtime through sudo helper:\n%s", script)
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

func TestVerifyRemoteDeployBackendBinariesRequiresExecutableStagedBinaries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmd"), "swarmd")
	writeTestFile(t, filepath.Join(root, ".bin", "main", "swarmctl"), "swarmctl")

	if err := verifyRemoteDeployBackendBinaries(root); err != nil {
		t.Fatalf("verifyRemoteDeployBackendBinaries() error = %v", err)
	}
	if err := os.Chmod(filepath.Join(root, ".bin", "main", "swarmctl"), 0o644); err != nil {
		t.Fatalf("chmod staged swarmctl: %v", err)
	}
	err := verifyRemoteDeployBackendBinaries(root)
	if err == nil {
		t.Fatal("verifyRemoteDeployBackendBinaries() error = nil, want missing executable error")
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("verifyRemoteDeployBackendBinaries() error = %q, want executable detail", err.Error())
	}
}

func TestCreateRejectsLANWireGuardRemoteDeploy(t *testing.T) {
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
	service := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), swarms, swarmStore, nil, nil, nil, startupPath, t.TempDir())
	state, err := swarms.EnsureLocalState(swarmruntime.EnsureLocalStateInput{Name: "Remote Deploy Test Host", Role: "master", SwarmMode: true})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	if _, err := swarmStore.PutGroup(pebblestore.SwarmGroupRecord{ID: "group-1", Name: "Group 1", HostSwarmID: state.Node.SwarmID}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := swarmStore.PutGroupMembership(pebblestore.SwarmGroupMembershipRecord{GroupID: "group-1", SwarmID: state.Node.SwarmID, Name: "Remote Deploy Test Host", SwarmRole: "master", MembershipRole: swarmruntime.GroupMembershipRoleHost}); err != nil {
		t.Fatalf("put group membership: %v", err)
	}

	_, err = service.Create(context.Background(), CreateSessionInput{
		Name:             "lan-child",
		SSHSessionTarget: "remote-host",
		TransportMode:    startupconfig.NetworkModeLAN,
		GroupID:          "group-1",
		RemoteRuntime:    "docker",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want LAN/WireGuard disabled error")
	}
	if !strings.Contains(err.Error(), "LAN/WireGuard remote deploy is disabled") {
		t.Fatalf("Create() error = %q, want disabled message", err.Error())
	}
}

func TestResolveMasterRemoteDeployEndpointUsesDevLanePort(t *testing.T) {
	t.Setenv("SWARM_LANE", "dev")
	t.Setenv("SWARM_LANE_PORT", "7782")
	endpoint, err := resolveMasterRemoteDeployEndpoint(startupconfig.FileConfig{
		Host:          "10.77.1.2",
		Port:          7781,
		AdvertiseHost: "10.77.1.2",
		AdvertisePort: 7781,
	}, startupconfig.NetworkModeLAN)
	if err != nil {
		t.Fatalf("resolveMasterRemoteDeployEndpoint() error = %v", err)
	}
	if endpoint != "http://10.77.1.2:7782" {
		t.Fatalf("endpoint = %q, want dev lane port", endpoint)
	}
}

func TestRenderChildStartupConfigIgnoresRemoteAdvertiseHostForTailscale(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                  "laptop-c5d07eb0",
		Name:                "laptop",
		TransportMode:       startupconfig.NetworkModeTailscale,
		MasterEndpoint:      "https://host.tailnet.ts.net",
		RemoteAdvertiseHost: "10.0.0.1",
	}
	cfg := (&Service{}).renderChildStartupConfig(record, startupconfig.FileConfig{}, swarmruntime.LocalState{})
	for _, needle := range []string{
		"host = 127.0.0.1",
		"advertise_host = 127.0.0.1",
		"mode = tailscale",
		"tailscale_url = https://host.tailnet.ts.net",
		"remote_deploy_enabled = true",
		"remote_deploy_host_api_base_url = https://host.tailnet.ts.net",
	} {
		if !strings.Contains(cfg, needle) {
			t.Fatalf("child startup config missing %q\n%s", needle, cfg)
		}
	}
	if strings.Contains(cfg, "host = 10.0.0.1") || strings.Contains(cfg, "advertise_host = 10.0.0.1") {
		t.Fatalf("tailscale child startup config should not use remote LAN host\n%s", cfg)
	}
}

func TestRemoteDevReplacementScriptDoesNotDuplicateRemoteRootMount(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:                  "remote-replace-test",
		Name:                "remote-replace-test",
		TransportMode:       startupconfig.NetworkModeLAN,
		RemoteRuntime:       "docker",
		RemoteRoot:          "/var/lib/swarmd/remote-deploy/remote-replace-test",
		RemoteAdvertiseHost: "10.0.0.10",
		Payloads: []pebblestore.RemoteDeployPayloadRecord{{
			TargetPath: "/workspaces",
		}},
	}
	script := remoteDevReplacementScript(record, remoteRuntimeArtifact{ImageRef: "localhost/swarm-remote-child:new"})
	if got := strings.Count(script, `"$remote_root:$remote_root"`); got != 1 {
		t.Fatalf("remote root mount occurrences = %d, want one\n%s", got, script)
	}
	if strings.Contains(script, `run_args+=(--volume '/var/lib/swarmd/remote-deploy/remote-replace-test:/var/lib/swarmd/remote-deploy/remote-replace-test')`) {
		t.Fatalf("replacement script duplicates concrete remote root mount\n%s", script)
	}
	if !strings.Contains(script, `run_args+=(--volume '/workspaces:/workspaces')`) {
		t.Fatalf("replacement script should still mount payload target\n%s", script)
	}
}

func TestRemoteInstallerScriptSanitizesTailscaleHostname(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "pc-child-test",
		Name:          "pc child",
		RemoteRoot:    "/var/lib/swarmd/remote-deploy/test",
		RemoteRuntime: "docker",
		ImageRef:      "localhost/swarm-remote-child:test1234",
	}
	script := remoteInstallerScript(record)
	for _, needle := range []string{
		`ts_hostname='pc-child'`,
		`export TS_HOSTNAME="$ts_hostname"`,
		`-e "TS_HOSTNAME=$ts_hostname"`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("installer script missing sanitized hostname %q\n%s", needle, script)
		}
	}
	for _, unexpected := range []string{
		`ts_hostname='pc child'`,
		`TS_HOSTNAME=pc child`,
	} {
		if strings.Contains(script, unexpected) {
			t.Fatalf("installer script leaked unsanitized Tailscale hostname %q\n%s", unexpected, script)
		}
	}
}

func TestRemoteInstallerScriptLaunchesRemoteContainerWithoutPersistence(t *testing.T) {
	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "remote-child-test",
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarmd/remote-deploy/test",
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
		`remote_root='/var/lib/swarmd/remote-deploy/test'`,
		`config_home='/etc/swarmd/remote-deploy/test'`,
		`cache_dir='/var/cache/swarmd/remote-deploy/test'`,
		`runtime_dir='/run/swarmd/remote-deploy/test'`,
		`legacy_credentials_file='/var/lib/swarmd/remote-deploy/test/remote-child.credentials.env'`,
		`bootstrap_secret_file='/etc/swarmd/remote-deploy/test/remote-deploy-bootstrap.secret'`,
		`tailscale_state_dir='/var/lib/swarmd/remote-deploy/test/data/tailscale'`,
		`swarmd_state_dir='/var/lib/swarmd/remote-deploy/test/data/swarmd'`,
		`runtime='docker'`,
		`image_ref='localhost/swarm-remote-child:test1234'`,
		`image_archive='swarm-remote-tailscale-image.tar'`,
		`use_archive_image='1'`,
		`container_name='swarm-remote-child-remote-child-test'`,
		`if runtime_cmd image inspect "$image_ref" >/dev/null 2>&1; then`,
		`runtime_cmd load -i "$image_archive" >/dev/null`,
		`as_root mkdir -p '/workspaces'`,
		`as_root mkdir -p "$remote_root" "$config_home" "$cache_dir" "$runtime_dir" "$tailscale_state_dir" "$swarmd_state_dir" "$log_dir"`,
		`if [ "$use_sudo" != "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then`,
		`container_runtime_uid='65534'`,
		`container_runtime_gid='65534'`,
		`repair_workspace_mount_permissions()`,
		`as_root chown -R "${container_runtime_uid}:${container_runtime_gid}" "$target"`,
		`repair_workspace_mount_permissions '/workspaces'`,
		`as_root chown -R 65534:65534 "$config_home" "$cache_dir" "$runtime_dir" "$swarmd_state_dir"`,
		`as_root chmod 0700 "$config_home" "$cache_dir" "$runtime_dir" "$swarmd_state_dir"`,
		`as_root chmod 0600 "$config_home/swarm.conf"`,
		`rm -f "$legacy_credentials_file"`,
		`cat > "$start_script" <<'SCRIPT'`,
		`-e "SWARMD_CONFIG_DIR=$config_home"`,
		`-e "SWARMD_CACHE_DIR=$cache_dir"`,
		`-e "SWARMD_RUNTIME_DIR=$runtime_dir"`,
		`-e "SWARMD_LOG_DIR=$log_dir"`,
		`-e "HOME=/nonexistent"`,
		`export TS_SOCKET="$tailscale_state_dir/tailscaled.sock"`,
		`tailscale_proxy_addr=`,
		`desktop_port=`,
		`peer_transport_port=`,
		`check_remote_ports`,
		`printf 'REMOTE_PORT_CONFLICT label=%s port=%s\n' "$label" "$port"`,
		`export TS_OUTBOUND_HTTP_PROXY_LISTEN="$tailscale_proxy_addr"`,
		`export SWARM_TAILSCALE_OUTBOUND_PROXY="http://$tailscale_proxy_addr"`,
		`export SWARM_DESKTOP_PORT="$desktop_port"`,
		`export SWARM_STARTUP_MODE=box`,
		`-e "SWARM_STARTUP_MODE=box"`,
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
	for _, forbidden := range []string{`$HOME/.config`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `remote_root/xdg`} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("installer script should not depend on forbidden home/XDG path %q\n%s", forbidden, script)
		}
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
		RemoteRoot:   "/var/lib/swarmd/remote-deploy/test",
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
		`remote_dir='/var/lib/swarmd/remote-deploy/test'`,
		`config_path='/etc/swarmd/remote-deploy/test/swarm.conf'`,
		`bootstrap_secret_path='/etc/swarmd/remote-deploy/test/remote-deploy-bootstrap.secret'`,
		`installer_path='/var/lib/swarmd/remote-deploy/test/install-remote-child.sh'`,
		`legacy_credentials_file='/var/lib/swarmd/remote-deploy/test/remote-child.credentials.env'`,
		`trap 'rm -f "$installer_path" "$legacy_credentials_file" "${config_tmp:-}" "${bootstrap_secret_tmp:-}"' EXIT`,
		`if [ "$use_sudo" != "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then use_sudo=1; fi`,
		`if ! mkdir -p "$remote_dir" 2>/dev/null; then as_root mkdir -p "$remote_dir"; fi`,
		`as_root mkdir -p "$(dirname "$config_path")" "$(dirname "$bootstrap_secret_path")"`,
		`cat > "$config_tmp" <<'SWARM_REMOTE_CONFIG_EOF'`,
		childCfgText,
		`as_root install -m 0600 "$config_tmp" "$config_path"`,
		`cat > "$bootstrap_secret_tmp" <<'SWARM_REMOTE_SECRET_EOF'`,
		"remote_deploy_session_token = session-secret",
		"remote_deploy_invite_token = invite-secret",
		`as_root install -m 0600 "$bootstrap_secret_tmp" "$bootstrap_secret_path"`,
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
		`remote_deploy_session_token =` + "\n",
		`remote_deploy_invite_token =` + "\n",
		`--env-file`,
		`chmod 0600 "$credentials_file"`,
	} {
		if strings.Contains(script, unexpected) {
			t.Fatalf("start script should not include %q\n%s", unexpected, script)
		}
	}
}

func TestResolveRemoteImagePrefixRegistryUsesOfficialProductionImage(t *testing.T) {
	t.Setenv(remoteImagePrefixEnv, "")
	prefix, err := resolveRemoteImagePrefix(remoteImageDeliveryRegistry)
	if err != nil {
		t.Fatalf("resolveRemoteImagePrefix error = %v", err)
	}
	if prefix != "ghcr.io/swarm-agent/swarm" {
		t.Fatalf("expected official image prefix, got %q", prefix)
	}

	t.Setenv(remoteImagePrefixEnv, "ghcr.io/swarm-agent/swarm-remote-child")
	if _, err := resolveRemoteImagePrefix(remoteImageDeliveryRegistry); err == nil {
		t.Fatalf("expected custom remote image prefix to be rejected")
	}
}

func TestRemoteInstallerScriptPullsAndVerifiesProductionRegistryDigestWithoutArchive(t *testing.T) {
	oldVersion := buildinfo.Version
	oldCommit := buildinfo.Commit
	buildinfo.Version = "v9.8.7"
	buildinfo.Commit = "0123456789abcdef"
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Commit = oldCommit
	})

	record := pebblestore.RemoteDeploySessionRecord{
		ID:            "remote-child-test",
		Name:          "remote-child",
		RemoteRoot:    "/var/lib/swarmd/remote-deploy/test",
		RemoteRuntime: "docker",
		ImageRef:      "ghcr.io/swarm-agent/swarm@sha256:abc123",
		SudoMode:      "sudo",
	}
	script := remoteInstallerScript(record)
	for _, needle := range []string{
		`image_ref='ghcr.io/swarm-agent/swarm@sha256:abc123'`,
		`image_archive='swarm-remote-tailscale-image.tar'`,
		`use_archive_image='0'`,
		`elif [ "$use_archive_image" != "1" ]; then`,
		`runtime_cmd pull "$image_ref" >/dev/null`,
		`expected_label='https://github.com/swarm-agent/swarm'`,
		`expected_label='v9.8.7'`,
		`expected_label='swarm.container.v1'`,
		`expected_label='app'`,
		`expected_label='0123456789abcdef'`,
		`actual_label=$(runtime_cmd image inspect "$image_ref" --format '{{ index .Config.Labels "org.opencontainers.image.source" }}' 2>/dev/null || true)`,
		`actual_label=$(runtime_cmd image inspect "$image_ref" --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' 2>/dev/null || true)`,
		`actual_label=$(runtime_cmd image inspect "$image_ref" --format '{{ index .Config.Labels "swarmagent.image.contract" }}' 2>/dev/null || true)`,
		`actual_label=$(runtime_cmd image inspect "$image_ref" --format '{{ index .Config.Labels "swarmagent.image.role" }}' 2>/dev/null || true)`,
		`actual_label=$(runtime_cmd image inspect "$image_ref" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' 2>/dev/null || true)`,
		`remote image label verification failed: %s=%s, expected %s`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("installer script missing %q\n%s", needle, script)
		}
	}
	if !strings.Contains(script, "\nlog_timer_step \"ensure_remote_image\" \"$step_started_ms\"") {
		t.Fatalf("installer script should separate image verification from timer step\n%s", script)
	}
	if strings.Contains(script, "configlog_timer_step") {
		t.Fatalf("installer script joined config path and timer command\n%s", script)
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

	service := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), swarms, swarmStore, nil, nil, nil, startupPath, t.TempDir())
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

func TestReconcileAlwaysOnRemoteSessionsRestartsStoppedRemoteContainer(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarmd.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()
	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), nil, nil, nil, nil, nil, "", "")
	record, err := svc.store.Put(pebblestore.RemoteDeploySessionRecord{
		ID:               "remote-always-on-1",
		Name:             "remote-always-on-1",
		Status:           "attached",
		AlwaysOn:         true,
		SSHSessionTarget: "test-ssh",
		RemoteRoot:       "/var/lib/swarmd/remote-deploy/remote-always-on-1",
		RemoteRuntime:    "docker",
		SSHReachable:     true,
	})
	if err != nil {
		t.Fatalf("put remote deploy session: %v", err)
	}
	var commands []string
	oldRunner := remoteSSHCommandRunner
	remoteSSHCommandRunner = func(_ context.Context, target, script string) (string, error) {
		if target != "test-ssh" {
			t.Fatalf("ssh target = %q, want test-ssh", target)
		}
		commands = append(commands, script)
		switch {
		case strings.Contains(script, "REMOTE_SSH_OK=1"):
			return "REMOTE_SSH_OK=1\n", nil
		case strings.Contains(script, "REMOTE_ACTIVE="):
			return "REMOTE_ACTIVE=0\n", nil
		case strings.Contains(script, "REMOTE_ALWAYS_ON_RESTARTED=1"):
			if !strings.Contains(script, "/var/lib/swarmd/remote-deploy/remote-always-on-1/run-remote-child.sh") {
				t.Fatalf("restart script did not use existing run script: %s", script)
			}
			if !strings.Contains(script, "/run/swarmd/remote-deploy/remote-always-on-1/run-remote-child.pid") {
				t.Fatalf("restart script did not use runtime PID path: %s", script)
			}
			if strings.Contains(script, "/var/lib/swarmd/remote-deploy/remote-always-on-1/run-remote-child.pid") {
				t.Fatalf("restart script used data root for PID file: %s", script)
			}
			return "REMOTE_ALWAYS_ON_RESTARTED=1\n", nil
		default:
			t.Fatalf("unexpected ssh script: %s", script)
			return "", nil
		}
	}
	defer func() { remoteSSHCommandRunner = oldRunner }()

	if err := svc.ReconcileAlwaysOnRemoteSessions(context.Background()); err != nil {
		t.Fatalf("ReconcileAlwaysOnRemoteSessions() error = %v", err)
	}
	if len(commands) != 3 {
		t.Fatalf("ssh command count = %d, want 3", len(commands))
	}
	updated, ok, err := svc.store.Get(record.ID)
	if err != nil || !ok {
		t.Fatalf("get updated remote deploy session ok=%t err=%v", ok, err)
	}
	if updated.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.LastError)
	}
	if updated.LastProgress != "Remote Always On restarted the remote child container." {
		t.Fatalf("LastProgress = %q", updated.LastProgress)
	}
}

func TestReconcileAlwaysOnRemoteSessionsReportsUnreachableSSHHost(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarmd.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}
	defer store.Close()
	svc := NewService(pebblestore.NewRemoteDeploySessionStore(store), pebblestore.NewSwarmNodeStore(store), nil, nil, nil, nil, nil, "", "")
	record, err := svc.store.Put(pebblestore.RemoteDeploySessionRecord{
		ID:               "remote-always-on-2",
		Name:             "remote-always-on-2",
		Status:           "attached",
		AlwaysOn:         true,
		SSHSessionTarget: "offline-ssh",
		RemoteRoot:       "/tmp/swarm-remote-always-on",
		RemoteRuntime:    "docker",
		SSHReachable:     true,
	})
	if err != nil {
		t.Fatalf("put remote deploy session: %v", err)
	}
	oldRunner := remoteSSHCommandRunner
	remoteSSHCommandRunner = func(_ context.Context, target, script string) (string, error) {
		if target != "offline-ssh" {
			t.Fatalf("ssh target = %q, want offline-ssh", target)
		}
		if !strings.Contains(script, "REMOTE_SSH_OK=1") {
			t.Fatalf("unexpected script after failed probe: %s", script)
		}
		return "", errors.New("connect: no route to host")
	}
	defer func() { remoteSSHCommandRunner = oldRunner }()

	err = svc.ReconcileAlwaysOnRemoteSessions(context.Background())
	if err == nil {
		t.Fatalf("ReconcileAlwaysOnRemoteSessions() error = nil, want unreachable error")
	}
	if !strings.Contains(err.Error(), "remote SSH host offline-ssh is unreachable") {
		t.Fatalf("error = %q, want explicit SSH unreachable message", err.Error())
	}
	updated, ok, err := svc.store.Get(record.ID)
	if err != nil || !ok {
		t.Fatalf("get updated remote deploy session ok=%t err=%v", ok, err)
	}
	if updated.SSHReachable {
		t.Fatalf("SSHReachable = true, want false")
	}
	if !strings.Contains(updated.LastError, "remote SSH host offline-ssh is unreachable") {
		t.Fatalf("LastError = %q, want unreachable message", updated.LastError)
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
	sourceDir := t.TempDir()
	writeTestFile(t, filepath.Join(sourceDir, "tracked.txt"), "tracked payload")
	gitCommand(t, sourceDir, "init")
	gitCommand(t, sourceDir, "add", "tracked.txt")
	gitCommand(t, sourceDir, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "add tracked payload")

	service := &Service{}
	record := &pebblestore.RemoteDeploySessionRecord{
		ID:           "remote-test-1",
		SessionToken: "secret",
		InviteToken:  "invite",
		RemoteRoot:   "/var/lib/swarmd/remote-deploy/test",
		Payloads: []pebblestore.RemoteDeployPayloadRecord{{
			ArchiveName: "payload-01.tar.gz",
			SourcePath:  sourceDir,
		}},
	}

	if err := service.prepareRemoteBundle(context.Background(), workDir, record); err != nil {
		t.Fatalf("prepareRemoteBundle() error = %v", err)
	}

	remoteBundleDir := filepath.Join(workDir, "bundle", "remote")
	archivePath := filepath.Join(remoteBundleDir, "payload-01.tar.gz")
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("expected payload archive to be staged: %v", err)
	}
	assertTarGzContainsOnly(t, archivePath, "tracked.txt")

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

func gitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
}

func assertTarGzContainsOnly(t *testing.T, archivePath string, want ...string) {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var got []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if header == nil {
			continue
		}
		got = append(got, header.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archive entries = %#v, want %#v", got, want)
	}
}
