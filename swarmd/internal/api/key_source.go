package api

import (
	"net"
	"net/http"
	"strings"
)

// keySource names where a request came from, for a key's "last used from" and
// the key.new_address signal: "this machine" for the local socket or a local
// process, "tailnet LOGIN" for a person's device through Tailscale Serve,
// otherwise the caller's network (an IPv4 /24 or IPv6 /48, so one network's
// changing addresses count as one place). Forwarding headers are believed
// only from loopback, where Serve and local relays sit.
func keySource(r *http.Request) string {
	if r == nil {
		return ""
	}
	if isLocalTransportRequest(r) {
		return "this machine"
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() {
		if login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); login != "" {
			return "tailnet " + login
		}
		forwarded := net.ParseIP(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]))
		if forwarded == nil || forwarded.IsLoopback() {
			return "this machine"
		}
		ip = forwarded
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}
