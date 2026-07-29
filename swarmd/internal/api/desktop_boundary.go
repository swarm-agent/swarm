package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"swarm/packages/swarmd/internal/tailscale"
)

const (
	tailscaleUserLoginHeader      = "Tailscale-User-Login"
	tailscaleUserNameHeader       = "Tailscale-User-Name"
	tailscaleUserProfilePicHeader = "Tailscale-User-Profile-Pic"
)

type desktopBoundaryContextKey string

const (
	desktopAdmittedOriginKey desktopBoundaryContextKey = "desktop-admitted-origin"
	desktopPendingOriginKey  desktopBoundaryContextKey = "desktop-pending-origin"
)

var errTailscaleDesktopOriginNotApproved = errors.New("tailscale desktop origin is not approved")

type desktopAdmission struct {
	origin         string
	tailscaleServe bool
}

func (s *Server) withDesktopBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admission, err := s.admitDesktopRequest(r)
		if err != nil {
			if errors.Is(err, errTailscaleDesktopOriginNotApproved) && shouldAllowPendingTailscaleDesktopRequest(r) {
				ctx := context.WithValue(r.Context(), desktopPendingOriginKey, admission)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			writeError(w, http.StatusForbidden, err)
			return
		}
		ctx := context.WithValue(r.Context(), desktopAdmittedOriginKey, admission)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) admitDesktopRequest(r *http.Request) (desktopAdmission, error) {
	if r == nil {
		return desktopAdmission{}, errors.New("desktop request is required")
	}
	rawAuthority := requestHost(r)
	if !forwardedHostMatchesRequestAuthority(r, rawAuthority) {
		return desktopAdmission{}, errors.New("forwarded host does not match the desktop request authority")
	}
	host, err := normalizeRequestAuthority(rawAuthority)
	if err != nil {
		return desktopAdmission{}, err
	}
	if isCanonicalLocalDesktopHost(host) {
		if !isLocalDesktopBrowserRequest(r) {
			return desktopAdmission{}, errors.New("local desktop authority requires a same-machine source")
		}
		origin := localDesktopRequestOrigin(r, host)
		if !browserHeadersMatchAdmittedOrigin(r, origin) {
			return desktopAdmission{}, errors.New("browser origin or referer does not match the admitted desktop origin")
		}
		return desktopAdmission{origin: origin}, nil
	}

	if authorityHasExplicitPort(rawAuthority) {
		return desktopAdmission{}, errors.New("tailscale desktop authority must not include an explicit port")
	}
	origin, err := normalizeExternalDesktopOrigin(host)
	if err != nil {
		return desktopAdmission{}, err
	}
	if s == nil || s.tailscaleServePolicy == nil || s.tailscaleServeDetector == nil {
		return desktopAdmission{}, errors.New("tailscale desktop policy or verifier is unavailable")
	}
	record, ok, err := s.tailscaleServePolicy.Get()
	if err != nil {
		return desktopAdmission{}, errors.New("tailscale desktop policy is unreadable")
	}
	approved := ok && containsExactString(record.Origins, origin)
	snapshot, err := s.tailscaleServeDetector.Snapshot(r.Context(), tailscale.UseCache)
	if err != nil {
		return desktopAdmission{}, errors.New("tailscale desktop route verification is unavailable")
	}
	route, found := snapshot.RouteForOrigin(origin)
	if !found || route.Classification != tailscale.ClassificationVerifiedSwarmDesktop {
		return desktopAdmission{}, errors.New("tailscale desktop origin is not a currently verified Swarm route")
	}
	if !trustedTailscaleServeProvenance(r) {
		return desktopAdmission{}, errors.New("trusted Tailscale Serve identity provenance is required")
	}
	if !browserHeadersMatchAdmittedOrigin(r, origin) {
		return desktopAdmission{}, errors.New("browser origin or referer does not match the admitted desktop origin")
	}
	admission := desktopAdmission{origin: origin, tailscaleServe: true}
	if !approved {
		return admission, errTailscaleDesktopOriginNotApproved
	}
	return admission, nil
}

