package run

import (
	"context"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: allocateManagedDesignerArtifactV3 emits server-owned ordered
// membership before children start. A fake coordinator isolates the threat of
// conflating independent calls or attaching non-Designer launches to the wave.
func TestArtifactV3GenerationAllocation(t *testing.T) {
	coordinator := &artifactV3CoordinatorFake{}
	runtime := tool.NewRuntime(1)
	runtime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), coordinator, nil, nil))
	svc := &Service{tools: runtime}
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}
	specs := []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "same"}, {RequestedSubagentType: "finder"}, {RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "same"}}
	for _, call := range []string{"call-a", "call-b", "call-a"} {
		if _, err := svc.allocateManagedDesignerArtifactV3(context.Background(), parent, call, specs); err != nil {
			t.Fatal(err)
		}
	}
	got := coordinator.prepared
	if len(got) != 6 {
		t.Fatal(len(got))
	}
	for i := 0; i < 6; i += 2 {
		if got[i].GenerationWaveID == "" || got[i].GenerationWaveID != got[i+1].GenerationWaveID || got[i].GenerationIndex != 1 || got[i+1].GenerationIndex != 2 || got[i].GenerationCount != 2 || got[i].TaskCallID == got[i+1].TaskCallID {
			t.Fatal("incorrect independent-root allocation")
		}
	}
	if got[0].GenerationWaveID == got[2].GenerationWaveID || got[0].GenerationWaveID != got[4].GenerationWaveID {
		t.Fatal("wave collision or unstable retry")
	}
}
