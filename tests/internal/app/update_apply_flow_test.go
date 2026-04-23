package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/launcher"
)

type shutdownRecorder struct {
	shutdownCalls  int
	shutdownErr    error
	shutdownReason string
}

func (s *shutdownRecorder) Shutdown(ctx context.Context, reason string) error {
	s.shutdownCalls++
	s.shutdownReason = reason
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("shutdown context missing deadline")
	}
	return s.shutdownErr
}

func TestRunPendingUpdateRequiresBackendShutdownBeforeHelper(t *testing.T) {
	a := newCommandTestApp()
	stub := &shutdownRecorder{}
	a.pendingUpdate = &pendingUpdateRequest{
		plan: client.UpdateApplyPlan{TargetVersion: "v1.2.3"},
		lane: "main",
	}

	originalLoadProfile := loadRuntimeProfileForUpdate
	originalShutdown := updateShutdownFunc
	originalHelper := runUpdateHelperForegroundFunc
	defer func() {
		loadRuntimeProfileForUpdate = originalLoadProfile
		updateShutdownFunc = originalShutdown
		runUpdateHelperForegroundFunc = originalHelper
	}()

	loadRuntimeProfileForUpdate = func(string) (launcher.Profile, error) { return launcher.Profile{}, nil }
	updateShutdownFunc = func(_ *client.API, ctx context.Context, reason string) error {
		return stub.Shutdown(ctx, reason)
	}
	helperCalled := 0
	runUpdateHelperForegroundFunc = func(_ launcher.Profile, _ client.UpdateApplyPlan, _ []string) error {
		helperCalled++
		if stub.shutdownCalls != 1 {
			return errors.New("helper ran before shutdown")
		}
		return nil
	}

	if err := a.runPendingUpdate(); err != nil {
		t.Fatalf("runPendingUpdate: %v", err)
	}
	if stub.shutdownCalls != 1 {
		t.Fatalf("shutdownCalls = %d, want 1", stub.shutdownCalls)
	}
	if stub.shutdownReason != "swarmtui update apply" {
		t.Fatalf("shutdownReason = %q", stub.shutdownReason)
	}
	if helperCalled != 1 {
		t.Fatalf("helperCalled = %d, want 1", helperCalled)
	}
}

func TestRunPendingUpdateStopsWhenBackendShutdownFails(t *testing.T) {
	a := newCommandTestApp()
	stub := &shutdownRecorder{shutdownErr: errors.New("boom")}
	a.pendingUpdate = &pendingUpdateRequest{
		plan: client.UpdateApplyPlan{TargetVersion: "v1.2.3"},
		lane: "main",
	}

	originalLoadProfile := loadRuntimeProfileForUpdate
	originalShutdown := updateShutdownFunc
	originalHelper := runUpdateHelperForegroundFunc
	defer func() {
		loadRuntimeProfileForUpdate = originalLoadProfile
		updateShutdownFunc = originalShutdown
		runUpdateHelperForegroundFunc = originalHelper
	}()

	loadRuntimeProfileForUpdate = func(string) (launcher.Profile, error) { return launcher.Profile{}, nil }
	updateShutdownFunc = func(_ *client.API, ctx context.Context, reason string) error {
		return stub.Shutdown(ctx, reason)
	}
	helperCalled := 0
	runUpdateHelperForegroundFunc = func(_ launcher.Profile, _ client.UpdateApplyPlan, _ []string) error {
		helperCalled++
		return nil
	}

	err := a.runPendingUpdate()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("runPendingUpdate error = %v, want boom", err)
	}
	if helperCalled != 0 {
		t.Fatalf("helperCalled = %d, want 0", helperCalled)
	}
}

func TestRunPendingUpdateShutdownUsesTimeoutContext(t *testing.T) {
	a := newCommandTestApp()
	a.pendingUpdate = &pendingUpdateRequest{
		plan: client.UpdateApplyPlan{TargetVersion: "v1.2.3"},
		lane: "main",
	}

	originalLoadProfile := loadRuntimeProfileForUpdate
	originalShutdown := updateShutdownFunc
	originalHelper := runUpdateHelperForegroundFunc
	defer func() {
		loadRuntimeProfileForUpdate = originalLoadProfile
		updateShutdownFunc = originalShutdown
		runUpdateHelperForegroundFunc = originalHelper
	}()

	loadRuntimeProfileForUpdate = func(string) (launcher.Profile, error) { return launcher.Profile{}, nil }
	updateShutdownFunc = func(_ *client.API, ctx context.Context, reason string) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("shutdown context missing deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 6*time.Second {
			t.Fatalf("shutdown timeout remaining = %v, want about 5s", remaining)
		}
		return nil
	}
	runUpdateHelperForegroundFunc = func(_ launcher.Profile, _ client.UpdateApplyPlan, _ []string) error { return nil }

	if err := a.runPendingUpdate(); err != nil {
		t.Fatalf("runPendingUpdate: %v", err)
	}
}

func TestRunPendingUpdatePrintsConditionalRestartMessage(t *testing.T) {
	a := newCommandTestApp()
	a.pendingUpdate = &pendingUpdateRequest{
		plan: client.UpdateApplyPlan{TargetVersion: "v1.2.3"},
		lane: "main",
	}

	originalLoadProfile := loadRuntimeProfileForUpdate
	originalShutdown := updateShutdownFunc
	originalHelper := runUpdateHelperForegroundFunc
	originalStdout := os.Stdout
	defer func() {
		loadRuntimeProfileForUpdate = originalLoadProfile
		updateShutdownFunc = originalShutdown
		runUpdateHelperForegroundFunc = originalHelper
		os.Stdout = originalStdout
	}()

	loadRuntimeProfileForUpdate = func(string) (launcher.Profile, error) { return launcher.Profile{}, nil }
	updateShutdownFunc = func(_ *client.API, _ context.Context, _ string) error { return nil }
	runUpdateHelperForegroundFunc = func(_ launcher.Profile, _ client.UpdateApplyPlan, _ []string) error { return nil }

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := a.runPendingUpdate()
	_ = w.Close()
	if runErr != nil {
		t.Fatalf("runPendingUpdate: %v", runErr)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stdout: %v", err)
	}
	_ = r.Close()

	output := buf.String()
	if !strings.Contains(output, "Swarm will attempt to restart automatically when the update finishes.") {
		t.Fatalf("stdout missing conditional restart line: %q", output)
	}
	if !strings.Contains(output, "If automatic restart is blocked, Swarm will tell you to restart it manually.") {
		t.Fatalf("stdout missing blocked restart line: %q", output)
	}
}
