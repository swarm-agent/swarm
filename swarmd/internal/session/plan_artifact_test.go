package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanDocumentPreservesPortableArtifacts(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-artifacts", "Artifacts", &pebblestore.SessionPlanDocument{
		Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "docs/summary.md", Role: " DELIVERABLE ", Description: " user deliverable ", MediaType: " text/markdown "}},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliver", Status: PlanCheckpointStatusPending, Order: 1,
			Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "tmp/evidence.json", Description: "selective evidence"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("normalize plan artifacts: %v", err)
	}
	if got := doc.Artifacts[0]; got.Path != "docs/summary.md" || got.Role != "deliverable" || got.Description != "user deliverable" || got.MediaType != "text/markdown" {
		t.Fatalf("plan artifact = %#v", got)
	}
	if got := doc.Checkpoints[0].Artifacts[0].Path; got != "tmp/evidence.json" {
		t.Fatalf("checkpoint artifact path = %q", got)
	}
	clone := clonePlanDocument(doc)
	clone.Artifacts[0].Path = "changed.md"
	clone.Checkpoints[0].Artifacts[0].Path = "changed.json"
	if doc.Artifacts[0].Path != "docs/summary.md" || doc.Checkpoints[0].Artifacts[0].Path != "tmp/evidence.json" {
		t.Fatalf("artifact slices were not cloned: %#v", doc)
	}
}

func TestNormalizePlanDocumentRejectsNonPortableArtifacts(t *testing.T) {
	for _, artifactPath := range []string{"/tmp/result.md", "C:/tmp/result.md", `C:\\tmp\\result.md`, "../result.md", "docs/../result.md"} {
		t.Run(strings.ReplaceAll(artifactPath, "/", "_"), func(t *testing.T) {
			_, err := NormalizePlanDocumentForSave("plan-artifacts", "Artifacts", &pebblestore.SessionPlanDocument{
				Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: artifactPath}},
			}, nil)
			if err == nil || (!strings.Contains(err.Error(), "portable workspace-relative") && !strings.Contains(err.Error(), "within the workspace") && !strings.Contains(err.Error(), "clean portable")) {
				t.Fatalf("path %q error = %v", artifactPath, err)
			}
		})
	}
}

func TestValidateExecutablePlanDocumentRejectsEscapingCheckpointArtifact(t *testing.T) {
	err := ValidateExecutablePlanDocument(&pebblestore.SessionPlanDocument{
		Title: "Artifact plan",
		Info:  pebblestore.SessionPlanInfo{Goal: "Deliver artifact"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Deliver", Status: PlanCheckpointStatusPending, Order: 1,
			Objective: "Deliver", AcceptanceCriteria: []string{"Delivered"},
			Artifacts: []pebblestore.SessionPlanArtifactReference{{Path: "../../secret"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoints[0].artifacts[0].path") {
		t.Fatalf("executable artifact validation error = %v", err)
	}
}
