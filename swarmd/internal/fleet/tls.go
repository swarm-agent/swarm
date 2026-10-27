// Package fleet links one Swarm (a home) to other Swarms it controls.
//
// A link goes one way. A Swarm invites a home with a one-time code; the home
// redeems it over TLS where each side checks the other's key, and gets an
// owner session on that Swarm, which it sends with every call from then on.
// The invited Swarm gets nothing that reaches the home.
package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Certificate makes the TLS certificate of a Swarm's own key (its node key,
// base64 as the swarm store keeps it). Peers pin the key, not the
// certificate, so a new certificate at every start is fine.
func Certificate(publicKey, privateKey string) (tls.Certificate, error) {
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(public) != ed25519.PublicKeySize {
		return tls.Certificate{}, errors.New("the node public key is not an ed25519 key")
	}
	private, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return tls.Certificate{}, errors.New("the node private key is not an ed25519 key")
	}
	key := ed25519.PrivateKey(private)
	if !key.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(public)) {
		return tls.Certificate{}, errors.New("the node keys do not match")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "swarm " + Fingerprint(public)[:16]},
		NotBefore:    now.Add(-time.Hour),
		// Nothing checks the dates: trust is the pinned key.
		NotAfter:    now.AddDate(100, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, ed25519.PublicKey(public), key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("make the node certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Fingerprint is a key's sha256 in hex, as the swarm store computes it for
// the node key.
func Fingerprint(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])
}

// PeerFingerprint is the fingerprint of the key in a peer's certificate, or
// "" when it is not an ed25519 key.
func PeerFingerprint(raw [][]byte) string {
	if len(raw) == 0 {
		return ""
	}
	cert, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return ""
	}
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return Fingerprint(key)
}

// ServerTLS is the TLS of a Swarm's link listener: it asks every caller for
// a certificate and leaves deciding who it is to the handler (a code's
// redeemer is not known yet).
func ServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		// HTTP/1.1 only: the realtime WebSocket upgrades an HTTP/1.1 connection.
		NextProtos: []string{"http/1.1"},
	}
}

// ClientTLS is the TLS a home uses to call a Swarm: its own certificate,
// and the Swarm's key must be the one pinned when the link was made.
func ClientTLS(cert tls.Certificate, pinned string) *tls.Config {
	pinned = strings.ToLower(strings.TrimSpace(pinned))
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
		// The chain is self-signed by design; VerifyPeerCertificate checks the
		// pinned key instead.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if got := PeerFingerprint(raw); got == "" || got != pinned {
				return fmt.Errorf("the machine's key %s is not the linked key %s", short(got), short(pinned))
			}
			return nil
		},
	}
}

func short(fingerprint string) string {
	if len(fingerprint) > 16 {
		return fingerprint[:16]
	}
	if fingerprint == "" {
		return "(none)"
	}
	return fingerprint
}