func shouldAllowPendingTailscaleDesktopRequest(r *http.Request) bool {
	if shouldServeDesktopAsset(r) {
		return true
	}
	if r == nil || r.URL.Path != TailscaleOnboardingApprovalPath {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodPost
}

func normalizeRequestAuthority(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "@/?#") {
		return "", errors.New("desktop request authority is invalid")
	}
	host := raw
	if parsedHost, _, err := net.SplitHostPort(raw); err == nil {
		host = parsedHost
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	} else if strings.Count(raw, ":") == 1 {
		return "", errors.New("desktop request authority contains an invalid port")
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), "."))
	if host == "" || strings.ContainsAny(host, " \\") {
		return "", errors.New("desktop request authority is invalid")
	}
	return host, nil
}

func authorityHasExplicitPort(raw string) bool {
	_, _, err := net.SplitHostPort(strings.TrimSpace(raw))
	return err == nil
}

func forwardedHostMatchesRequestAuthority(r *http.Request, requestAuthority string) bool {
	values := r.Header.Values("X-Forwarded-Host")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	forwardedAuthority := strings.TrimSpace(values[0])
	requestAuthority = strings.TrimSpace(requestAuthority)
	if forwardedAuthority == "" || requestAuthority == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(forwardedAuthority, "."), strings.TrimSuffix(requestAuthority, "."))
}

func isCanonicalLocalDesktopHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, candidate := range detectLANAddresses() {
		if candidateIP := net.ParseIP(strings.TrimSpace(candidate)); candidateIP != nil && candidateIP.Equal(ip) {
			return true
		}
	}
	return false
}

func localDesktopRequestOrigin(r *http.Request, normalizedHost string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	authority := strings.TrimSpace(requestHost(r))
	if _, _, err := net.SplitHostPort(authority); err != nil {
		if ip := net.ParseIP(normalizedHost); ip != nil && strings.Contains(normalizedHost, ":") {
			authority = "[" + normalizedHost + "]"
		} else {
			authority = normalizedHost
		}
	}
	return scheme + "://" + strings.ToLower(authority)
}

func normalizeExternalDesktopOrigin(host string) (string, error) {
	return tailscale.NormalizeHTTPSOrigin("https://" + host)
}

func trustedTailscaleServeProvenance(r *http.Request) bool {
	ip := remoteRequestIP(r)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	if !exactSingleHeaderValue(r.Header, "X-Forwarded-Proto", "https") {
		return false
	}
	return nonEmptySingleHeader(r.Header, tailscaleUserLoginHeader) &&
		nonEmptySingleHeader(r.Header, tailscaleUserNameHeader) &&
		nonEmptySingleHeader(r.Header, tailscaleUserProfilePicHeader)
}

func exactSingleHeaderValue(header http.Header, name, expected string) bool {
	values := header.Values(name)
	return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), expected)
}

func nonEmptySingleHeader(header http.Header, name string) bool {
	values := header.Values(name)
	return len(values) == 1 && strings.TrimSpace(values[0]) != ""
}

func browserHeadersMatchAdmittedOrigin(r *http.Request, admittedOrigin string) bool {
	for _, name := range []string{"Origin", "Referer"} {
		raw := strings.TrimSpace(r.Header.Get(name))
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.Scheme == "" || parsed.Host == "" {
			return false
		}
		headerOrigin, err := normalizeAbsoluteOrigin(parsed)
		if err != nil || headerOrigin != admittedOrigin {
			return false
		}
	}
	return true
}

func normalizeAbsoluteOrigin(parsed *url.URL) (string, error) {
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", errors.New("unsupported browser origin scheme")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return "", errors.New("browser origin host is required")
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	authority := hostname
	if ip := net.ParseIP(hostname); ip != nil && strings.Contains(hostname, ":") {
		authority = "[" + hostname + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(hostname, port)
	}
	return scheme + "://" + authority, nil
}

func containsExactString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func admittedDesktopOrigin(r *http.Request) (desktopAdmission, bool) {
	if r == nil {
		return desktopAdmission{}, false
	}
	admission, ok := r.Context().Value(desktopAdmittedOriginKey).(desktopAdmission)
	return admission, ok && admission.origin != ""
}

func pendingDesktopOrigin(r *http.Request) (desktopAdmission, bool) {
	if r == nil {
		return desktopAdmission{}, false
	}
	admission, ok := r.Context().Value(desktopPendingOriginKey).(desktopAdmission)
	return admission, ok && admission.origin != "" && admission.tailscaleServe
}
