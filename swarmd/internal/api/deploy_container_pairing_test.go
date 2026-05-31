package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/stream"
)

type fakePairingAccountBindDeployService struct {
	fakeReplicateDeployService
	lastInput deployruntime.ContainerPairingAccountBindInput
}

func (f *fakePairingAccountBindDeployService) BindLocalPairingAccount(_ context.Context, input deployruntime.ContainerPairingAccountBindInput) error {
	f.lastInput = input
	return nil
}

func TestDeployContainerPairingAccountBindRequiresPeerAuth(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	server.SetDeployContainerService(&fakePairingAccountBindDeployService{})

	payload, err := json.Marshal(deployruntime.ContainerPairingAccountBindInput{HostSwarmID: "host-swarm", ChildSwarmID: "child-swarm", UserID: "user-1", AccountScopeID: "account-1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/pairing/account-bind", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestDeployContainerPairingAccountBindRejectsPeerHostMismatch(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	server.SetDeployContainerService(&fakePairingAccountBindDeployService{})

	payload, err := json.Marshal(deployruntime.ContainerPairingAccountBindInput{HostSwarmID: "other-host", ChildSwarmID: "child-swarm", UserID: "user-1", AccountScopeID: "account-1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/pairing/account-bind", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm"}))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestDeployContainerPairingAccountBindPassesPeerBoundIdentity(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	fakeDeploy := &fakePairingAccountBindDeployService{}
	server.SetDeployContainerService(fakeDeploy)

	payload, err := json.Marshal(deployruntime.ContainerPairingAccountBindInput{ChildSwarmID: "child-swarm", UserID: "user-1", AccountScopeID: "account-1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/container/pairing/account-bind", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm"}))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if fakeDeploy.lastInput.HostSwarmID != "host-swarm" || fakeDeploy.lastInput.ChildSwarmID != "child-swarm" || fakeDeploy.lastInput.UserID != "user-1" || fakeDeploy.lastInput.AccountScopeID != "account-1" {
		t.Fatalf("input = %#v", fakeDeploy.lastInput)
	}
}
