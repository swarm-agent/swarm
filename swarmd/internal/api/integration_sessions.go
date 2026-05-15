package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/appstorage"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	integrationBuilderSessionSource = "integration_builder"
	integrationBuilderScope         = "swarm"
	integrationBuilderWorkspaceName = "Integrations"
	integrationBuilderWorkspacePart = "integrations"
)

type integrationSessionCreateRequest struct {
	Title      string                         `json:"title"`
	Mode       string                         `json:"mode"`
	AgentName  string                         `json:"agent_name"`
	Metadata   map[string]any                 `json:"metadata"`
	Preference integrationSessionPreferenceIn `json:"preference"`
}

type integrationSessionPreferenceIn struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

func (s *Server) handleIntegrationBuilderSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, ok := parsePositiveInt(raw)
			if !ok {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		scanLimit := 10000
		if limit > scanLimit {
			scanLimit = limit
		}
		sessions, err := s.sessions.ListSessions(scanLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		filtered := make([]pebblestore.SessionSnapshot, 0, len(sessions))
		for _, session := range sessions {
			if isIntegrationBuilderSession(session) {
				filtered = append(filtered, session)
				if len(filtered) >= limit {
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": filtered})
	case http.MethodPost:
		var req integrationSessionCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		workspacePath, err := integrationBuilderWorkspacePath()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		mode := sessionruntime.NormalizeMode(req.Mode)
		if strings.TrimSpace(req.Mode) == "" {
			mode = sessionruntime.ModePlan
		}
		metadata := mergeSessionCreateMetadata(integrationBuilderSessionMetadata(), req.Metadata)
		session, event, err := s.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
			Title:         firstNonEmpty(strings.TrimSpace(req.Title), "New integration"),
			WorkspacePath: workspacePath,
			WorkspaceName: integrationBuilderWorkspaceName,
			Mode:          mode,
			Preference: &pebblestore.ModelPreference{
				Provider:    strings.TrimSpace(req.Preference.Provider),
				Model:       strings.TrimSpace(req.Preference.Model),
				Thinking:    strings.TrimSpace(req.Preference.Thinking),
				ServiceTier: strings.TrimSpace(req.Preference.ServiceTier),
				ContextMode: strings.TrimSpace(req.Preference.ContextMode),
			},
			Metadata: metadata,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session})
	default:
		methodNotAllowed(w)
	}
}

func integrationBuilderWorkspacePath() (string, error) {
	return appstorage.DataDir("global-sessions", integrationBuilderWorkspacePart)
}

func integrationBuilderSessionMetadata() map[string]any {
	return map[string]any{
		"source":          integrationBuilderSessionSource,
		"session_source":  integrationBuilderSessionSource,
		"scope":           integrationBuilderScope,
		"workspace_scope": integrationBuilderScope,
		"title_pending":   true,
	}
}

func isIntegrationBuilderSession(session pebblestore.SessionSnapshot) bool {
	return sessionMetadataEquals(session.Metadata, "source", integrationBuilderSessionSource) ||
		sessionMetadataEquals(session.Metadata, "session_source", integrationBuilderSessionSource)
}

func sessionMetadataEquals(metadata map[string]any, key, want string) bool {
	value, ok := metadata[key]
	if !ok {
		return false
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(want))
}

func parsePositiveInt(raw string) (int, bool) {
	value := 0
	for _, ch := range strings.TrimSpace(raw) {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		value = value*10 + int(ch-'0')
	}
	return value, value > 0
}
