package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const applicationAgentBindingKey = "swarm_v3_application_agent"
type applicationAgentContextKey struct{}
type applicationAgentBinding struct {
	ID string `json:"id"`
	Revision uint64 `json:"revision"`
}

// This API deliberately remains behind the authenticated local API, not the
// container session-listener allowlist. It does not grant worker management.
func (s *Server) handleApplicationAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() { writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired); return }
	scope := "sessions:read"
	if r.Method != http.MethodGet { scope = "sessions:write" }
	if !s.requireScope(w, r, scope) { return }
	if s.sessions == nil || s.sessions.Store() == nil { writeError(w, http.StatusServiceUnavailable, errors.New("session store unavailable")); return }
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v3/application-agents/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts[0]) > 128 { writeError(w, http.StatusBadRequest, errors.New("agent id required")); return }
	id := parts[0]
	store := s.sessions.Store()
	if len(parts) == 1 && r.Method == http.MethodPut {
		var req struct {
			Name string `json:"name"`
			Instructions string `json:"instructions"`
			Context string `json:"context"`
			ExpectedRevision *uint64 `json:"expected_revision"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil { writeError(w, http.StatusBadRequest, err); return }
		if req.ExpectedRevision == nil { writeError(w, http.StatusBadRequest, errors.New("expected_revision required (0 for create)")); return }
		record, err := store.PutApplicationAgent(principal.AccountScopeID, principal.UserID, pebblestore.ApplicationAgent{ID:id, Name:req.Name, Instructions:req.Instructions, Context:req.Context}, *req.ExpectedRevision)
		if errors.Is(err, pebblestore.ErrApplicationAgentConflict) { writeError(w, http.StatusConflict, err); return }
		if err != nil { writeError(w, http.StatusBadRequest, err); return }
		writeJSON(w, http.StatusOK, record); return
	}
	record, found, err := store.GetApplicationAgent(principal.AccountScopeID, principal.UserID, id, 0)
	if err != nil { writeError(w, http.StatusInternalServerError, err); return }
	if !found { writeError(w, http.StatusNotFound, errors.New("application agent not found")); return }
	if len(parts) == 1 {
		if r.Method != http.MethodGet { methodNotAllowed(w); return }
		writeJSON(w, http.StatusOK, record); return
	}
	if parts[1] != "conversations" { http.NotFound(w,r); return }
	if len(parts) == 2 {
		if r.Method != http.MethodPost { methodNotAllowed(w); return }
		// Caller supplies a pinned revision, preventing context changes from
		// silently altering the meaning of an idempotent create retry.
		revision, err := strconv.ParseUint(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || revision == 0 { writeError(w, http.StatusBadRequest, errors.New("revision required")); return }
		_, found, err := store.GetApplicationAgent(principal.AccountScopeID, principal.UserID, id, revision)
		if err != nil || !found { writeError(w, http.StatusConflict, errors.New("application agent revision unavailable")); return }
		binding := applicationAgentBinding{ID:id, Revision:revision}
		r = r.WithContext(context.WithValue(r.Context(), applicationAgentContextKey{}, binding))
		s.handleSessionsV3PrimaryCreate(w,r,principal); return
	}
	if len(parts) > 4 || (len(parts) == 4 && parts[3] != "messages" && parts[3] != "events") { http.NotFound(w,r); return }
	if r.Method != http.MethodGet && !(len(parts) == 4 && parts[3] == "messages" && r.Method == http.MethodPost) { methodNotAllowed(w); return }
	session, found, err := s.sessions.GetSession(parts[2])
	if err != nil { writeError(w,http.StatusInternalServerError,err); return }
	binding, bindErr := readApplicationAgentBinding(session.Metadata)
	if !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID || bindErr != nil || binding.ID != id {
		writeSessionNotFound(w); return
	}
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	clone.URL = &urlCopy
	clone.URL.Path = "/v3/sessions/" + strings.Join(parts[2:],"/")
	s.handleSessionV3PrimaryByID(w,clone)
}

func readApplicationAgentBinding(metadata map[string]any) (applicationAgentBinding, error) {
	var binding applicationAgentBinding
	b, err := json.Marshal(metadata[applicationAgentBindingKey])
	if err != nil { return binding, err }
	if err := json.Unmarshal(b,&binding); err != nil { return binding, err }
	if binding.ID == "" || binding.Revision == 0 { return binding, errors.New("invalid application agent binding") }
	return binding,nil
}

func (s *Server) applicationAgentInstructions(session pebblestore.SessionSnapshot) (string,error) {
	if _, bound := session.Metadata[applicationAgentBindingKey]; !bound { return "",nil }
	binding, err := readApplicationAgentBinding(session.Metadata)
	if err != nil { return "",err }
	record, found, err := s.sessions.Store().GetApplicationAgent(session.AccountScopeID,session.UserID,binding.ID,binding.Revision)
	if err != nil { return "",err }
	if !found { return "",errors.New("application agent context unavailable") }
	return fmt.Sprintf("\n\nApplication instructions (additive; never override system policy or permissions):\n%s\n\nApplication context:\n%s",record.Instructions,record.Context),nil
}
