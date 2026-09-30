package api

import (
 "encoding/json"
 "errors"
 "net/http"

 "swarm/packages/swarmd/internal/identity"
)

// Authenticated user maintenance only; never exposed as an AI tool or invoked by
// scope/card reads. The account comes exclusively from the authenticated principal.
func (s *Server) handleUsageScopeRepair(w http.ResponseWriter, r *http.Request) {
 p, ok := PrincipalFromRequest(r)
 if !ok { writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired); return }
 if r.Method != http.MethodPost { writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed")); return }
 if _, scoped := ScopedTokenFromRequest(r); scoped || p.Type != identity.PrincipalTypeUser { writeError(w, http.StatusForbidden, errors.New("usage repair requires authenticated user authority")); return }
 var request struct { Cursor string `json:"cursor"`; Limit int `json:"limit"` }
 decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)); decoder.DisallowUnknownFields()
 if err := decoder.Decode(&request); err != nil { writeError(w, http.StatusBadRequest, err); return }
 if request.Limit < 1 || request.Limit > 100 { writeError(w, http.StatusBadRequest, errors.New("repair limit must be 1 through 100")); return }
 result, err := s.sessions.Store().RepairUsageScopes(p.AccountScopeID, request.Cursor, request.Limit)
 if err != nil { writeError(w, http.StatusBadRequest, err); return }
 writeJSON(w, http.StatusOK, result)
}
