package run_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runpkg "swarm/packages/swarmd/internal/run"
	sandboxruntime "swarm/packages/swarmd/internal/sandbox"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type sandboxProbeCounter struct {
	enabled        bool
	status         sandboxruntime.Status
	isEnabledCalls int
	getStatusCalls int
}

func (s *sandboxProbeCounter) IsEnabled() (bool, error) {
	s.isEnabledCalls++
	return s.enabled, nil
}

func (s *sandboxProbeCounter) GetStatus() (sandboxruntime.Status, error) {
	s.getStatusCalls++
	return s.status, nil
}

func TestRunTurnStreamingSkipsSandboxStatusPreflightWhenSandboxDisabled(t *testing.T) {
	svc, sessionID := newSandboxRunTestService(t)
	sandboxSvc := &sandboxProbeCounter{enabled: false}
	svc.SetSandboxService(sandboxSvc)

	if _, err := svc.RunTurnStreaming(context.Background(), sessionID, runpkg.RunRequest{
		Prompt: "hello",
	}, runpkg.RunStartMeta{
		RunID: "run-disabled-sandbox",
	}, nil); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if sandboxSvc.isEnabledCalls != 1 {
		t.Fatalf("IsEnabled calls = %d, want 1", sandboxSvc.isEnabledCalls)
	}
	if sandboxSvc.getStatusCalls != 0 {
		t.Fatalf("GetStatus calls = %d, want 0", sandboxSvc.getStatusCalls)
	}
}

func TestRunTurnStreamingChecksSandboxStatusWhenSandboxEnabled(t *testing.T) {
	svc, sessionID := newSandboxRunTestService(t)
	sandboxSvc := &sandboxProbeCounter{
		enabled: true,
		status: sandboxruntime.Status{
			Enabled: true,
			Ready:   false,
			Summary: "probe failed",
		},
	}
	svc.SetSandboxService(sandboxSvc)

	_, err := svc.RunTurnStreaming(context.Background(), sessionID, runpkg.RunRequest{
		Prompt: "hello",
	}, runpkg.RunStartMeta{
		RunID: "run-enabled-sandbox",
	}, nil)
	if err == nil {
		t.Fatal("run turn succeeded, want sandbox readiness error")
	}
	if !strings.Contains(err.Error(), "sandbox is ON but unavailable: probe failed") {
		t.Fatalf("error = %q, want sandbox readiness failure", err.Error())
	}
	if sandboxSvc.isEnabledCalls != 1 {
		t.Fatalf("IsEnabled calls = %d, want 1", sandboxSvc.isEnabledCalls)
	}
	if sandboxSvc.getStatusCalls != 1 {
		t.Fatalf("GetStatus calls = %d, want 1", sandboxSvc.getStatusCalls)
	}
}

func newSandboxRunTestService(t *testing.T) (*runpkg.Service, string) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sandbox-run.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	modelSvc := model.NewService(pebblestore.NewModelStore(store), events, nil)
	if _, _, err := modelSvc.SetGlobalPreference("codex", "gpt-5.4", "high"); err != nil {
		t.Fatalf("set global preference: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "sandbox",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "high",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	runner := &staticRunner{
		id: "codex",
		responses: []provideriface.Response{
			{Text: "ok"},
		},
	}
	providers := registry.New()
	providers.RegisterRunner(runner)

	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure default agents: %v", err)
	}

	return runpkg.NewService(sessionSvc, modelSvc, providers, tool.NewRuntime(1), nil, agentSvc, nil, events), session.ID
}
