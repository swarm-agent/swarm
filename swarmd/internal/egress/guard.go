package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// The gateway runs on the host, outside the sandbox firewall, so it applies
// the same rule itself: a sandbox may reach the public internet through it,
// never this server, the tailnet, private networks, loopback or metadata.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96", "2001:db8::/32",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// ErrBlockedDestination reports a destination the gateway never connects to.
var ErrBlockedDestination = errors.New("destination is not a public internet address")

func publicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// dialPublic resolves host itself and connects only to a public address,
// dialing the checked address so DNS cannot change between check and use.
func dialPublic(ctx context.Context, network, hostport string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		addrs = resolved
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error = fmt.Errorf("%w: %s", ErrBlockedDestination, host)
	for _, addr := range addrs {
		if !publicAddr(addr) {
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
