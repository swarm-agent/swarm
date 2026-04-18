package localcontainers_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceResolveCreateHostPortSkipsRunningManagedPairs(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 7)
	firstBackend := listenPort(t, basePort+1)
	defer firstBackend.Close()
	firstDesktop := listenPort(t, basePort+2)
	defer firstDesktop.Close()
	secondBackend := listenPort(t, basePort+3)
	defer secondBackend.Close()
	secondDesktop := listenPort(t, basePort+4)
	defer secondDesktop.Close()

	svc, localStore, closeStore := newTestLocalContainerService(t, map[string]string{
		"managed-one": "running",
		"managed-two": "running",
	})
	defer closeStore()

	putLocalContainerRecord(t, localStore, pebblestore.SwarmLocalContainerRecord{
		ID:            "managed-one",
		Name:          "Managed One",
		Runtime:       "podman",
		ContainerName: "managed-one",
		HostPort:      basePort + 1,
	})
	putLocalContainerRecord(t, localStore, pebblestore.SwarmLocalContainerRecord{
		ID:            "managed-two",
		Name:          "Managed Two",
		Runtime:       "podman",
		ContainerName: "managed-two",
		HostPort:      basePort + 3,
	})

	got, err := svc.ResolveCreateHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err != nil {
		t.Fatalf("ResolveCreateHostPort returned error: %v", err)
	}
	if want := basePort + 5; got != want {
		t.Fatalf("ResolveCreateHostPort returned %d, want %d", got, want)
	}
}

func TestServiceResolveCreateHostPortFailsWhenImmediatePairBelongsToMissingRecord(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 5)
	backendListener := listenPort(t, basePort+1)
	defer backendListener.Close()
	desktopListener := listenPort(t, basePort+2)
	defer desktopListener.Close()

	svc, localStore, closeStore := newTestLocalContainerService(t, nil)
	defer closeStore()

	putLocalContainerRecord(t, localStore, pebblestore.SwarmLocalContainerRecord{
		ID:            "missing-one",
		Name:          "Missing One",
		Runtime:       "podman",
		ContainerName: "missing-one",
		HostPort:      basePort + 1,
	})

	_, err := svc.ResolveCreateHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err == nil {
		t.Fatal("ResolveCreateHostPort returned nil error, want backend-port collision")
	}
	if want := fmt.Sprintf("host port %d is not available", basePort+1); !strings.Contains(err.Error(), want) {
		t.Fatalf("ResolveCreateHostPort error = %q, want substring %q", err.Error(), want)
	}
}

func TestServiceResolveCreateHostPortFailsWhenNextPairIsBlockedExternally(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 7)
	firstBackend := listenPort(t, basePort+1)
	defer firstBackend.Close()
	firstDesktop := listenPort(t, basePort+2)
	defer firstDesktop.Close()
	nextBackend := listenPort(t, basePort+3)
	defer nextBackend.Close()

	svc, localStore, closeStore := newTestLocalContainerService(t, map[string]string{
		"managed-one": "running",
	})
	defer closeStore()

	putLocalContainerRecord(t, localStore, pebblestore.SwarmLocalContainerRecord{
		ID:            "managed-one",
		Name:          "Managed One",
		Runtime:       "podman",
		ContainerName: "managed-one",
		HostPort:      basePort + 1,
	})

	_, err := svc.ResolveCreateHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err == nil {
		t.Fatal("ResolveCreateHostPort returned nil error, want next-pair collision")
	}
	if want := fmt.Sprintf("host port %d is not available", basePort+3); !strings.Contains(err.Error(), want) {
		t.Fatalf("ResolveCreateHostPort error = %q, want substring %q", err.Error(), want)
	}
}

func newTestLocalContainerService(t *testing.T, statuses map[string]string) (*localcontainers.Service, *pebblestore.SwarmLocalContainerStore, func()) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "localcontainers.pebble"))
	if err != nil {
		t.Fatalf("open pebble store: %v", err)
	}

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "podman")
	if err := os.WriteFile(scriptPath, []byte(fakeInspectScript(statuses)), 0o755); err != nil {
		t.Fatalf("write fake podman script: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	localStore := pebblestore.NewSwarmLocalContainerStore(store)
	svc := localcontainers.NewService(localStore, nil, nil, nil, nil, "")
	return svc, localStore, func() {
		_ = store.Close()
	}
}

func fakeInspectScript(statuses map[string]string) string {
	lines := []string{
		"#!/usr/bin/env bash",
		"set -eu",
		`if [[ "${1:-}" == "inspect" && "${2:-}" == "--format" ]]; then`,
		`  name="${4:-}"`,
		`  case "$name" in`,
	}
	for name, status := range statuses {
		lines = append(lines, fmt.Sprintf("  %s) printf '%s|cid-%s\\n'; exit 0 ;;", shellCasePattern(name), shellSingleQuoted(status), shellSingleQuoted(name)))
	}
	lines = append(lines,
		`  *) echo "no such container" >&2; exit 125 ;;`,
		`  esac`,
		`fi`,
		`echo "unsupported fake podman command" >&2`,
		`exit 64`,
	)
	return strings.Join(lines, "\n") + "\n"
}

func shellCasePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)
	return replacer.Replace(value)
}

func shellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, `'`, `'\''`)
}

func putLocalContainerRecord(t *testing.T, store *pebblestore.SwarmLocalContainerStore, record pebblestore.SwarmLocalContainerRecord) {
	t.Helper()
	if _, err := store.Put(record); err != nil {
		t.Fatalf("put local container record: %v", err)
	}
}

func findFreeBasePortSpan(t *testing.T, width int) int {
	t.Helper()
	for port := 20000; port+width <= 65000; port++ {
		allFree := true
		for offset := 0; offset < width; offset++ {
			if !portAvailable(port + offset) {
				allFree = false
				break
			}
		}
		if allFree {
			return port
		}
	}
	t.Fatalf("failed to find %d consecutive free loopback ports", width)
	return 0
}
