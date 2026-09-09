package run

import (
	"context"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: three Designer alternatives preserve explicit source intent;
// malformed later launch cannot allocate earlier capabilities. This coordinator
// boundary test observes requests, not provider behavior or rendered fidelity.
func TestArtifactV3IntentWavePreflight(t *testing.T) {
	c := &artifactV3CoordinatorFake{}
	r := tool.NewRuntime(1)
	r.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), c, nil, nil))
	s := &Service{tools: r}
	parent := pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}
	specs := make([]taskLaunchSpec, 3)
	for i := range specs {
		specs[i] = taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "remix", ArtifactV3Source: &taskArtifactV3Source{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 7, RevisionIntent: pebblestore.ArtifactV3RevisionWholeProject}}
	}
	specs[2].ArtifactV3Source.TargetPartIDs = []string{"hero"}
	if _, err := s.allocateManagedDesignerArtifactV3(context.Background(), parent, "invalid", specs); err == nil || len(c.prepared) != 0 {
		t.Fatalf("partial allocation: %v %d", err, len(c.prepared))
	}
	specs[2].ArtifactV3Source.TargetPartIDs = nil
	if _, err := s.allocateManagedDesignerArtifactV3(context.Background(), parent, "valid", specs); err != nil {
		t.Fatal(err)
	}
	if len(c.prepared) != 3 {
		t.Fatal(len(c.prepared))
	}
	for i, p := range c.prepared {
		if p.RevisionIntent != pebblestore.ArtifactV3RevisionWholeProject || p.CandidateIndex != i+1 || p.ProjectionSeq != 7 || p.Initial {
			t.Fatalf("request=%+v", p)
		}
	}
	var b strings.Builder
	writeDesignerAnimationGuidance(&b, "motion_ui")
	for _, bad := range []string{"Artifact V2", "artifact_v2_author", "Do not emit HTML"} {
		if strings.Contains(b.String(), bad) {
			t.Fatal(bad)
		}
	}
}
