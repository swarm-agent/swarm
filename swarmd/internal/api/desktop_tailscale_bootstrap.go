package api

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/tailscale"
)

const tailscaleDesktopBootstrapPath = "/__swarm/tailscale-serve-approval"

var tailscaleDesktopBootstrapPage = template.Must(template.New("tailscale-desktop-bootstrap").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="same-origin">
<title>Approve Tailscale access · Swarm</title>
<style>
:root{color-scheme:dark;font-family:ui-sans-serif,system-ui,sans-serif;background:#0b0b0d;color:#f7f4f2}body{min-height:100vh;margin:0;display:grid;place-items:center;background:radial-gradient(circle at top,#25151a,#0b0b0d 55%)}main{width:min(34rem,calc(100% - 3rem));padding:2rem;border:1px solid #3b3033;border-radius:1.25rem;background:#141215;box-shadow:0 2rem 6rem #0008}h1{margin:.2rem 0 1rem;font-size:1.65rem}p{line-height:1.6;color:#c9c1c3}code{display:block;overflow-wrap:anywhere;margin:1.25rem 0;padding:1rem;border-radius:.75rem;background:#09090b;color:#fff}button{width:100%;padding:.85rem 1rem;border:0;border-radius:.75rem;background:#e44b65;color:white;font:inherit;font-weight:700;cursor:pointer}small{display:block;margin-top:1rem;color:#8f8588}</style>
</head>
<body><main>
<p>Swarm first-launch security</p>
<h1>Approve this Tailscale Serve address?</h1>
<p>This Swarm is still being set up. Approving adds only the verified address below to this machine's Desktop allowlist.</p>
<code>{{.Origin}}</code>
<form method="post" action="` + tailscaleDesktopBootstrapPath + `"><button type="submit">Approve and continue</button></form>
<small>The address is read from this node's active Tailscale Serve configuration, not from the browser.</small>
</main></body></html>`))

type tailscaleDesktopBootstrapCandidate struct {
	origin string
}

func (s *Server) tryTailscaleDesktopBootstrap(w http.ResponseWriter, r *http.Request) bool {
	if !isTailscaleBootstrapSurfaceRequest(r) {
		return false
	}
	candidate, err := s.verifyTailscaleDesktopBootstrap(r)
	if err != nil {
		return false
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = tailscaleDesktopBootstrapPage.Execute(w, struct{ Origin string }{Origin: candidate.origin})
		return true
	}

	if r.Body != nil {
		defer r.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1))
		if readErr != nil || len(body) != 0 {
			writeError(w, http.StatusBadRequest, errors.New("tailscale bootstrap approval accepts no browser-supplied values"))
			return true
		}
	}
	_, changed, err := s.tailscaleServePolicy.Add(candidate.origin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("tailscale desktop approval could not be persisted"))
		return true
	}
	if !changed {
		writeError(w, http.StatusForbidden, errors.New("tailscale desktop bootstrap approval is no longer available"))
		return true
	}
	s.tailscaleServeDetector.Invalidate()
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return true
}

func isTailscaleBootstrapSurfaceRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.Method == http.MethodPost {
		return r.URL.Path == tailscaleDesktopBootstrapPath
	}
	if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != tailscaleDesktopBootstrapPath) {
		return false
	}
	return exactSingleHeaderValue(r.Header, "Sec-Fetch-Mode", "navigate") &&
		(strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") || r.URL.Path == tailscaleDesktopBootstrapPath)
}

func (s *Server) verifyTailscaleDesktopBootstrap(r *http.Request) (tailscaleDesktopBootstrapCandidate, error) {
	if s == nil || s.tailscaleServePolicy == nil || s.tailscaleServeDetector == nil {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale desktop policy or verifier is unavailable")
	}
	if !s.desktopOnboardingIncomplete() {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("desktop onboarding is already complete")
	}
	rawAuthority := requestHost(r)
	if !forwardedHostMatchesRequestAuthority(r, rawAuthority) || authorityHasExplicitPort(rawAuthority) {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale desktop authority is invalid")
	}
	host, err := normalizeRequestAuthority(rawAuthority)
	if err != nil || isCanonicalLocalDesktopHost(host) {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale desktop authority is invalid")
	}
	origin, err := normalizeExternalDesktopOrigin(host)
	if err != nil {
		return tailscaleDesktopBootstrapCandidate{}, err
	}
	if !trustedTailscaleServeProvenance(r) || !browserHeadersMatchAdmittedOrigin(r, origin) {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("trusted same-origin Tailscale Serve provenance is required")
	}
	record, ok, err := s.tailscaleServePolicy.Get()
	if err != nil {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale desktop policy is unreadable")
	}
	if ok && containsExactString(record.Origins, origin) {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale desktop origin is already approved")
	}
	s.tailscaleServeDetector.Invalidate()
	snapshot, err := s.tailscaleServeDetector.Snapshot(r.Context(), tailscale.RequireFresh)
	if err != nil {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("fresh tailscale route verification is unavailable")
	}
	if snapshot.SelfOrigin != origin {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale identity does not own this node")
	}
	requestLogin := strings.TrimSpace(r.Header.Get(tailscaleUserLoginHeader))
	ownerMatches, productOwnerExists, err := s.tailscaleBootstrapOwnerMatches(requestLogin)
	if err != nil {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("Swarm owner identity is unavailable")
	}
	if productOwnerExists {
		if !ownerMatches {
			return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale identity is not the Swarm owner")
		}
	} else if !strings.EqualFold(strings.TrimSpace(snapshot.OwnerLogin), requestLogin) {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("tailscale identity does not own this node")
	}
	route, found := snapshot.RouteForOrigin(origin)
	if !found || route.Classification != tailscale.ClassificationVerifiedSwarmDesktop {
		return tailscaleDesktopBootstrapCandidate{}, errors.New("origin is not this node's verified non-Funnel Swarm Desktop route")
	}
	return tailscaleDesktopBootstrapCandidate{origin: origin}, nil
}

func (s *Server) tailscaleBootstrapOwnerMatches(login string) (matches bool, ownerExists bool, err error) {
	login = strings.TrimSpace(login)
	if s == nil || s.identityService == nil {
		return false, false, nil
	}
	summary, err := s.identityService.StateSummary()
	if err != nil {
		return false, false, err
	}
	if summary.CurrentUser == nil || summary.AccountScope == nil || summary.CurrentSelection == nil {
		return false, false, nil
	}
	owner := summary.CurrentUser
	ownerID := strings.TrimSpace(summary.AccountScope.CreatedByUserID)
	if ownerID == "" {
		ownerID = strings.TrimSpace(summary.AccountScope.UserID)
	}
	if ownerID == "" || ownerID != strings.TrimSpace(owner.ID) {
		return false, true, nil
	}
	return login != "" && (strings.EqualFold(login, strings.TrimSpace(owner.Email)) ||
		strings.EqualFold(login, strings.TrimSpace(owner.Username)) ||
		strings.EqualFold(login, strings.TrimSpace(owner.ID))), true, nil
}

func (s *Server) desktopOnboardingIncomplete() bool {
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return false
	}
	if cfg.DesktopOnboardingCompleteSet && cfg.DesktopOnboardingComplete {
		return false
	}
	_, identityBootstrapped, err := s.onboardingIdentityPayload()
	if err != nil {
		return false
	}
	return shouldShowOnboarding(cfg, identityBootstrapped)
}
