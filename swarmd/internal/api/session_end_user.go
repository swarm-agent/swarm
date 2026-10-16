package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A gateway names the end user (its customer or visitor) a session belongs to
// when it creates the session. The binding is stored once in protected session
// metadata (pebblestore.SessionEndUserMetadataKey): no route changes it, the
// model never sees it, and per-end-user limits and usage read it from there.

// sessionsV3EndUserBinding returns the metadata value for a create request's
// end_user and labels. Only a gateway token for agentName may set them.
func sessionsV3EndUserBinding(r *http.Request, agentName, endUser string, labels map[string]string) (map[string]any, error) {
	rec, scoped := ScopedTokenFromRequest(r)
	if !scoped || strings.TrimSpace(rec.AgentName) == "" || rec.AgentOperator {
		return nil, errors.New("end_user and labels are set only by a gateway (agent-bound) token")
	}
	if strings.TrimSpace(rec.AgentName) != strings.TrimSpace(agentName) {
		return nil, errors.New("end_user is set only on the gateway token's own agent")
	}
	if endUser != "" {
		if err := pebblestore.ValidateEndUser(endUser); err != nil {
			return nil, err
		}
	}
	if err := pebblestore.ValidateEndUserLabels(labels); err != nil {
		return nil, err
	}
	return pebblestore.EndUserMetadata(strings.TrimSpace(agentName), endUser, labels), nil
}

// addSessionEndUserFields adds end_user and labels to a session response.
func addSessionEndUserFields(response map[string]any, metadata map[string]any) error {
	binding, ok, err := pebblestore.SessionEndUserFromMetadata(metadata)
	if err != nil || !ok {
		return err
	}
	if binding.EndUser != "" {
		response["end_user"] = binding.EndUser
	}
	if len(binding.Labels) > 0 {
		response["labels"] = binding.Labels
	}
	return nil
}

const endUserUsageDefaultLimit = 100
const endUserUsageMaxLimit = 1000

// endUserUsageRow is one end user's usage for a day: the projection of real
// provider receipts (UsageScopeTotal), never an estimate made here.
type endUserUsageRow struct {
	EndUser  string                      `json:"end_user"`
	Recorded bool                        `json:"recorded"`
	Usage    pebblestore.UsageScopeTotal `json:"usage"`
}

// handleSessionsV3EndUserUsage is GET /v3/sessions:end-user-usage: one UTC
// day's model usage of an agent's sessions, grouped by end user (query
// end_user= for one). A gateway token reads its own agent; the owner names
// agent_name. Sessions without an end_user are not in it.
func (s *Server) handleSessionsV3EndUserUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScopeAny(w, r, "usage:read", "usage:write", "sessions:read") {
		return
	}
	query := r.URL.Query()
	agent := strings.TrimSpace(query.Get("agent_name"))
	if rec, scoped := ScopedTokenFromRequest(r); scoped && strings.TrimSpace(rec.AgentName) != "" {
		if agent != "" && agent != strings.TrimSpace(rec.AgentName) {
			writeError(w, http.StatusForbidden, errors.New("a gateway token reads only its own agent's usage"))
			return
		}
		agent = strings.TrimSpace(rec.AgentName)
	}
	if agent == "" {
		writeError(w, http.StatusBadRequest, errors.New("agent_name is required"))
		return
	}
	date := strings.TrimSpace(query.Get("date"))
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if parsed, err := time.Parse("2006-01-02", date); err != nil || parsed.Format("2006-01-02") != date {
		writeError(w, http.StatusBadRequest, errors.New("date must be YYYY-MM-DD (UTC)"))
		return
	}
	store := s.sessions.Store()
	if endUser := query.Get("end_user"); endUser != "" {
		if err := pebblestore.ValidateEndUser(endUser); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		total, recorded, err := store.GetUsageScopeDay(principal.AccountScopeID, pebblestore.UsageScopeKindEndUser, agent, pebblestore.EndUserScopeID(endUser), date)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_name": agent, "date": date, "end_users": []endUserUsageRow{{EndUser: endUser, Recorded: recorded, Usage: total}}, "truncated": false})
		return
	}
	limit := endUserUsageDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > endUserUsageMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Errorf("limit must be 1-%d", endUserUsageMaxLimit))
			return
		}
		limit = parsed
	}
	totals, truncated, err := store.ListUsageScopeDays(principal.AccountScopeID, pebblestore.UsageScopeKindEndUser, agent, date, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]endUserUsageRow, 0, len(totals))
	for _, total := range totals {
		endUser, err := pebblestore.EndUserFromScopeID(total.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		rows = append(rows, endUserUsageRow{EndUser: endUser, Recorded: true, Usage: total})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_name": agent, "date": date, "end_users": rows, "truncated": truncated})
}
