package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// CompanionContextPath is the account user's Companion context bank: topics
// (indexed automatically from substantive Companion messages), decisions and
// work pointers, each with its original source. It is separate from explicit
// account memory and never changes it. Every read is bounded; results are
// verified against their sources and filtered by the Companion's current
// workspace scope; their text is untrusted data.
const CompanionContextPath = "/v3/companion/context"

type companionContextPutRequest struct {
	ID               string                             `json:"id"`
	Kind             string                             `json:"kind"`
	Title            string                             `json:"title"`
	Summary          string                             `json:"summary,omitempty"`
	Keywords         []string                           `json:"keywords,omitempty"`
	WorkspacePath    string                             `json:"workspace_path,omitempty"`
	Source           pebblestore.CompanionContextSource `json:"source"`
	ExpectedRevision int64                              `json:"expected_revision"`
}

type companionContextDeleteRequest struct {
	ID               string `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type companionContextResult struct {
	Record     pebblestore.CompanionContextRecord `json:"record"`
	Score      int                                `json:"score"`
	SourceText string                             `json:"source_text,omitempty"`
}

func (s *Server) handleCompanionContext(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if r.URL.Path != CompanionContextPath {
		http.NotFound(w, r)
		return
	}
	scope := "sessions:read"
	if r.Method != http.MethodGet {
		scope = "sessions:write"
	}
	if !s.requireScope(w, r, scope) {
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session store unavailable"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleCompanionContextGet(w, r, principal)
	case http.MethodPost:
		var req companionContextPutRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.handleCompanionContextPut(w, principal, req)
	case http.MethodDelete:
		var req companionContextDeleteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		err := s.sessions.Store().DeleteCompanionContext(principal.AccountScopeID, principal.UserID, req.ID, req.ExpectedRevision)
		if err != nil {
			writeCompanionContextError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_id": strings.TrimSpace(req.ID)})
	default:
		methodNotAllowed(w)
	}
}

func writeCompanionContextError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pebblestore.ErrCompanionContextConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, pebblestore.ErrCompanionContextNotFound), errors.Is(err, pebblestore.ErrCompanionContextSource):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, pebblestore.ErrCompanionContextInvalid):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// companionContextScope is the Companion session's current runtime workspace
// scope, the same scope its turns retrieve under. Without a Companion or a
// resolvable scope, workspace-bound records are not visible.
func (s *Server) companionContextScope(principal identity.Principal) (func(string) bool, error) {
	record, found, err := s.sessions.Store().GetCompanion(principal.AccountScopeID, principal.UserID)
	if err != nil || !found {
		return nil, err
	}
	session, err := s.companionSession(principal, record)
	if err != nil || session == nil {
		return nil, err
	}
	resolver, ok := s.runner.(interface {
		ResolveRuntimeWorkspaceScope(pebblestore.SessionSnapshot, identity.Principal) (tool.WorkspaceScope, error)
	})
	if !ok || resolver == nil {
		return nil, nil
	}
	scope, err := resolver.ResolveRuntimeWorkspaceScope(*session, principal)
	if err != nil {
		return nil, nil
	}
	return pebblestore.CompanionWorkspaceScope(scope.Roots), nil
}

func (s *Server) handleCompanionContextGet(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	query := r.URL.Query()
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	allow, err := s.companionContextScope(principal)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	store := s.sessions.Store()
	q, kind := strings.TrimSpace(query.Get("q")), strings.TrimSpace(query.Get("kind"))
	if q != "" || kind != "" {
		if strings.TrimSpace(query.Get("cursor")) != "" {
			writeError(w, http.StatusBadRequest, errors.New("cursor pages the full list; it cannot be combined with q or kind"))
			return
		}
		hits, stats, err := store.RetrieveCompanionContext(principal.AccountScopeID, principal.UserID, pebblestore.CompanionContextQuery{
			Keywords: pebblestore.CompanionContextKeywords(q), Kind: kind, MaxResults: limit, AllowWorkspace: allow, Now: time.Now().UnixMilli(),
		})
		if err != nil {
			writeCompanionContextError(w, err)
			return
		}
		results := make([]companionContextResult, 0, len(hits))
		for _, hit := range hits {
			results = append(results, companionContextResult{Record: hit.Record, Score: hit.Score, SourceText: hit.SourceText})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results, "read_stats": stats, "content_trust": "untrusted"})
		return
	}
	records, next, stats, err := store.ListCompanionContext(principal.AccountScopeID, principal.UserID, query.Get("cursor"), limit)
	if err != nil {
		writeCompanionContextError(w, err)
		return
	}
	visible := make([]pebblestore.CompanionContextRecord, 0, len(records))
	hidden := 0
	for _, record := range records {
		if record.WorkspacePath != "" && (allow == nil || !allow(record.WorkspacePath)) {
			hidden++
			continue
		}
		visible = append(visible, record)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "records": visible, "next_cursor": next, "scope_hidden": hidden, "read_stats": stats, "content_trust": "untrusted"})
}

func (s *Server) handleCompanionContextPut(w http.ResponseWriter, principal identity.Principal, req companionContextPutRequest) {
	if req.Kind != pebblestore.CompanionContextKindDecision && req.Kind != pebblestore.CompanionContextKindWork {
		writeError(w, http.StatusBadRequest, errors.New("kind must be decision or work; topics are indexed from Companion messages"))
		return
	}
	if workspace := strings.TrimSpace(req.WorkspacePath); workspace != "" {
		allow, err := s.companionContextScope(principal)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if allow == nil || !allow(workspace) {
			writeError(w, http.StatusForbidden, errors.New("workspace_path is outside the Companion's current workspace scope"))
			return
		}
	}
	record, changed, err := s.sessions.Store().PutCompanionContext(pebblestore.CompanionContextRecord{
		ID: req.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: req.Kind,
		Title: req.Title, Summary: req.Summary, Keywords: req.Keywords, WorkspacePath: req.WorkspacePath, Source: req.Source,
	}, req.ExpectedRevision, time.Now().UnixMilli())
	if err != nil {
		writeCompanionContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed, "record": record})
}
