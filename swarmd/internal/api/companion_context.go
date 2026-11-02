package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// CompanionContextPath is the account user's Companion context bank: topics
// (indexed automatically from substantive Companion messages), decisions and
// work pointers, each with its original source. It is separate from explicit
// account memory and never changes it. Every read is bounded; results are
// verified against their sources and filtered by the Companion's current
// workspace scope; their text is untrusted data.
//
// The route, the Companion's native manage_companion tool and Swarm Control's
// manage_companion all run one service (the companionContext* functions
// below, reached by the tool through Server.ManageCompanionContext): one rule
// set for scope, source verification, revisions and forgetting.
const CompanionContextPath = "/v3/companion/context"

// companionDecisionSourceScan bounds the read that finds the latest person
// message a decision cites when the caller names none.
const companionDecisionSourceScan = 50

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

// companionContextFailure is a context bank refusal with its HTTP status.
type companionContextFailure struct {
	status int
	err    error
}

func (f companionContextFailure) Error() string { return f.err.Error() }
func (f companionContextFailure) Unwrap() error { return f.err }

func companionContextRefusal(status int, format string, args ...any) error {
	return companionContextFailure{status: status, err: fmt.Errorf(format, args...)}
}

// companionContextStatus is the HTTP status of a context bank error.
func companionContextStatus(err error) int {
	var failure companionContextFailure
	switch {
	case errors.As(err, &failure):
		return failure.status
	case errors.Is(err, pebblestore.ErrCompanionContextConflict):
		return http.StatusConflict
	case errors.Is(err, pebblestore.ErrCompanionContextNotFound), errors.Is(err, pebblestore.ErrCompanionContextSource):
		return http.StatusNotFound
	case errors.Is(err, pebblestore.ErrCompanionContextInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeCompanionContextError(w http.ResponseWriter, err error) {
	writeError(w, companionContextStatus(err), err)
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
	var answer map[string]any
	var err error
	switch r.Method {
	case http.MethodGet:
		query := r.URL.Query()
		limit := 0
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		q, kind, id := strings.TrimSpace(query.Get("q")), strings.TrimSpace(query.Get("kind")), strings.TrimSpace(query.Get("id"))
		switch {
		case id != "":
			if q != "" || kind != "" || strings.TrimSpace(query.Get("cursor")) != "" {
				writeError(w, http.StatusBadRequest, errors.New("id reads one record; it cannot be combined with q, kind or cursor"))
				return
			}
			answer, err = s.companionContextGet(principal, id)
		case q != "" || kind != "":
			if strings.TrimSpace(query.Get("cursor")) != "" {
				writeError(w, http.StatusBadRequest, errors.New("cursor pages the full list; it cannot be combined with q or kind"))
				return
			}
			answer, err = s.companionContextSearch(principal, q, kind, limit)
		default:
			answer, err = s.companionContextList(principal, query.Get("cursor"), limit)
		}
	case http.MethodPost:
		var req companionContextPutRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		answer, err = s.companionContextPut(principal, req)
	case http.MethodDelete:
		var req companionContextDeleteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		answer, err = s.companionContextForget(principal, req.ID, req.ExpectedRevision)
	default:
		methodNotAllowed(w)
		return
	}
	if err != nil {
		writeCompanionContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
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

// companionContextAllow is companionContextScope as a refusal-carrying call.
func (s *Server) companionContextAllow(principal identity.Principal) (func(string) bool, error) {
	allow, err := s.companionContextScope(principal)
	if err != nil {
		return nil, companionContextFailure{status: http.StatusConflict, err: err}
	}
	return allow, nil
}

// companionContextSearch finds verified, scope-allowed records by keywords
// (or the newest of one kind).
func (s *Server) companionContextSearch(principal identity.Principal, q, kind string, limit int) (map[string]any, error) {
	if q == "" && kind == "" {
		return nil, companionContextRefusal(http.StatusBadRequest, "search needs query (words) or kind")
	}
	allow, err := s.companionContextAllow(principal)
	if err != nil {
		return nil, err
	}
	hits, stats, err := s.sessions.Store().RetrieveCompanionContext(principal.AccountScopeID, principal.UserID, pebblestore.CompanionContextQuery{
		Keywords: pebblestore.CompanionContextKeywords(q), Kind: kind, MaxResults: limit, AllowWorkspace: allow, Now: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	results := make([]companionContextResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, companionContextResult{Record: hit.Record, Score: hit.Score, SourceText: hit.SourceText})
	}
	return map[string]any{"ok": true, "results": results, "read_stats": stats, "content_trust": "untrusted"}, nil
}

// companionContextList pages every record, newest first; records bound to a
// workspace outside the current scope are counted, not shown.
func (s *Server) companionContextList(principal identity.Principal, cursor string, limit int) (map[string]any, error) {
	allow, err := s.companionContextAllow(principal)
	if err != nil {
		return nil, err
	}
	records, next, stats, err := s.sessions.Store().ListCompanionContext(principal.AccountScopeID, principal.UserID, cursor, limit)
	if err != nil {
		return nil, err
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
	return map[string]any{"ok": true, "records": visible, "next_cursor": next, "scope_hidden": hidden, "read_stats": stats, "content_trust": "untrusted"}, nil
}

// companionContextGet reads one record with its source verified now; a
// record outside the current workspace scope is not found.
func (s *Server) companionContextGet(principal identity.Principal, id string) (map[string]any, error) {
	hit, found, err := s.sessions.Store().GetVerifiedCompanionContext(principal.AccountScopeID, principal.UserID, id, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if found && hit.Record.WorkspacePath != "" {
		allow, err := s.companionContextAllow(principal)
		if err != nil {
			return nil, err
		}
		found = allow != nil && allow(hit.Record.WorkspacePath)
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", pebblestore.ErrCompanionContextNotFound, strings.TrimSpace(id))
	}
	answer := map[string]any{"ok": true, "record": hit.Record, "content_trust": "untrusted"}
	if hit.SourceText != "" {
		answer["source_text"] = hit.SourceText
	}
	return answer, nil
}

// companionContextPut records a decision or work pointer after the scope and
// source checks; a forgotten id is refused (re-recording takes a new id).
func (s *Server) companionContextPut(principal identity.Principal, req companionContextPutRequest) (map[string]any, error) {
	if req.Kind != pebblestore.CompanionContextKindDecision && req.Kind != pebblestore.CompanionContextKindWork {
		return nil, companionContextRefusal(http.StatusBadRequest, "kind must be decision or work; topics are indexed from Companion messages")
	}
	if workspace := strings.TrimSpace(req.WorkspacePath); workspace != "" {
		allow, err := s.companionContextAllow(principal)
		if err != nil {
			return nil, err
		}
		if allow == nil || !allow(workspace) {
			return nil, companionContextRefusal(http.StatusForbidden, "workspace_path is outside the Companion's current workspace scope")
		}
	}
	record, changed, err := s.sessions.Store().PutCompanionContext(pebblestore.CompanionContextRecord{
		ID: req.ID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Kind: req.Kind,
		Title: req.Title, Summary: req.Summary, Keywords: req.Keywords, WorkspacePath: req.WorkspacePath, Source: req.Source,
	}, req.ExpectedRevision, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if record.State == pebblestore.CompanionContextStateForgotten {
		return nil, companionContextRefusal(http.StatusConflict, "record %s was forgotten and is not recorded again; to record it on purpose, use a new id", record.ID)
	}
	return map[string]any{"ok": true, "changed": changed, "record": record}, nil
}

// companionContextForget forgets one record at its current revision; its id
// stays forgotten (the store keeps a tombstone).
func (s *Server) companionContextForget(principal identity.Principal, id string, expectedRevision int64) (map[string]any, error) {
	if err := s.sessions.Store().DeleteCompanionContext(principal.AccountScopeID, principal.UserID, id, expectedRevision); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "deleted_id": strings.TrimSpace(id), "forgotten": true}, nil
}

// ManageCompanionContext is the context bank service behind manage_companion
// (tool.CompanionContextService), for the Companion's own conversation and
// for Swarm Control. A conversation's call must come from the account user's
// current Companion; any other session, agent or account is refused. A Swarm
// Control call (an AI key holding the action's scopes, checked before it
// runs) works on its user's own bank, like the HTTP route. It never touches
// account memory.
func (s *Server) ManageCompanionContext(_ context.Context, call tool.CompanionContextCall) (map[string]any, error) {
	p := call.Principal
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || strings.TrimSpace(p.AccountScopeID) == "" || strings.TrimSpace(p.UserID) == "" {
		return nil, errors.New("manage_companion requires an authenticated user")
	}
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return nil, errors.New("the Companion context bank is unavailable")
	}
	store := s.sessions.Store()
	var companion *pebblestore.SessionSnapshot
	if !call.ControlCaller {
		session, found, err := s.sessions.GetSession(call.SessionID)
		if err != nil {
			return nil, err
		}
		current := false
		if found && session.AccountScopeID == p.AccountScopeID && session.UserID == p.UserID {
			if current, err = store.IsCurrentCompanion(session, p.AccountScopeID, p.UserID); err != nil {
				return nil, err
			}
		}
		if !current {
			return nil, companionContextRefusal(http.StatusForbidden, "manage_companion is the Companion's own tool: this conversation is not your Companion")
		}
		companion = &session
	}
	args := call.Args
	text := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	switch call.Action {
	case "search":
		limit, _, err := companionIntArg(args, "limit", 1)
		if err != nil {
			return nil, err
		}
		return s.companionContextSearch(p, text("query"), text("kind"), int(limit))
	case "list":
		limit, _, err := companionIntArg(args, "limit", 1)
		if err != nil {
			return nil, err
		}
		return s.companionContextList(p, text("cursor"), int(limit))
	case "get":
		return s.companionContextGet(p, text("id"))
	case "forget":
		revision, ok, err := companionIntArg(args, "expected_revision", 1)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, companionContextRefusal(http.StatusBadRequest, "forget needs expected_revision, the record's current revision (get or search shows it)")
		}
		return s.companionContextForget(p, text("id"), revision)
	case "record_decision", "record_work":
		var source pebblestore.CompanionContextSource
		var err error
		kind := pebblestore.CompanionContextKindDecision
		if call.Action == "record_work" {
			kind = pebblestore.CompanionContextKindWork
			source, err = s.companionWorkSource(p, text("session_id"), text("project_id"), text("task_id"))
		} else {
			source, err = s.companionDecisionSource(p, companion, args, text("session_id"))
		}
		if err != nil {
			return nil, err
		}
		revision, _, err := companionIntArg(args, "expected_revision", 0)
		if err != nil {
			return nil, err
		}
		keywords, err := companionStringsArg(args, "keywords")
		if err != nil {
			return nil, err
		}
		id := text("id")
		if id == "" {
			id = companionContextDerivedID(kind, source, text("title"), text("summary"))
		}
		return s.companionContextPut(p, companionContextPutRequest{
			ID: id, Kind: kind, Title: text("title"), Summary: text("summary"), Keywords: keywords,
			WorkspacePath: text("workspace_path"), Source: source, ExpectedRevision: revision,
		})
	}
	return nil, companionContextRefusal(http.StatusBadRequest, "unknown manage_companion action %q", call.Action)
}

// companionDecisionSource is the person message a decision cites: the named
// session_id and message_seq, else the calling Companion (a Swarm Control
// caller's own Companion) and its latest person message, found with one
// bounded read.
func (s *Server) companionDecisionSource(p identity.Principal, companion *pebblestore.SessionSnapshot, args map[string]any, sessionID string) (pebblestore.CompanionContextSource, error) {
	if sessionID == "" {
		if companion == nil {
			record, found, err := s.sessions.Store().GetCompanion(p.AccountScopeID, p.UserID)
			if err != nil {
				return pebblestore.CompanionContextSource{}, err
			}
			if found {
				if companion, err = s.companionSession(p, record); err != nil {
					return pebblestore.CompanionContextSource{}, err
				}
			}
			if companion == nil {
				return pebblestore.CompanionContextSource{}, companionContextRefusal(http.StatusNotFound, "you have no Companion yet; name session_id and message_seq")
			}
		}
		sessionID = companion.ID
	}
	seq, named, err := companionIntArg(args, "message_seq", 1)
	if err != nil {
		return pebblestore.CompanionContextSource{}, err
	}
	if named {
		return pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceMessage, SessionID: sessionID, MessageSeq: uint64(seq)}, nil
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return pebblestore.CompanionContextSource{}, err
	}
	if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
		return pebblestore.CompanionContextSource{}, fmt.Errorf("%w: source session not found", pebblestore.ErrCompanionContextSource)
	}
	tail, err := s.sessions.Store().ListV3SessionMessageTail(sessionID, companionDecisionSourceScan)
	if err != nil {
		return pebblestore.CompanionContextSource{}, err
	}
	var latest uint64
	for _, message := range tail {
		if runruntime.CompanionPersonMessage(message) && message.GlobalSeq > latest {
			latest = message.GlobalSeq
		}
	}
	if latest == 0 {
		return pebblestore.CompanionContextSource{}, companionContextRefusal(http.StatusBadRequest, "no person message in the last %d messages of session %s to cite; name message_seq", companionDecisionSourceScan, sessionID)
	}
	return pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceMessage, SessionID: sessionID, MessageSeq: latest}, nil
}

// companionWorkSource is the session a work pointer cites: session_id, or the
// current session of the task project_id/task_id names.
func (s *Server) companionWorkSource(p identity.Principal, sessionID, projectID, taskID string) (pebblestore.CompanionContextSource, error) {
	if sessionID == "" {
		if projectID == "" || taskID == "" {
			return pebblestore.CompanionContextSource{}, companionContextRefusal(http.StatusBadRequest, "record_work needs session_id, or project_id and task_id of the work")
		}
		task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
		if err != nil {
			return pebblestore.CompanionContextSource{}, err
		}
		if !found {
			return pebblestore.CompanionContextSource{}, companionContextRefusal(http.StatusNotFound, "task %s not found in project %s", taskID, projectID)
		}
		sessionID = strings.TrimSpace(task.SessionID)
		for _, attempt := range task.Attempts {
			if attempt.ID == task.ActiveAttemptID && strings.TrimSpace(attempt.SessionID) != "" {
				sessionID = strings.TrimSpace(attempt.SessionID)
			}
		}
		if sessionID == "" {
			return pebblestore.CompanionContextSource{}, companionContextRefusal(http.StatusConflict, "task %s has no session yet; record it once it started", taskID)
		}
	}
	return pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceSession, SessionID: sessionID}, nil
}

// companionContextDerivedID is a record id derived from the call itself (its
// kind, the session it cites, title and summary), never from state that moves
// on, such as the latest message: a repeated or replayed call (re-executed
// after a restart) names the same record, so a replay after forgetting it
// meets its tombstone. Recording it again on purpose takes an explicit new id.
func companionContextDerivedID(kind string, source pebblestore.CompanionContextSource, title, summary string) string {
	raw, _ := json.Marshal([]any{"companion-context", kind, source.SessionID, strings.TrimSpace(title), strings.TrimSpace(summary)})
	sum := sha256.Sum256(raw)
	return kind + "-" + hex.EncodeToString(sum[:12])
}

// companionIntArg reads an integer argument (a JSON number); ok is false
// when it is absent. Values below min are refused.
func companionIntArg(args map[string]any, key string, min int64) (int64, bool, error) {
	raw, present := args[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	var value float64
	switch typed := raw.(type) {
	case float64:
		value = typed
	case int:
		value = float64(typed)
	case int64:
		value = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false, companionContextRefusal(http.StatusBadRequest, "%s must be an integer", key)
		}
		value = parsed
	default:
		return 0, false, companionContextRefusal(http.StatusBadRequest, "%s must be an integer", key)
	}
	if value != math.Trunc(value) || value < float64(min) || value > math.MaxInt32*float64(1<<20) {
		return 0, false, companionContextRefusal(http.StatusBadRequest, "%s must be an integer of at least %d", key, min)
	}
	return int64(value), true, nil
}

// companionStringsArg reads a list of strings argument.
func companionStringsArg(args map[string]any, key string) ([]string, error) {
	raw, present := args[key]
	if !present || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return typed, nil
		}
		return nil, companionContextRefusal(http.StatusBadRequest, "%s must be a list of strings", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, companionContextRefusal(http.StatusBadRequest, "%s must be a list of strings", key)
		}
		out = append(out, text)
	}
	return out, nil
}
