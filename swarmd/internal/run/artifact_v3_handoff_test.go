package run

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Requirement: regular and swarm task results expose native Git references,
// retain independently successful candidates on partial failure, and never call
// a pending base a completed revision. Threat: V1 aliases invite invalid follow-up
// calls or fabricate readiness. This serialization boundary is narrower than a
// provider journey and exercises the production stream/result collector.
func TestArtifactV3TaskHandoffNativeReferencesAndPartialFailure(t *testing.T) {
	grant := tool.ArtifactV3AuthorGrant{OwnerSessionID: "parent", ArtifactID: "artifact", TurnID: "turn", CandidateID: "one", BaseCommitOID: strings.Repeat("a", 40)}
	pending := buildTaskLaunchOutcome(taskLaunchPrepared{ArtifactV3AuthorContext: &tool.ArtifactV3AuthorRunContext{Grant: grant}})
	if pending.ArtifactReference.CommitOID != "" || pending.ArtifactReference.ArtifactID != grant.ArtifactID {
		t.Fatalf("pending slot fabricated revision: %+v", pending.ArtifactReference)
	}
	ready := taskArtifactV3Reference(grant, strings.Repeat("b", 40), 42, "ready")
	failed := pending
	failed.ArtifactReference.Status = "failed"
	failed.ArtifactReference.FailureCode = "child_run_failed"
	outcomes := []taskLaunchOutcome{{ArtifactReference: ready}, failed}
	refs := collectTaskReadyArtifactReferences(outcomes, []error{nil, errors.New("injected child failure")})
	if len(refs) != 1 || refs[0] != ready || failed.ArtifactReference.FailureCode != "child_run_failed" {
		t.Fatalf("partial wave lost candidate or failure: %+v", refs)
	}
	for _, reference := range []*taskArtifactReference{ready, failed.ArtifactReference} {
		payload := buildTaskStreamLaunchPayload(taskLaunchOutcome{ArtifactReference: reference}, reference.Status, "completed", true)
		raw, err := json.Marshal(payload["artifact_reference"])
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"collection_id", "variant_id", "event_seq", "source_artifact", "artifact_v2"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("native handoff leaked %s: %s", forbidden, raw)
			}
		}
		var decoded taskArtifactReference
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ArtifactID != grant.ArtifactID || decoded.CandidateID != grant.CandidateID || decoded.Status != reference.Status {
			t.Fatalf("identity lost: %s", raw)
		}
	}
	// The four native source fields returned at readiness must feed the actual
	// task parser without translating collection/variant identities.
	args, _ := json.Marshal(map[string]any{"mode": "regular", "prompt": "revise", "subagent_type": "designer", "meta_prompt": "revise", "artifact_v3_source": map[string]any{"session_id": ready.SessionID, "artifact_id": ready.ArtifactID, "commit_oid": ready.CommitOID, "projection_seq": ready.ProjectionSeq}})
	parsed, err := parseTaskCallArguments(string(args))
	if err != nil || parsed.ArtifactV3Source.CommitOID != ready.CommitOID {
		t.Fatalf("native continuation: %+v %v", parsed, err)
	}
}

// Requirement: the runtime's model-visible task schema includes the exact native
// source CAS fields, and the Designer writer excludes destination authority.
// Threat: parser-only support leaves providers unable to express safe follow-ups.
func TestArtifactV3DesignerModelVisibleSchemas(t *testing.T) {
	definitions := tool.NewRuntime(1).Definitions()
	var taskFound, authorFound bool
	for _, definition := range definitions {
		properties, _ := definition.Parameters["properties"].(map[string]any)
		switch definition.Name {
		case "task":
			taskFound = true
			source, ok := properties["artifact_v3_source"].(map[string]any)
			if !ok {
				t.Fatal("model-visible native source missing")
			}
			required, _ := source["required"].([]string)
			if strings.Join(required, ",") != "session_id,artifact_id,commit_oid,projection_seq" || source["additionalProperties"] != false {
				t.Fatalf("source schema=%+v", source)
			}
		case "artifact_v3_author":
			authorFound = true
			if definition.Parameters["additionalProperties"] != false {
				t.Fatal("author schema is open")
			}
			for _, field := range []string{"artifact_id", "commit_oid", "destination", "policy", "build_command", "part_id"} {
				if _, ok := properties[field]; ok {
					t.Fatalf("caller authority %s exposed", field)
				}
			}
		}
	}
	if !taskFound || !authorFound {
		t.Fatalf("task=%v author=%v", taskFound, authorFound)
	}
}
