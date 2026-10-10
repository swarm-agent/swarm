package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Signal scopes. Reading the feed needs signals:read; reporting an outside
// event (a host security monitor, a script) needs signals:write. The owner
// and local callers carry no scoped token and may do both.
const (
	signalsReadScope  = "signals:read"
	signalsWriteScope = "signals:write"
	maxSignalBody     = 16 << 10
)

// handleSignals serves the machine signal feed.
//
//	GET  /v3/signals?after=N&limit=N&kind=agent,run.failed&min_severity=warning
//	POST /v3/signals  {kind:"external.…", severity, summary, source, dedup_key, refs, attrs}
//
// Readers see their account's signals and machine-wide ones. Outside sources
// may only report kinds under "external.", so they cannot imitate the signals
// Swarm itself raises; the source is recorded as "external:<name>" and a
// scoped token's id is recorded with what it reported.
func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	if s.signals == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("the signal feed is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	account := strings.TrimSpace(principal.AccountScopeID)
	switch r.Method {
	case http.MethodGet:
		if !s.requireScope(w, r, signalsReadScope) {
			return
		}
		s.listSignals(w, r, account)
	case http.MethodPost:
		if !s.requireScope(w, r, signalsWriteScope) {
			return
		}
		s.ingestSignal(w, r, account)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) listSignals(w http.ResponseWriter, r *http.Request, account string) {
	query := r.URL.Query()
	var after uint64
	if raw := strings.TrimSpace(query.Get("after")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after must be a signal number"))
			return
		}
		after = parsed
	}
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive number"))
			return
		}
		limit = parsed
	}
	filter := pebblestore.SignalFilter{Account: account, MinSeverity: strings.TrimSpace(query.Get("min_severity"))}
	for _, kind := range strings.Split(query.Get("kind"), ",") {
		if kind = strings.TrimSpace(kind); kind != "" {
			filter.KindPrefixes = append(filter.KindPrefixes, kind)
		}
	}
	page, err := s.signals.ListAfter(after, limit, filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signals": page.Signals, "next_after": page.NextAfter, "oldest_seq": page.OldestSeq, "latest_seq": page.LatestSeq, "gap": page.Gap})
}

func (s *Server) ingestSignal(w http.ResponseWriter, r *http.Request, account string) {
	var req struct {
		Kind     string            `json:"kind"`
		Severity string            `json:"severity"`
		Summary  string            `json:"summary"`
		Source   string            `json:"source"`
		DedupKey string            `json:"dedup_key"`
		Refs     map[string]string `json:"refs"`
		Attrs    map[string]string `json:"attrs"`
	}
	if err := decodeJSONLimited(w, r, &req, maxSignalBody); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if !strings.HasPrefix(kind, pebblestore.SignalKindExternalRoot+".") {
		writeError(w, http.StatusBadRequest, errors.New(`reported signals must use a kind under "external.", such as external.falco`))
		return
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	if source == "" {
		source = "unnamed"
	}
	if !pebblestore.ValidSignalSourceName(source) {
		writeError(w, http.StatusBadRequest, errors.New("source must be lowercase letters, digits and dashes"))
		return
	}
	if _, reserved := req.Refs["reported_by_token"]; reserved {
		writeError(w, http.StatusBadRequest, errors.New(`the ref "reported_by_token" is set by Swarm`))
		return
	}
	refs := req.Refs
	if scoped, ok := ScopedTokenFromRequest(r); ok {
		refs = make(map[string]string, len(req.Refs)+1)
		for k, v := range req.Refs {
			refs[k] = v
		}
		refs["reported_by_token"] = scoped.ID
	}
	sig, err := s.signals.Append(pebblestore.Signal{
		Kind:     kind,
		Severity: req.Severity,
		Source:   pebblestore.SignalSourceExternal + ":" + source,
		Account:  account,
		Summary:  req.Summary,
		DedupKey: req.DedupKey,
		Refs:     refs,
		Attrs:    req.Attrs,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signal": sig})
}
