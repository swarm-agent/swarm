package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Purpose: indexed accounting must not expose projections without an authenticated
// principal. Boundary: handleUsageScope. Direct HTTP handler invocation proves
// fail-closed rejection without constructing an unrelated executor or provider.
func TestUsageScopeRequiresPrincipal(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.handleUsageScope(response, httptest.NewRequest(http.MethodGet, "/v3/usage/scope?kind=worker&id=worker", nil))
	if response.Code != http.StatusUnauthorized { t.Fatalf("status = %d", response.Code) }
}
