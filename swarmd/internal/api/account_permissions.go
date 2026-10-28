package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

type accountPermissionsPatch struct {
	ExpectedRevision *uint64 `json:"expected_revision"`
	ClientRequestID  string  `json:"client_request_id"`
	Tasks            *string `json:"tasks,omitempty"`
	Devices          map[string]struct {
		Files   *string `json:"files,omitempty"`
		Browser *string `json:"browser,omitempty"`
	} `json:"devices,omitempty"`
	Linked map[string]struct {
		Work *string `json:"work,omitempty"`
	} `json:"linked,omitempty"`
}

func (s *Server) handleAccountPermissions(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok {
		writeError(w, 401, errors.New("principal required"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	actor, _ := productActorFromRequest(r)
	current, err := s.sessions.Store().GetAccountPermissions(principal.AccountScopeID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if token, ok := ScopedTokenFromRequest(r); ok && token.AgentName != "" {
		writeError(w, 403, errors.New("gateway tokens cannot access account permissions"))
		return
	}
	if _, bound := r.Context().Value(applicationAgentContextKey{}).(applicationAgentBinding); bound {
		writeError(w, 403, errors.New("gateway tokens cannot access account permissions"))
		return
	}
	if r.Method == http.MethodGet {
		s.writeAccountPermissions(w, r, 200, current)
		return
	}
	if err := sessionAccessAuthority(r); err != nil {
		writeError(w, 403, err)
		return
	}
	var req accountPermissionsPatch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, err)
		return
	}
	if req.ExpectedRevision == nil || req.ClientRequestID == "" || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, errors.New("expected_revision, client_request_id and exactly one request required"))
		return
	}
	if *req.ExpectedRevision != current.Revision {
		s.writeAccountPermissions(w, r, 409, current)
		return
	}
	by := "owner"
	if token, ok := ScopedTokenFromRequest(r); ok {
		by = token.Name
	}
	clients, err := s.identitySessions.ListClientSessions()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	active := map[string]bool{}
	now := time.Now().UnixMilli()
	for _, client := range clients {
		if client.AccountScopeID == principal.AccountScopeID && client.Device != "" && client.RevokedAt == 0 && client.ExpiresAt > now {
			active[client.SessionID] = true
			if client.SessionID == actor.ClientSessionID {
				by = client.Device
			}
		}
	}
	for id := range req.Devices {
		if id != actor.ClientSessionID || !active[id] {
			writeError(w, 403, errors.New("change device rules on that device using its own client session"))
			return
		}
	}
	if req.Tasks != nil {
		current.Tasks = *req.Tasks
	}
	for _, patch := range req.Devices {
		for _, value := range []*string{patch.Files, patch.Browser} {
			if value != nil && *value == store.RuleAsk {
				writeError(w, 400, store.ErrDeviceAskRetired)
				return
			}
		}
	}
	for id, patch := range req.Devices {
		rule := current.Device(id)
		wasAsk := []string{}
		for _, kind := range rule.WasAsk {
			if (kind == "files" && patch.Files == nil) || (kind == "browser" && patch.Browser == nil) {
				wasAsk = append(wasAsk, kind)
			}
		}
		rule.WasAsk = nil
		if len(wasAsk) > 0 {
			rule.WasAsk = wasAsk
		}
		if patch.Files != nil {
			rule.Files = *patch.Files
		}
		if patch.Browser != nil {
			rule.Browser = *patch.Browser
		}
		rule.Changed = store.PermissionChange{By: by, At: now}
		current.Devices[id] = rule
	}
	for name, patch := range req.Linked {
		if s.fleetTokens == nil {
			writeError(w, 400, errors.New("linked machine not found"))
			return
		}
		_, owned, err := s.fleetTokens.GetFleetLinkToken(principal.AccountScopeID, name)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !owned {
			writeError(w, 400, errors.New("linked machine not found"))
			return
		}
		rule := current.Link(name)
		if patch.Work != nil {
			// allowed is the retired name of auto (older clients).
			rule.Work = store.NormalizeLinkedRule(*patch.Work)
		}
		rule.Changed = store.PermissionChange{By: by, At: now}
		current.Linked[name] = rule
	}
	saved, err := s.sessions.Store().Underlying().SetAccountPermissions(principal.AccountScopeID, *req.ExpectedRevision, current)
	if errors.Is(err, store.ErrAccountPermissionsConflict) {
		s.writeAccountPermissions(w, r, 409, saved)
		return
	}
	if err != nil {
		writeError(w, 400, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"revision": saved.Revision})
	key := fmt.Sprintf("permissions:%s:%d", principal.AccountScopeID, saved.Revision)
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: "__account_permissions__:" + principal.AccountScopeID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: "permissions.update", EventType: "permissions.updated", EventPayload: payload, NowUnixMs: now})
	if err != nil {
		writeError(w, 500, fmt.Errorf("permissions stored but realtime publication failed: %w", err))
		return
	}
	s.writeAccountPermissions(w, r, 200, saved)
}
