package run

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: whole-artifact selections admitted with nil optional Part IDs
// must bind all five Designer alternatives to the exact native source. Threat:
// treating optional intent as authentication rejects valid whole-project work.
// Exercise the production parser, message binding and allocation boundary; only
// the author coordinator is fake. Actual owner/head admission is tested in store.
func TestTaskNativeWholeSelectionDispatch(t *testing.T) {
	for _, targetCase := range []string{"omitted", "empty", "selected"} {
		for _, explicit := range []bool{false, true} {
			t.Run(targetCase+map[bool]string{false: "/implicit", true: "/explicit"}[explicit], func(t *testing.T) {
				selection := pebblestore.SessionArtifactSelectionReference{
					SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, Action: "use",
				}
				selection.RevisionRef = "revision-" + selection.CommitOID
				var ids []string
				if targetCase == "selected" {
					ids = []string{"opening", "resolve"}
				}
				if targetCase != "omitted" {
					selection.TargetPartIDs = &ids
				}
				args := map[string]any{"mode": "swarm", "agent_type": "designer", "count": 5, "prompt": "Animate the complete story in five styles"}
				if explicit {
					args["artifact_v3_source"] = &taskArtifactV3Source{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, CommitOID: selection.CommitOID, ProjectionSeq: selection.ProjectionSeq}
				}
				body, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := parseTaskCallArguments(string(body))
				if err != nil {
					t.Fatal(err)
				}
				selected := latestTaskArtifactUseSelection([]pebblestore.MessageSnapshot{{Role: "user", ArtifactSelections: []pebblestore.SessionArtifactSelectionReference{selection}}})
				if err := bindTaskNativeArtifactSelection(&parsed, parsed.Launches, selected); err != nil {
					t.Fatal(err)
				}
				if len(parsed.Launches) != 5 || parsed.SourceArtifact != nil || parsed.ArtifactV2Source != nil || !reflect.DeepEqual(parsed.Swarm.ArtifactV3Source, parsed.ArtifactV3Source) {
					t.Fatal("whole-wave source binding lost")
				}
				coordinator := &artifactV3CoordinatorFake{}
				runtime := tool.NewRuntime(1)
				runtime.SetArtifactV3AuthorService(tool.NewArtifactV3AuthorService(t.TempDir(), coordinator, nil, nil))
				svc := &Service{tools: runtime}
				contexts, err := svc.allocateManagedDesignerArtifactV3(context.Background(), pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "whole-wave", parsed.Launches)
				if err != nil || len(contexts) != 5 || len(coordinator.prepared) != 5 {
					t.Fatalf("allocation count: contexts=%d prepared=%d err=%v", len(contexts), len(coordinator.prepared), err)
				}
				for i, request := range coordinator.prepared {
					if request.Initial || request.ArtifactID != selection.ArtifactID || request.BaseCommitOID != selection.CommitOID || request.ProjectionSeq != selection.ProjectionSeq || !equalStringSet(request.TargetPartIDs, ids) || request.CandidateIndex != i+1 {
						t.Fatalf("candidate source lost: %+v", request)
					}
					if parsed.Launches[i].SourceArtifact != nil || !reflect.DeepEqual(parsed.Launches[i].ArtifactV3Source, parsed.ArtifactV3Source) {
						t.Fatal("launch source changed")
					}
				}
			})
		}
	}
}

// Requirement: accepting nil optional Parts must not accept missing authenticated
// identity, mismatched explicit sources or added Part hints. Threat: a fix for
// whole-project intent could weaken authentication or partially mutate a wave.
// The shared permission/execution binding function is the narrowest rejection
// boundary; byte snapshots assert that neither an early nor late launch changes.
func TestTaskNativeWholeSelectionRejectsInvalidContextAtomically(t *testing.T) {
	for _, field := range []string{"session", "commit", "projection", "revision", "source-session", "source-artifact", "source-commit", "source-projection", "parts", "hint", "legacy"} {
		t.Run(field, func(t *testing.T) {
			selection := &pebblestore.SessionArtifactSelectionReference{SessionID: "parent", ArtifactID: "artifact", CommitOID: strings.Repeat("a", 40), ProjectionSeq: 9, Action: "use"}
			selection.RevisionRef = "revision-" + selection.CommitOID
			source := &taskArtifactV3Source{SessionID: selection.SessionID, ArtifactID: selection.ArtifactID, CommitOID: selection.CommitOID, ProjectionSeq: selection.ProjectionSeq}
			launch := taskLaunchSpec{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, ArtifactV3Source: source}
			switch field {
			case "session":
				selection.SessionID = ""
			case "commit":
				selection.CommitOID = ""
			case "projection":
				selection.ProjectionSeq = 0
			case "revision":
				selection.RevisionRef = "revision-" + strings.Repeat("b", 40)
			case "source-session":
				source.SessionID = "foreign"
			case "source-artifact":
				source.ArtifactID = "other"
			case "source-commit":
				source.CommitOID = strings.Repeat("b", 40)
			case "source-projection":
				source.ProjectionSeq++
			case "parts":
				source.TargetPartIDs = []string{"unselected"}
			case "hint":
				launch.SourceArguments = map[string]any{"section_target": &taskSwarmSectionTarget{ID: "unselected", Label: "Unselected", Kind: "semantic"}}
			case "legacy":
				launch.SourceArtifact = &pebblestore.SessionArtifactSelectionReference{CollectionID: "legacy"}
			}
			parsed := taskCallArguments{Launches: []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged}, launch}}
			before, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindTaskNativeArtifactSelection(&parsed, parsed.Launches, selection); err == nil {
				t.Fatal("invalid binding accepted")
			}
			after, err := json.Marshal(parsed)
			if err != nil || string(before) != string(after) {
				t.Fatal("rejection partially mutated wave")
			}
		})
	}
}
