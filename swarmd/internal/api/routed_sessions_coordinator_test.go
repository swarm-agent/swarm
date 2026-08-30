package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
)

func TestRoutedSessionCoordinatorRejectsBeforeRouter(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording"}
	server, principal, _ := newSessionRouterTestServer(t, runner, []sessionRouterWorkspace{{"/workspace/sole", "Sole", "Sole workspace"}})

	tests := []struct {
		name string
		body string
	}{
		{name: "missing input", body: `{"client_request_id":"request"}`},
		{name: "missing idempotency", body: `{"input":"route this"}`},
		{name: "duplicate staging", body: `{"input":"route this","client_request_id":"request","staging_ids":["staged","staged"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, RoutedSessionsPath, strings.NewReader(test.body))
			request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
			response := httptest.NewRecorder()
			server.handleRoutedSessionStart(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	if runner.createCalls != 0 || runner.streamingCalls != 0 {
		t.Fatalf("invalid requests reached Router: create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}
}

func TestRoutedSessionCoordinatorRouteRegistrationAndMethod(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, RoutedSessionsPath, nil)
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", AccountScopeSource: identity.AccountScopeSourceServerState}))
	response := httptest.NewRecorder()
	server.apiMux().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("registered route status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestNormalizeRoutedSessionMediaCanonicalizesAndBounds(t *testing.T) {
	media, ids, err := normalizeRoutedSessionMedia(routedSessionStartRequest{Media: []routedSessionMediaRequest{{StagingID: " staged ", Modality: " IMAGE ", FileType: ".PNG"}}})
	if err != nil {
		t.Fatalf("normalize media: %v", err)
	}
	if len(media) != 1 || media[0].StagingID != "staged" || media[0].Modality != "image" || media[0].FileType != "png" || len(ids) != 1 || ids[0] != "staged" {
		t.Fatalf("media=%+v ids=%v", media, ids)
	}
}

func TestRoutedSessionRequestHashBindsPayload(t *testing.T) {
	base := routedSessionStartRequest{Input: "route this", AgentName: "swarm", Metadata: map[string]any{"source": "desktop"}, WorkspacePath: "/source", HostWorkspacePath: "/source", RuntimeWorkspacePath: "/runtime", WorkspaceBindingID: "binding", SwarmID: "runtime", TargetKind: "host", TargetRelationship: "self"}
	binding := sessionsV3PrimaryBinding{RuntimeSwarmID: "runtime", WorkspaceBindingID: "binding", SourceWorkspaceID: "workspace", SourceWorkspaceGeneration: 2, SourceWorkspacePath: "/source", RuntimeWorkspacePath: "/runtime", PlacementGeneration: 3, BindingGeneration: 4}
	first, err := routedSessionRequestHash(base, binding, "request", nil)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second := base
	second.Input = "route something else"
	secondHash, err := routedSessionRequestHash(second, binding, "request", nil)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first == secondHash {
		t.Fatal("request hash did not bind routed input")
	}
	planRequested := true
	second = base
	second.PlanModeRequested = &planRequested
	planHash, err := routedSessionRequestHash(second, binding, "request", nil)
	if err != nil {
		t.Fatalf("Plan hash: %v", err)
	}
	if first == planHash {
		t.Fatal("request hash did not bind plan_mode_requested")
	}
	changedBinding := binding
	changedBinding.WorkspaceBindingID = "other-binding"
	bindingHash, err := routedSessionRequestHash(base, changedBinding, "request", nil)
	if err != nil {
		t.Fatalf("binding hash: %v", err)
	}
	if first == bindingHash {
		t.Fatal("request hash did not bind canonical workspace authority")
	}
	changedRequestPath := base
	changedRequestPath.WorkspacePath = "/other-source"
	pathHash, err := routedSessionRequestHash(changedRequestPath, binding, "request", nil)
	if err != nil {
		t.Fatalf("path hash: %v", err)
	}
	if first == pathHash {
		t.Fatal("request hash did not bind source workspace path")
	}
	changedRequestSwarm := base
	changedRequestSwarm.SwarmID = "other-runtime"
	swarmHash, err := routedSessionRequestHash(changedRequestSwarm, binding, "request", nil)
	if err != nil {
		t.Fatalf("swarm hash: %v", err)
	}
	if first == swarmHash {
		t.Fatal("request hash did not bind runtime swarm authority")
	}
}
