package localcontainers_test

import (
	"fmt"
	"net"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func TestResolveHostPortUsesImmediateNextPair(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 3)

	got, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err != nil {
		t.Fatalf("ResolveHostPort returned error: %v", err)
	}
	if want := basePort + 1; got != want {
		t.Fatalf("ResolveHostPort returned %d, want %d", got, want)
	}
}

func TestResolveHostPortSkipsImmediateBackendPortWhenOccupied(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 5)
	backendListener := listenPort(t, basePort+1)
	defer backendListener.Close()

	got, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err != nil {
		t.Fatalf("ResolveHostPort returned error: %v", err)
	}
	if want := basePort + 3; got != want {
		t.Fatalf("ResolveHostPort returned %d, want %d", got, want)
	}
}

func TestResolveHostPortSkipsImmediateDesktopPortWhenOccupied(t *testing.T) {
	basePort := findFreeBasePortSpan(t, 5)
	desktopListener := listenPort(t, basePort+2)
	defer desktopListener.Close()

	got, err := localcontainers.ResolveHostPort(fmt.Sprintf("http://127.0.0.1:%d", basePort), 0)
	if err != nil {
		t.Fatalf("ResolveHostPort returned error: %v", err)
	}
	if want := basePort + 3; got != want {
		t.Fatalf("ResolveHostPort returned %d, want %d", got, want)
	}
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
