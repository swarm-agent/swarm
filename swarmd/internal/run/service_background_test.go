package run

import "testing"

func TestBuildBackgroundRunMetadataUsesResolvedTargetIdentity(t *testing.T) {
	existing := map[string]any{
		"target_kind": "remote",
		"target_name": "stale target",
	}

	metadata := buildBackgroundRunMetadata(existing, "background", "memory", resolvedRunExecutionContext{WorkspacePath: "/workspaces/swarm-go"})

	if metadata["target_kind"] != "background" || metadata["target_name"] != "memory" {
		t.Fatalf("background target metadata = %+v", metadata)
	}
	if metadata["launch_mode"] != "background" || metadata["background"] != true {
		t.Fatalf("background metadata missing: %+v", metadata)
	}
}

func TestBuildBackgroundRunMetadataSetsTargetForOrdinaryBackgroundRuns(t *testing.T) {
	metadata := buildBackgroundRunMetadata(map[string]any{"source": "chat"}, "background", "memory", resolvedRunExecutionContext{WorkspacePath: "/workspace"})

	if metadata["target_kind"] != "background" || metadata["target_name"] != "memory" {
		t.Fatalf("ordinary background target metadata = %+v", metadata)
	}
}

func TestEffectiveRunOwnerTransportKeepsForegroundWebSocketNonBackground(t *testing.T) {
	service := &Service{}

	if got := service.effectiveRunOwnerTransport(RunOptions{OwnerTransport: "ws"}, func(StreamEvent) {}); got != "ws" {
		t.Fatalf("foreground websocket owner transport = %q, want ws", got)
	}
	if got := service.effectiveRunOwnerTransport(RunOptions{Background: true}, func(StreamEvent) {}); got != "background_api" {
		t.Fatalf("background websocket owner transport = %q, want background_api", got)
	}
}
