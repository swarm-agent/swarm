package localcontainers_test

import (
	"fmt"
	"net"
	"strings"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func TestResolveHostPortUsesImmediateNextPair(t *testing.T) {
	basePort := findFreeBasePort(t)

	got, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err != nil {
		t.Fatalf("ResolveHostPort returned error: %v", err)
	}
	if want := basePort + 1; got != want {
		t.Fatalf("ResolveHostPort returned %d, want %d", got, want)
	}
}

func TestResolveHostPortFailsWhenImmediateBackendPortIsOccupied(t *testing.T) {
	basePort := findFreeBasePort(t)
	backendListener := listenPort(t, basePort+1)
	defer backendListener.Close()

	_, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err == nil {
		t.Fatal("ResolveHostPort returned nil error, want backend-port collision")
	}
	if want := fmt.Sprintf("host port %d is not available", basePort+1); !strings.Contains(err.Error(), want) {
		t.Fatalf("ResolveHostPort error = %q, want substring %q", err.Error(), want)
	}
}

func TestResolveHostPortFailsWhenImmediateDesktopPortIsOccupied(t *testing.T) {
	basePort := findFreeBasePort(t)
	desktopListener := listenPort(t, basePort+2)
	defer desktopListener.Close()

	_, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err == nil {
		t.Fatal("ResolveHostPort returned nil error, want desktop-port collision")
	}
	if want := fmt.Sprintf("desktop host port %d is not available", basePort+2); !strings.Contains(err.Error(), want) {
		t.Fatalf("ResolveHostPort error = %q, want substring %q", err.Error(), want)
	}
}

func findFreeBasePort(t *testing.T) int {
	t.Helper()
	for port := 20000; port <= 65000; port++ {
		if !portAvailable(port) || !portAvailable(port+1) || !portAvailable(port+2) {
			continue
		}
		return port
	}
	t.Fatal("failed to find three consecutive free loopback ports")
	return 0
}

func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func listenPort(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen on %d: %v", port, err)
	}
	return ln
}
