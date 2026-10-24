package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// codePrefix marks a link code and its format version.
const codePrefix = "swarm-link-1."

// DefaultPort is where a Swarm listens for its homes when the person names
// only an address.
const DefaultPort = "7792"

// Invite is what a link code carries: where the invited Swarm listens, its
// key, and the one-time secret that proves the home got the code.
type Invite struct {
	Address     string `json:"a"`
	Fingerprint string `json:"f"`
	ID          string `json:"i"`
	Secret      string `json:"s"`
	Name        string `json:"n,omitempty"`
}

var (
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	namePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// NewSecret makes an invite's id and secret.
func NewSecret() (id, secret string, err error) {
	buf := make([]byte, 40)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(buf[:8]), base64.RawURLEncoding.EncodeToString(buf[8:]), nil
}

// HashSecret is how the invited Swarm keeps a secret: never the secret itself.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretMatches compares a presented secret with a kept hash in constant time.
func SecretMatches(secret, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(hash)) == 1
}

// Encode writes the link code.
func (i Invite) Encode() (string, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	return codePrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

// Decode reads a link code.
func Decode(code string) (Invite, error) {
	code = strings.Join(strings.Fields(code), "")
	if !strings.HasPrefix(code, codePrefix) {
		return Invite{}, errors.New("not a Swarm link code (it starts with " + codePrefix + ")")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, codePrefix))
	if err != nil {
		return Invite{}, errors.New("the link code is damaged")
	}
	var invite Invite
	if err := json.Unmarshal(data, &invite); err != nil {
		return Invite{}, errors.New("the link code is damaged")
	}
	return invite, invite.validate()
}

func (i Invite) validate() error {
	if _, _, err := net.SplitHostPort(i.Address); err != nil {
		return errors.New("the link code has no address")
	}
	if !fingerprintPattern.MatchString(i.Fingerprint) {
		return errors.New("the link code has no key")
	}
	if i.ID == "" || len(i.Secret) < 32 {
		return errors.New("the link code has no secret")
	}
	if i.Name != "" && !namePattern.MatchString(i.Name) {
		return errors.New("the link code's name is not valid")
	}
	return nil
}

// ValidName reports whether name can name a machine: lowercase letters,
// digits and dashes.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// NameFrom turns a host name into a machine name ("" when nothing is left).
func NameFrom(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if !ValidName(name) {
		return ""
	}
	return name
}

// ListenAddress checks where a Swarm may listen for its homes and adds the
// default port when only an address is given. It must be one address of
// this machine on a private network (a LAN, a VPN such as WireGuard or
// Tailscale, or loopback), never every address and never a public one.
// Port 0 picks a free port (tests).
func ListenAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if ip := net.ParseIP(strings.Trim(addr, "[]")); ip != nil {
		addr = net.JoinHostPort(ip.String(), DefaultPort)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "", fmt.Errorf("listen address %q: give this machine's IP on the network the home reaches it on, with or without :PORT", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("listen address %q: give an IP, not a name", addr)
	}
	if ip.IsUnspecified() {
		return "", fmt.Errorf("listen address %q: name one address, not every address", addr)
	}
	if !(ip.IsPrivate() || ip.IsLoopback() || cgnat.Contains(ip)) {
		return "", fmt.Errorf("listen address %q is not on a private network (LAN, WireGuard, Tailscale)", addr)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// cgnat is the shared address space Tailscale and some VPNs use.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
