package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nodeCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := Certificate(base64.StdEncoding.EncodeToString(public), base64.StdEncoding.EncodeToString(private))
	if err != nil {
		t.Fatal(err)
	}
	return cert, Fingerprint(public)
}

func TestCodeRoundTripAndRefusals(t *testing.T) {
	id, secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	invite := Invite{Address: "192.168.77.20:7792", Fingerprint: strings.Repeat("ab", 32), ID: id, Secret: secret, Name: "box-b"}
	code, err := invite.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// A code pasted with line breaks still reads.
	got, err := Decode(code[:20] + "\n  " + code[20:])
	if err != nil || got != invite {
		t.Fatalf("decode: %+v %v", got, err)
	}
	if !SecretMatches(secret, HashSecret(secret)) || SecretMatches(secret+"x", HashSecret(secret)) {
		t.Fatal("secret match")
	}
	for _, bad := range []string{"", "swarm-pair-1.abc", codePrefix + "!!", codePrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"a":"x"}`))} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("%q decoded", bad)
		}
	}
}

func TestListenAddress(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.77.20":      "192.168.77.20:7792",
		"192.168.77.20:9000": "192.168.77.20:9000",
		"100.101.102.103":    "100.101.102.103:7792",
		"127.0.0.1:0":        "127.0.0.1:0",
	} {
		if got, err := ListenAddress(in); err != nil || got != want {
			t.Fatalf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:7792", "8.8.8.8", "box:7792", ""} {
		if _, err := ListenAddress(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// The home reaches only the Swarm whose key it pinned, and the Swarm sees
// the home's key.
func TestPinnedKeysBothWays(t *testing.T) {
	serverCert, serverFP := nodeCert(t)
	homeCert, homeFP := nodeCert(t)
	_, otherFP := nodeCert(t)
	seen := ""
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := [][]byte{}
		for _, c := range r.TLS.PeerCertificates {
			raw = append(raw, c.Raw)
		}
		seen = PeerFingerprint(raw)
		w.Write([]byte(`{"ok":true}`))
	}))
	srv.TLS = ServerTLS(serverCert)
	srv.StartTLS()
	defer srv.Close()
	address := strings.TrimPrefix(srv.URL, "https://")

	var out struct{ OK bool }
	if err := NewClient(homeCert, address, serverFP, "").JSON(t.Context(), http.MethodGet, "/", nil, &out); err != nil || !out.OK {
		t.Fatalf("pinned call: %v", err)
	}
	if seen != homeFP {
		t.Fatalf("server saw %q, want the home's key", seen)
	}
	if err := NewClient(homeCert, address, otherFP, "").JSON(t.Context(), http.MethodGet, "/", nil, nil); err == nil || !strings.Contains(err.Error(), "not the linked key") {
		t.Fatalf("another key accepted: %v", err)
	}
}
