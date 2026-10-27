package fleet

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Purpose: Client.Upgrade must pin the peer, replace app credentials, and never
// follow redirects with the link session. A real TLS HTTP peer is the narrowest
// layer that observes headers and proves a wrong pin sends no HTTP request.
func TestUpgradeCredentialsAndPin(t *testing.T) {
	cert, fp := nodeCert(t)
	home, _ := nodeCert(t)
	_, wrong := nodeCert(t)
	var calls atomic.Int32
	observed := make(chan http.Header, 1)
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		observed <- r.Header.Clone()
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusFound)
	}))
	peer.TLS = ServerTLS(cert)
	peer.StartTLS()
	defer peer.Close()
	address := strings.TrimPrefix(peer.URL, "https://")
	headers := http.Header{TokenHeader: {"app-token"}, "Authorization": {"Bearer app"}, "Cookie": {"app=secret"}, "Proxy-Authorization": {"proxy-secret"}}
	_, conn, err := NewClient(home, address, fp, "link-session").Upgrade(t.Context(), "/stream", headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect: %v", err)
	}
	got := <-observed
	if got.Get(TokenHeader) != "link-session" || got.Get("Authorization") != "" || got.Get("Cookie") != "" || got.Get("Proxy-Authorization") != "" {
		t.Fatal("credentials not isolated")
	}
	if got.Get("Upgrade") != "websocket" || got.Get("Connection") != "Upgrade" {
		t.Fatal("upgrade stripped")
	}
	_, conn, err = NewClient(home, address, wrong, "link-session").Upgrade(t.Context(), "/stream", headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "not the linked key") {
		t.Fatalf("wrong pin: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("redirect followed or wrong peer received credentials")
	}
}
