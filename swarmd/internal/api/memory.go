package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/memory"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Server) SetMemoryService(m *memory.Service) { s.memory = m }
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok || !p.Valid() {
		http.Error(w, "authentication required", 401)
		return
	}
	if s.memory == nil {
		http.Error(w, "memory unavailable", 503)
		return
	}
	var result any
	var err error
	if r.Method == http.MethodGet {
		if r.URL.Query().Has("session_id") {
			result, err = s.memory.Store.SelectionForSession(p.AccountScopeID, p.UserID, r.URL.Query().Get("session_id"))
		} else {
			result, err = s.memory.Store.GetForAccount(p.AccountScopeID)
		}
	} else if r.Method == http.MethodPost {
		var req struct {
			Action           string               `json:"action"`
			ExpectedRevision int64                `json:"expected_revision"`
			Reason           string               `json:"reason"`
			Entry            store.MemoryEntry    `json:"entry"`
			EntryID          string               `json:"entry_id"`
			Source           store.MemorySource   `json:"source"`
			Settings         store.MemorySettings `json:"settings"`
			RestoreRevision  int64                `json:"restore_revision"`
			JobID            string               `json:"job_id"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&req); err != nil {
			http.Error(w, "invalid memory request", 400)
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			http.Error(w, "one request required", 400)
			return
		}
		switch req.Action {
		case "remember":
			result, err = s.memory.Remember(r.Context(), req.ExpectedRevision, req.Entry, req.Reason)
		case "settings":
			result, err = s.memory.Configure(r.Context(), req.ExpectedRevision, req.Settings)
		case "forget", "forget_source", "restore":
			op := req.Action
			if op == "forget" {
				op = "delete"
			}
			result, err = s.memory.Store.MutateForAccount(p.AccountScopeID, store.MemoryMutation{ExpectedRevision: req.ExpectedRevision, Actor: store.MemoryActor{Kind: "user", ID: p.UserID}, Reason: req.Reason, Operation: op, EntryID: req.EntryID, Source: req.Source, RestoreRevision: req.RestoreRevision})
		case "run_now":
			result, err = s.memory.RunNow(r.Context(), req.JobID)
		case "cancel":
			err = s.memory.Cancel(r.Context(), req.JobID)
			result = map[string]bool{"cancelled": err == nil}
		case "approve":
			result, err = s.memory.Approve(r.Context(), req.JobID)
		default:
			http.Error(w, "unknown memory action", 400)
			return
		}
	} else {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrMemoryConflict) {
			status = 409
		}
		if errors.Is(err, store.ErrMemoryPolicy) {
			status = 403
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
