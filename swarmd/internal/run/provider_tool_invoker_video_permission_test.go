package run

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: ordinary chat must reach manage_video without a durable approval
// record before its first Studio proposal. Exercise the real provider invoker and
// permission store with bypass disabled. The deliberately unconfigured video
// runtime must still reject execution, proving this is not an authority bypass.
func TestProviderManagedChatVideoSkipsBootstrapPermission(t *testing.T) {
	workspace := t.TempDir()
	svc, sid, permissions, cleanup := newProviderManagedV3PermissionTestService(t, workspace)
	defer cleanup()
	invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
		sessionID: sid, permissionSessionID: sid, runID: "video-permission-regression", step: 1,
		sessionMode: sessionruntime.ModeAuto, workspacePath: workspace, workspaceRoots: []string{workspace},
		workspaceOriginPath: workspace, workspaceOriginRoots: []string{workspace}, workspaceName: "workspace",
		applySessionMutation: providerManagedV3NoopMutation, providerManagedV3: true,
	})
	for i, action := range tool.ManageVideoActionNames(true) {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := invoker.ExecuteTool(ctx, toolInvocation(fmt.Sprintf("video-%d", i), "manage_video", fmt.Sprintf(`{"action":%q}`, action)))
			if err != nil {
				t.Fatalf("invoker failed: %v", err)
			}
			if result.PermissionWaitMS != 0 {
				t.Fatalf("permission wait: %d", result.PermissionWaitMS)
			}
			if strings.Contains(strings.ToLower(result.Error+result.Output), "permission") {
				t.Fatalf("unexpected approval result: %+v", result)
			}
			if result.Error == "" {
				t.Fatal("unconfigured video authority unexpectedly succeeded")
			}
		})
	}
	pending, err := permissions.ListPending(sid, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("created %d permission records", len(pending))
	}
}
