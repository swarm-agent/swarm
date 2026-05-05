package deploy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newAlwaysOnTestService(t *testing.T) (*Service, *pebblestore.DeployContainerStore, *pebblestore.SwarmLocalContainerStore, *localcontainers.Service, string) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	configPath := filepath.Join(t.TempDir(), "swarm.conf")
	localStore := pebblestore.NewSwarmLocalContainerStore(store)
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	localSvc := localcontainers.NewService(localStore, deploymentStore, nil, nil, nil, configPath)
	deploySvc := NewService(deploymentStore, localSvc, nil, nil, nil, nil, nil, configPath)
	return deploySvc, deploymentStore, localStore, localSvc, configPath
}

func TestRecoverLocalDeploymentsOnlyEnsuresAlwaysOnAttachedDeployments(t *testing.T) {
	deploySvc, deploymentStore, localStore, localSvc, _ := newAlwaysOnTestService(t)
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ready.Close)

	started := make([]string, 0, 1)
	localSvc.SetControlContainerFuncForTest(func(_ context.Context, runtimeName, action, containerName string) error {
		started = append(started, fmt.Sprintf("%s:%s:%s", runtimeName, action, containerName))
		return nil
	})
	localSvc.SetInspectContainerFuncForTest(func(_, containerName string) (string, string, error) {
		if containerName == "always-on-child" && len(started) == 0 {
			return "exited", "runtime-" + containerName, nil
		}
		return "running", "runtime-" + containerName, nil
	})

	records := []pebblestore.DeployContainerRecord{
		{
			ID:              "always-on-child",
			Kind:            "container",
			Name:            "Always On Child",
			Status:          "stopped",
			Runtime:         "podman",
			ContainerName:   "always-on-child",
			BackendHostPort: 1234,
			ChildBackendURL: ready.URL,
			AttachStatus:    "attached",
			AlwaysOn:        true,
		},
		{
			ID:              "manual-child",
			Kind:            "container",
			Name:            "Manual Child",
			Status:          "stopped",
			Runtime:         "podman",
			ContainerName:   "manual-child",
			BackendHostPort: 5678,
			ChildBackendURL: ready.URL,
			AttachStatus:    "attached",
			AlwaysOn:        false,
		},
	}
	for _, record := range records {
		if _, err := deploymentStore.Put(record); err != nil {
			t.Fatalf("put deployment %s: %v", record.ID, err)
		}
		if _, err := localStore.Put(pebblestore.SwarmLocalContainerRecord{
			ID:             record.ID,
			Name:           record.Name,
			ContainerName:  record.ContainerName,
			Runtime:        record.Runtime,
			Status:         "exited",
			HostAPIBaseURL: ready.URL,
			HostPort:       record.BackendHostPort,
		}); err != nil {
			t.Fatalf("put local container %s: %v", record.ID, err)
		}
	}

	if err := deploySvc.RecoverLocalDeployments(context.Background()); err != nil {
		t.Fatalf("RecoverLocalDeployments() error = %v", err)
	}
	if len(started) != 1 || started[0] != "podman:start:always-on-child" {
		t.Fatalf("started = %#v, want only always-on child", started)
	}
	manual, _, err := deploymentStore.Get("manual-child")
	if err != nil {
		t.Fatalf("get manual deployment: %v", err)
	}
	if manual.Status != "stopped" {
		t.Fatalf("manual status = %q, want stopped", manual.Status)
	}
}

func TestEnsureRunningWaitsForAPIReadiness(t *testing.T) {
	deploySvc, deploymentStore, localStore, localSvc, _ := newAlwaysOnTestService(t)
	readyCalls := 0
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		readyCalls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ready.Close)

	localSvc.SetControlContainerFuncForTest(func(context.Context, string, string, string) error { return nil })
	localSvc.SetInspectContainerFuncForTest(func(_, containerName string) (string, string, error) {
		return "running", "runtime-" + containerName, nil
	})

	record := pebblestore.DeployContainerRecord{
		ID:              "ready-child",
		Kind:            "container",
		Name:            "Ready Child",
		Status:          "stopped",
		Runtime:         "podman",
		ContainerName:   "ready-child",
		BackendHostPort: 1234,
		ChildBackendURL: ready.URL,
		AttachStatus:    "attached",
		AlwaysOn:        true,
	}
	if _, err := deploymentStore.Put(record); err != nil {
		t.Fatalf("put deployment: %v", err)
	}
	if _, err := localStore.Put(pebblestore.SwarmLocalContainerRecord{
		ID:             record.ID,
		Name:           record.Name,
		ContainerName:  record.ContainerName,
		Runtime:        record.Runtime,
		Status:         "exited",
		HostAPIBaseURL: ready.URL,
		HostPort:       record.BackendHostPort,
	}); err != nil {
		t.Fatalf("put local container: %v", err)
	}

	deployment, err := deploySvc.EnsureRunning(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("EnsureRunning() error = %v", err)
	}
	if deployment.Status != "running" {
		t.Fatalf("deployment status = %q, want running", deployment.Status)
	}
	if readyCalls == 0 {
		t.Fatalf("readiness endpoint was not called")
	}
}
