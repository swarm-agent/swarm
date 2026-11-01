package fleet

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// Purpose: a linked machine that has gone away (its address accepts or drops
// the connection but never answers) must fail a home's call within the link's
// own bounds, not after the kernel's SYN retries or never. Threat: a bare
// transport that holds the proxy, unlink and the fleet list for minutes.
// Boundary: fleet.NewClient's transport. A real TCP listener that never
// speaks TLS is the narrowest hermetic stand-in for a blackholed peer; a
// dropped SYN cannot be reproduced without network privileges, so the dial
// bound is asserted on the transport's configuration.
func TestClientGoneMachineFailsFast(t *testing.T) {
	home, _ := nodeCert(t)
	_, fp := nodeCert(t)
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var held []net.Conn // accepted, never answered
		for {
			conn, err := silent.Accept()
			if err != nil {
				for _, c := range held {
					c.Close()
				}
				return
			}
			held = append(held, conn)
		}
	}()
	defer func() {
		silent.Close()
		<-done
	}()

	client := NewClient(home, silent.Addr().String(), fp, "link-session")
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil || transport.TLSHandshakeTimeout != TLSHandshakeTimeout || transport.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Fatalf("link transport is not bounded: %+v", transport)
	}
	if DialTimeout != 5*time.Second || TLSHandshakeTimeout != 5*time.Second || ResponseHeaderTimeout != 30*time.Second {
		t.Fatal("link bounds changed")
	}
	if client.http.Timeout != 0 {
		t.Fatal("a whole-request timeout would cut long streams")
	}

	start := time.Now()
	resp, err := client.Do(t.Context(), http.MethodGet, "/v3/fleet", nil, nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a silent machine answered")
	}
	if elapsed := time.Since(start); elapsed > TLSHandshakeTimeout+3*time.Second {
		t.Fatalf("silent machine held the call %v: %v", elapsed, err)
	}
}
