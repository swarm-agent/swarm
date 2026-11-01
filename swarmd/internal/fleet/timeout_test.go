package fleet

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	if !ok || transport.DialContext == nil || transport.TLSHandshakeTimeout != TLSHandshakeTimeout || client.headerTimeout != ResponseHeaderTimeout {
		t.Fatalf("link transport is not bounded: %+v", transport)
	}
	if DialTimeout != 5*time.Second || TLSHandshakeTimeout != 5*time.Second || ResponseHeaderTimeout != 30*time.Second {
		t.Fatal("link bounds changed")
	}
	if client.http.Timeout != 0 {
		t.Fatal("a whole-request timeout would cut long streams")
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatal("a transport-wide header timeout would cut calls with a held wait")
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

// Purpose: a linked call in which the machine deliberately holds its answer
// (manage_sessions wait_seconds up to 120, environment waits up to 110, a
// forwarded request) must outlast the 30 s header bound, while an ordinary
// call to the same healthy-but-slow machine stays bounded at 30 s. Threat: a
// transport-wide header timeout failing held waits at 30 s, recording a
// healthy machine as failed, and prompting retries that duplicate a run
// already started there. Boundary: Client.Do with WithHeldWait and
// WithoutHeaderBound. A real pinned-TLS server that answers after more than
// ResponseHeaderTimeout is the narrowest layer that proves the real bound;
// the three calls run concurrently, so the test takes about 31 s.
func TestClientHeldWaitOutlastsHeaderBound(t *testing.T) {
	serverCert, serverFP := nodeCert(t)
	home, _ := nodeCert(t)
	hold := ResponseHeaderTimeout + time.Second
	release := make(chan struct{})
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(hold):
		case <-release:
			return
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(`{"held":true}`))
	}))
	peer.TLS = ServerTLS(serverCert)
	peer.StartTLS()
	defer peer.Close()
	defer close(release)
	address := strings.TrimPrefix(peer.URL, "https://")
	client := NewClient(home, address, serverFP, "link-session")

	type outcome struct {
		err     error
		body    string
		elapsed time.Duration
	}
	var held, unbounded, ordinary outcome
	var wg sync.WaitGroup
	run := func(out *outcome, heldWait, unbound bool) {
		defer wg.Done()
		ctx := t.Context()
		if heldWait {
			ctx = WithHeldWait(ctx, 60*time.Second)
		}
		if unbound {
			ctx = WithoutHeaderBound(ctx)
		}
		start := time.Now()
		resp, err := client.Do(ctx, http.MethodGet, "/v3/held", nil, nil)
		if err == nil {
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			out.body, err = string(data), readErr
		}
		out.err, out.elapsed = err, time.Since(start)
	}
	wg.Add(3)
	go run(&held, true, false)
	go run(&unbounded, false, true)
	go run(&ordinary, false, false)
	wg.Wait()

	for name, got := range map[string]outcome{"held wait": held, "forwarded": unbounded} {
		if got.err != nil || got.body != `{"held":true}` {
			t.Fatalf("%s call to a machine answering after %v failed after %v: %v %q", name, hold, got.elapsed, got.err, got.body)
		}
		if got.elapsed < ResponseHeaderTimeout {
			t.Fatalf("%s call answered in %v, before the machine's hold", name, got.elapsed)
		}
	}
	var timeout *headerTimeoutError
	if !errors.As(ordinary.err, &timeout) {
		t.Fatalf("ordinary call to a machine holding %v: %v (%v)", hold, ordinary.err, ordinary.elapsed)
	}
	if ordinary.elapsed < ResponseHeaderTimeout-time.Second || ordinary.elapsed > ResponseHeaderTimeout+3*time.Second {
		t.Fatalf("ordinary call bounded at %v, want about %v", ordinary.elapsed, ResponseHeaderTimeout)
	}
}
