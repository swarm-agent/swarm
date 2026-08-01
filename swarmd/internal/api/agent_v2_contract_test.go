package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAgentV2RejectsRemovedSplitModelFields(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{agents: agentruntime.NewService(pebblestore.NewAgentStore(store), events)}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-one", AccountScopeID: "account-one"}
	for _, field := range []string{"model_mode", "plan_provider", "plan_model", "plan_thinking", "plan_service_tier", "auto_provider", "auto_model", "auto_thinking", "auto_service_tier"} {
		body := `{"mode":"subagent","prompt":"test","runtime_mode":"read","tool_contract":{"preset":"read_only"},"` + field + `":"removed"}`
		req := httptest.NewRequest(http.MethodPut, "/v2/agents/custom", strings.NewReader(body))
		req = req.WithContext(identity.ContextWithPrincipal(req.Context(), principal))
		res := httptest.NewRecorder()
		server.handleAgentByNameV2(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("field %q status = %d, want %d: %s", field, res.Code, http.StatusBadRequest, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "unknown field") {
			t.Fatalf("field %q error = %s, want unknown field", field, res.Body.String())
		}
	}
}
