package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestFiveAgentsMultipartAnimationTargetedFlow(t *testing.T) {
	specs := []struct {
		ID            string
		Title         string
		DurationMS    int
		Parts         []map[string]any
		TargetPartID  string
		InitialHTML   string
		RevisedSnippet string
	}{
		{
			ID:         "agent-1-neural-boot",
			Title:      "Cyberpunk Neural Boot Pipeline",
			DurationMS: 9000,
			Parts: []map[string]any{
				{"id": "part-1", "label": "System Diagnostics", "kind": "temporal", "start_ms": 0, "end_ms": 3000},
				{"id": "part-2", "label": "Neural Convergence", "kind": "temporal", "start_ms": 3000, "end_ms": 6000},
				{"id": "part-3", "label": "Synthesis & Online", "kind": "temporal", "start_ms": 6000, "end_ms": 9000},
			},
			TargetPartID: "part-2",
			InitialHTML: `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script></head><body><main id="stage"><canvas id="canvas"></canvas></main></body></html>`,
			RevisedSnippet: `<canvas id="canvas" data-vortex="quantum"></canvas>`,
		},
		{
			ID:         "agent-2-orbital-telemetry",
			Title:      "Cybernetic Orbital Station Telemetry",
			DurationMS: 6000,
			Parts: []map[string]any{
				{"id": "nav-array", "label": "Navigation Array", "kind": "temporal", "start_ms": 0, "end_ms": 2000},
				{"id": "vector-display", "label": "Attitude Vector Display", "kind": "temporal", "start_ms": 2000, "end_ms": 4000},
				{"id": "reactor-core", "label": "Reactor Core Waveform", "kind": "temporal", "start_ms": 4000, "end_ms": 6000},
			},
			TargetPartID: "reactor-core",
			InitialHTML: `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":6000,"fps":60}</script></head><body><header id="nav-array"></header><main id="vector-display"></main><footer id="reactor-core"></footer></body></html>`,
			RevisedSnippet: `<footer id="reactor-core" class="dual-harmonic-plasma"></footer>`,
		},
		{
			ID:         "agent-3-threat-radar",
			Title:      "Automated Threat Interceptor Radar",
			DurationMS: 9000,
			Parts: []map[string]any{
				{"id": "surveillance", "label": "Passive Surveillance", "kind": "temporal", "start_ms": 0, "end_ms": 3000},
				{"id": "target-lock", "label": "Target Lock", "kind": "temporal", "start_ms": 3000, "end_ms": 6000},
				{"id": "intercept", "label": "Countermeasure Intercept", "kind": "temporal", "start_ms": 6000, "end_ms": 9000},
			},
			TargetPartID: "target-lock",
			InitialHTML: `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script></head><body><main id="radar-screen"><section id="surveillance"></section><section id="target-lock"></section><section id="intercept"></section></main></body></html>`,
			RevisedSnippet: `<section id="target-lock" class="hexagonal-targeting-mesh"></section>`,
		},
		{
			ID:         "agent-4-data-pipeline",
			Title:      "Distributed Event Stream Transmutation Engine",
			DurationMS: 8000,
			Parts: []map[string]any{
				{"id": "ingestion", "label": "Raw Ingestion Buffer", "kind": "temporal", "start_ms": 0, "end_ms": 2500},
				{"id": "sharding", "label": "Cryptographic Sharding Ring", "kind": "temporal", "start_ms": 2500, "end_ms": 5500},
				{"id": "commit", "label": "Immutable Ledger Commit", "kind": "temporal", "start_ms": 5500, "end_ms": 8000},
			},
			TargetPartID: "sharding",
			InitialHTML: `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":8000,"fps":60}</script></head><body><main id="pipeline"><div id="ingestion"></div><div id="sharding"></div><div id="commit"></div></main></body></html>`,
			RevisedSnippet: `<div id="sharding" data-nodes="8-constellation"></div>`,
		},
		{
			ID:         "agent-5-multimodel-consensus",
			Title:      "Multi-Model Consensus & Speculative Decoding Lattice",
			DurationMS: 9000,
			Parts: []map[string]any{
				{"id": "candidates", "label": "Candidate Generation", "kind": "temporal", "start_ms": 0, "end_ms": 3000},
				{"id": "consensus", "label": "Consensus Matrix", "kind": "temporal", "start_ms": 3000, "end_ms": 6000},
				{"id": "emission", "label": "Speculative Stream Emission", "kind": "temporal", "start_ms": 6000, "end_ms": 9000},
			},
			TargetPartID: "consensus",
			InitialHTML: `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script></head><body><main id="app"><div id="candidates"></div><div id="consensus"></div><div id="emission"></div></main></body></html>`,
			RevisedSnippet: `<div id="consensus" class="neural-attention-crossbar"></div>`,
		},
	}

	var wg sync.WaitGroup
	errorsMu := sync.Mutex{}
	var runErrors []error

	for i, spec := range specs {
		wg.Add(1)
		go func(agentIdx int, tc struct {
			ID            string
			Title         string
			DurationMS    int
			Parts         []map[string]any
			TargetPartID  string
			InitialHTML   string
			RevisedSnippet string
		}) {
			defer wg.Done()

			sessionID := fmt.Sprintf("session-agent-%d", agentIdx+1)
			runID := fmt.Sprintf("run-agent-%d", agentIdx+1)

			repo := &directArtifactV3RepoFake{}
			author := NewArtifactV3AuthorService(t.TempDir(), repo, &artifactV3BuilderFake{}, &artifactV3PreviewerFake{})
			runtime := NewRuntime(1)
			runtime.SetArtifactV3AuthorService(author)

			scope := WorkspaceScope{
				SessionID: sessionID,
				Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account-1", UserID: "user-1"},
			}
			ctx, cancel := context.WithTimeout(WithArtifactRunContext(context.Background(), ArtifactRunContext{SessionID: sessionID, RunID: runID}), 15*time.Second)
			defer cancel()

			// STEP 1: Agent creates the initial 3-part animation
			createPayload := map[string]any{
				"action":            "create",
				"title":             tc.Title,
				"media_type":        "text/html",
				"content":           tc.InitialHTML,
				"animation_profile": map[string]any{"profile": "motion_ui"},
				"parts":             tc.Parts,
				"draft_handle":      map[string]any{"session_id": sessionID, "artifact_id": "art-" + tc.ID, "turn_id": "t-1"},
			}
			createBytes, _ := json.Marshal(createPayload)
			createOut, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: fmt.Sprintf("call-create-%d", agentIdx), Name: "manage_artifact", Arguments: string(createBytes)})
			if err != nil {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d initial create failed: %w", agentIdx+1, err))
				errorsMu.Unlock()
				return
			}
			if !strings.Contains(createOut, `"artifact_id"`) {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d create missing artifact_id in output: %s", agentIdx+1, createOut))
				errorsMu.Unlock()
				return
			}

			// Verify manifest has all 3 parts properly configured
			if len(repo.submits) != 1 {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d expected 1 submit, got %d", agentIdx+1, len(repo.submits)))
				errorsMu.Unlock()
				return
			}
			var m pebblestore.ArtifactV3Manifest
			_ = json.Unmarshal(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename], &m)
			if len(m.Parts) != 3 {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d expected 3 parts in manifest, got %d", agentIdx+1, len(m.Parts)))
				errorsMu.Unlock()
				return
			}

			// STEP 2: Agent performs targeted iteration on TargetPartID
			revisedHTML := tc.InitialHTML
			if tc.RevisedSnippet != "" {
				// Replace a snippet in the body
				revisedHTML = strings.Replace(revisedHTML, "</body>", tc.RevisedSnippet+"</body>", 1)
			}
			revisePayload := map[string]any{
				"action": "revise_v3",
				"artifact_v3_reference": map[string]any{
					"session_id":   sessionID,
					"artifact_id":  "artifact-direct",
					"revision_ref": "revision-" + strings.Repeat("a", 40),
				},
				"target_part_ids": []string{tc.TargetPartID},
				"content":         revisedHTML,
				"draft_handle":    map[string]any{"session_id": sessionID, "artifact_id": "art-" + tc.ID, "turn_id": "t-2"},
			}
			reviseBytes, _ := json.Marshal(revisePayload)
			reviseOut, err := runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, Call{CallID: fmt.Sprintf("call-revise-%d", agentIdx), Name: "manage_artifact", Arguments: string(reviseBytes)})
			if err != nil {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d targeted revise failed on %s: %w", agentIdx+1, tc.TargetPartID, err))
				errorsMu.Unlock()
				return
			}
			if !strings.Contains(reviseOut, `"status":"awaiting_selection"`) || !strings.Contains(reviseOut, tc.TargetPartID) {
				errorsMu.Lock()
				runErrors = append(runErrors, fmt.Errorf("agent %d revise invalid output: %s", agentIdx+1, reviseOut))
				errorsMu.Unlock()
				return
			}
		}(i, spec)
	}

	wg.Wait()

	if len(runErrors) != 0 {
		for _, err := range runErrors {
			t.Errorf("5-agent flow error: %v", err)
		}
		t.Fatalf("5-agent benchmark failed with %d errors", len(runErrors))
	}
}
