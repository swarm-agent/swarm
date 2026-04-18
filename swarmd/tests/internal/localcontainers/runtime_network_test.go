package localcontainers_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func TestPrepareRuntimeNetworkCreatesPodmanNetworkWithDNSDisabled(t *testing.T) {
	stateDir, logPath := installFakePodmanNetworkRuntime(t)

	network, err := localcontainers.PrepareRuntimeNetwork(context.Background(), "podman", "swarm-net")
	if err != nil {
		t.Fatalf("PrepareRuntimeNetwork returned error: %v", err)
	}
	if got, want := network.Name, "swarm-net"; got != want {
		t.Fatalf("network name = %q, want %q", got, want)
	}
	if got, want := network.Gateway, "10.89.0.1"; got != want {
		t.Fatalf("network gateway = %q, want %q", got, want)
	}

	state := readTrimmedFile(t, filepath.Join(stateDir, "swarm-net.state"))
	if got, want := state, "dns_disabled"; got != want {
		t.Fatalf("network state = %q, want %q", got, want)
	}

	logOutput := readTrimmedFile(t, logPath)
	if !strings.Contains(logOutput, "network create --disable-dns swarm-net") {
		t.Fatalf("command log did not record disabled-dns create: %q", logOutput)
	}
}

func TestPrepareRuntimeNetworkRecreatesStalePodmanDNSNetworkWhenUnused(t *testing.T) {
	stateDir, logPath := installFakePodmanNetworkRuntime(t)
	if err := os.WriteFile(filepath.Join(stateDir, "swarm-net.state"), []byte("dns_enabled\n"), 0o644); err != nil {
		t.Fatalf("seed stale network state: %v", err)
	}

	network, err := localcontainers.PrepareRuntimeNetwork(context.Background(), "podman", "swarm-net")
	if err != nil {
		t.Fatalf("PrepareRuntimeNetwork returned error: %v", err)
	}
	if got, want := network.Gateway, "10.89.0.1"; got != want {
		t.Fatalf("network gateway = %q, want %q", got, want)
	}

	state := readTrimmedFile(t, filepath.Join(stateDir, "swarm-net.state"))
	if got, want := state, "dns_disabled"; got != want {
		t.Fatalf("network state = %q, want %q", got, want)
	}

	logOutput := readTrimmedFile(t, logPath)
	if !strings.Contains(logOutput, "network rm swarm-net") {
		t.Fatalf("command log did not record network removal: %q", logOutput)
	}
	if !strings.Contains(logOutput, "network create --disable-dns swarm-net") {
		t.Fatalf("command log did not record disabled-dns recreate: %q", logOutput)
	}
}

func TestPrepareRuntimeNetworkFailsForStalePodmanDNSNetworkInUse(t *testing.T) {
	stateDir, _ := installFakePodmanNetworkRuntime(t)
	if err := os.WriteFile(filepath.Join(stateDir, "swarm-net.state"), []byte("dns_enabled\n"), 0o644); err != nil {
		t.Fatalf("seed stale network state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "swarm-net.containers"), []byte("child-one\n"), 0o644); err != nil {
		t.Fatalf("seed attached container list: %v", err)
	}

	_, err := localcontainers.PrepareRuntimeNetwork(context.Background(), "podman", "swarm-net")
	if err == nil {
		t.Fatal("PrepareRuntimeNetwork returned nil error for stale in-use network")
	}
	if want := `still uses embedded DNS`; !strings.Contains(err.Error(), want) {
		t.Fatalf("PrepareRuntimeNetwork error = %q, want substring %q", err.Error(), want)
	}
}

func installFakePodmanNetworkRuntime(t *testing.T) (string, string) {
	t.Helper()

	binDir := t.TempDir()
	stateDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "fake-podman.log")
	scriptPath := filepath.Join(binDir, "podman")
	if err := os.WriteFile(scriptPath, []byte(fakePodmanNetworkScript(stateDir, logPath)), 0o755); err != nil {
		t.Fatalf("write fake podman script: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return stateDir, logPath
}

func fakePodmanNetworkScript(stateDir, logPath string) string {
	return "#!/usr/bin/env bash\n" +
		"set -eu\n" +
		"STATE_DIR=" + shellSingleQuotedNet(stateDir) + "\n" +
		"LOG_PATH=" + shellSingleQuotedNet(logPath) + "\n" +
		"mkdir -p \"$STATE_DIR\"\n" +
		"printf '%s\\n' \"$*\" >> \"$LOG_PATH\"\n" +
		"if [[ \"${1:-}\" == \"network\" && \"${2:-}\" == \"inspect\" ]]; then\n" +
		"  name=\"${3:-}\"\n" +
		"  state_file=\"$STATE_DIR/$name.state\"\n" +
		"  if [[ ! -f \"$state_file\" ]]; then\n" +
		"    echo 'Error: network not found' >&2\n" +
		"    exit 125\n" +
		"  fi\n" +
		"  state=\"$(tr -d '\\n' < \"$state_file\")\"\n" +
		"  dns_enabled=false\n" +
		"  if [[ \"$state\" == \"dns_enabled\" ]]; then dns_enabled=true; fi\n" +
		"  printf '[{\"name\":\"%s\",\"subnets\":[{\"gateway\":\"10.89.0.1\"}],\"dns_enabled\":%s}]\\n' \"$name\" \"$dns_enabled\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"${1:-}\" == \"network\" && \"${2:-}\" == \"create\" ]]; then\n" +
		"  shift 2\n" +
		"  state='dns_enabled'\n" +
		"  if [[ \"${1:-}\" == \"--disable-dns\" ]]; then\n" +
		"    state='dns_disabled'\n" +
		"    shift\n" +
		"  fi\n" +
		"  name=\"${1:-}\"\n" +
		"  printf '%s\\n' \"$state\" > \"$STATE_DIR/$name.state\"\n" +
		"  printf '%s\\n' \"$name\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"${1:-}\" == \"network\" && \"${2:-}\" == \"rm\" ]]; then\n" +
		"  name=\"${3:-}\"\n" +
		"  rm -f \"$STATE_DIR/$name.state\" \"$STATE_DIR/$name.containers\"\n" +
		"  printf '%s\\n' \"$name\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"${1:-}\" == \"ps\" ]]; then\n" +
		"  filter=''\n" +
		"  while [[ $# -gt 0 ]]; do\n" +
		"    case \"$1\" in\n" +
		"      --filter)\n" +
		"        filter=\"${2:-}\"\n" +
		"        shift 2\n" +
		"        ;;\n" +
		"      *)\n" +
		"        shift\n" +
		"        ;;\n" +
		"    esac\n" +
		"  done\n" +
		"  name=\"${filter#network=}\"\n" +
		"  containers_file=\"$STATE_DIR/$name.containers\"\n" +
		"  if [[ -f \"$containers_file\" ]]; then\n" +
		"    cat \"$containers_file\"\n" +
		"  fi\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'unsupported fake podman command' >&2\n" +
		"exit 64\n"
}

func readTrimmedFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(content))
}

func shellSingleQuotedNet(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
