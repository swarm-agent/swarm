package run

import (
	"encoding/json"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDirectDesignerSwarmHTMLValidation(t *testing.T) {
	validHTML := `<!doctype html>
<html>
<head>
  <script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script>
  <script>
    window.__SWARM_ANIMATION_V1__ = {
      ready: async () => ({ duration_ms: 9000, fps: 60 }),
      seek: async (time_ms) => ({ time_ms: time_ms })
    };
  </script>
</head>
<body>
  <section id="part-1">Part 1 Content</section>
  <section id="part-2">Part 2 Content</section>
  <section id="part-3">Part 3 Content</section>
</body>
</html>`

	// 1. Valid HTML must pass
	if err := validateGeneratedDesignerHTML(validHTML, true, []string{"part-1", "part-2", "part-3"}); err != nil {
		t.Fatalf("expected valid HTML to pass validation, got: %v", err)
	}

	// 2. Empty output must fail
	if err := validateGeneratedDesignerHTML("", true, nil); err == nil {
		t.Fatal("expected empty output to fail validation")
	}

	// 3. Missing <html> tags must fail
	if err := validateGeneratedDesignerHTML("<div>plain div</div>", true, nil); err == nil {
		t.Fatal("expected missing html tags to fail validation")
	}

	// 4. Missing animation manifest must fail when isAnimation=true
	noManifest := `<html><head><script>window.__SWARM_ANIMATION_V1__ = { ready: async()=>({}), seek: async()=>({}) };</script></head><body></body></html>`
	if err := validateGeneratedDesignerHTML(noManifest, true, nil); err == nil || !strings.Contains(err.Error(), "swarm-animation-manifest") {
		t.Fatalf("expected missing manifest error, got: %v", err)
	}

	// 5. Missing controller must fail when isAnimation=true
	noController := `<html><head><script id="swarm-animation-manifest" type="application/json">{}</script></head><body></body></html>`
	if err := validateGeneratedDesignerHTML(noController, true, nil); err == nil || !strings.Contains(err.Error(), "__SWARM_ANIMATION_V1__") {
		t.Fatalf("expected missing controller error, got: %v", err)
	}

	// 6. Missing required part ID must fail
	missingPart := `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script><script>window.__SWARM_ANIMATION_V1__ = { version: "swarm.animation/v1", ready: async()=>({}), seek: async()=>({}) };</script></head><body><div id="part-1"></div></body></html>`
	if err := validateGeneratedDesignerHTML(missingPart, true, []string{"part-1", "part-2"}); err == nil || !strings.Contains(err.Error(), "part-2") {
		t.Fatalf("expected missing part-2 error, got: %v", err)
	}
}

func TestDirectDesignerSwarmExtractHTML(t *testing.T) {
	rawMarkdown := "Here is your design:\n```html\n<!doctype html><html><body><h1>Hello</h1></body></html>\n```\nEnjoy!"
	extracted := extractHTMLFromModelOutput(rawMarkdown)
	if !strings.HasPrefix(extracted, "<!doctype html>") || !strings.HasSuffix(extracted, "</html>") {
		t.Fatalf("unexpected extracted html: %s", extracted)
	}

	bareHTML := "<!DOCTYPE html><html><body>Bare</body></html>"
	extractedBare := extractHTMLFromModelOutput(bareHTML)
	if extractedBare != bareHTML {
		t.Fatalf("unexpected extracted bare html: %s", extractedBare)
	}
}

func TestDirectDesignerSwarmPromptComposition(t *testing.T) {
	// Test initial creation prompt
	parts := []pebblestore.SessionArtifactPart{
		{ID: "boot", Label: "System Boot", StartMs: 0, EndMs: 3000, Kind: "temporal"},
		{ID: "vortex", Label: "Neural Vortex", StartMs: 3000, EndMs: 6000, Kind: "temporal"},
	}
	sys, user := composeDirectDesignerSwarmPrompt(
		"Create a cyberpunk HUD",
		"Neon Blue",
		nil,
		taskSwarmHydratedDelta{Index: 1, Title: "Cyberpunk HUD", Theme: "Neon Blue", Role: "Cyan circuits and glowing displays"},
		false,
		"",
		"",
		parts,
		&pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui"},
		6000,
	)

	if !strings.Contains(sys, "swarm-animation-manifest") || !strings.Contains(sys, "__SWARM_ANIMATION_V1__") {
		t.Fatalf("system prompt missing animation contracts: %s", sys)
	}
	if !strings.Contains(user, "boot") || !strings.Contains(user, "vortex") {
		t.Fatalf("user prompt missing required parts: %s", user)
	}
	if !strings.Contains(user, "Neon Blue") {
		t.Fatalf("user prompt missing theme: %s", user)
	}

	// Test targeted revision prompt
	existingHTML := `<!doctype html><html><body><div id="boot"></div><div id="vortex"></div></body></html>`
	_, userRev := composeDirectDesignerSwarmPrompt(
		"Upgrade vortex with counter-rotating rings",
		"Quantum",
		nil,
		taskSwarmHydratedDelta{Index: 1, Title: "Vortex Upgrade", Theme: "Quantum"},
		true,
		existingHTML,
		"vortex",
		nil,
		nil,
		0,
	)

	if !strings.Contains(userRev, "TARGETED SINGLE-PART REVISION") {
		t.Fatalf("revision prompt missing targeted revision header: %s", userRev)
	}
	if !strings.Contains(userRev, "Target Part to modify: vortex") {
		t.Fatalf("revision prompt missing target part identification: %s", userRev)
	}
	if !strings.Contains(userRev, "preserve all non-target parts") {
		t.Fatalf("revision prompt missing preservation constraint: %s", userRev)
	}
	if !strings.Contains(userRev, existingHTML) {
		t.Fatalf("revision prompt missing existing HTML: %s", userRev)
	}
}

func TestDirectDesignerSwarmManifestValidation(t *testing.T) {
	designers := []taskDesignerManifestRow{
		{Index: 1, Theme: "theme-1", StreamKey: "key-1"},
		{Index: 2, Theme: "theme-2", StreamKey: "key-2"},
	}
	manifest := taskLaunchManifest{
		TaskMode:        taskModeSwarm,
		SwarmAgentType:  "designer",
		ExecutionFormat: taskExecutionFormatDesignerDirect,
		DesignerCount:   2,
		Designers:       designers,
	}
	digest, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatalf("digest failed: %v", err)
	}
	manifest.ManifestHash = digest

	envelope, _ := json.Marshal(map[string]any{"manifest_hash": digest, "manifest": manifest})
	parsed := taskCallArguments{
		Mode: taskModeSwarm,
		Swarm: &taskSwarmSpec{
			AgentType: "designer",
			Count:     2,
			Themes:    []string{"theme-1", "theme-2"},
		},
		Launches: []taskLaunchSpec{
			{StreamKey: "key-1"},
			{StreamKey: "key-2"},
		},
	}

	if err := validateApprovedDirectDesignerSwarm(string(envelope), parsed); err != nil {
		t.Fatalf("expected valid approved manifest to pass, got: %v", err)
	}

	// Tampered hash must fail
	tampered, _ := json.Marshal(map[string]any{"manifest_hash": "bad-hash", "manifest": manifest})
	if err := validateApprovedDirectDesignerSwarm(string(tampered), parsed); err == nil {
		t.Fatal("expected tampered hash to fail validation")
	}

	// Theme mismatch must fail
	mismatchedThemes := parsed
	mismatchedThemes.Swarm.Themes = []string{"theme-1", "wrong-theme"}
	if err := validateApprovedDirectDesignerSwarm(string(envelope), mismatchedThemes); err == nil {
		t.Fatal("expected theme mismatch to fail validation")
	}
}

func TestDirectDesignerSwarmRepairFlow(t *testing.T) {
	// First output from model is broken (missing __SWARM_ANIMATION_V1__)
	brokenOutput := "```html\n<!doctype html><html><head><script id=\"swarm-animation-manifest\" type=\"application/json\">{\"version\":\"swarm.animation/v1\",\"duration_ms\":6000,\"fps\":60}</script></head><body><section id=\"part-1\"></section><section id=\"part-2\"></section></body></html>\n```"
	err := validateGeneratedDesignerHTML(extractHTMLFromModelOutput(brokenOutput), true, []string{"part-1", "part-2"})
	if err == nil {
		t.Fatal("expected broken output to fail validation")
	}

	// Model fixes it on repair turn
	fixedOutput := "```html\n<!doctype html><html><head>\n  <script id=\"swarm-animation-manifest\" type=\"application/json\">{\"version\":\"swarm.animation/v1\",\"duration_ms\":6000,\"fps\":60}</script>\n  <script>\n    window.__SWARM_ANIMATION_V1__ = {\n      version: \"swarm.animation/v1\",\n      ready: async () => ({ duration_ms: 6000, fps: 60 }),\n      seek: async (ms) => ({ time_ms: ms })\n    };\n  </script>\n</head><body><section id=\"part-1\"></section><section id=\"part-2\"></section></body></html>\n```"
	fixedErr := validateGeneratedDesignerHTML(extractHTMLFromModelOutput(fixedOutput), true, []string{"part-1", "part-2"})
	if fixedErr != nil {
		t.Fatalf("expected repaired output to pass validation, got: %v", fixedErr)
	}
}

func TestExtractPartsFromPrompt(t *testing.T) {
	prompt1 := `Create a high-fidelity, self-contained 3-part HTML animation of the Swarm mark.
1. Part 1 (0 - 3000ms) Emergence: Awakening
2. Part 2 (3000 - 6000ms) Metamorphosis: Fluid flow
3. Part 3 (6000 - 9000ms) Convergence: Lock into mark`
	parts1 := extractPartsFromPrompt(prompt1, 9000)
	if len(parts1) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts1))
	}
	if parts1[0].ID != "part-1" || parts1[0].StartMs != 0 || parts1[0].EndMs != 3000 {
		t.Fatalf("unexpected part 1: %+v", parts1[0])
	}
	if parts1[2].ID != "part-3" || parts1[2].StartMs != 6000 || parts1[2].EndMs != 9000 {
		t.Fatalf("unexpected part 3: %+v", parts1[2])
	}

	prompt2 := "Create a 3-part HTML animation (Part 1: boot, Part 2: vortex, Part 3: online) with duration 9000ms"
	parts2 := extractPartsFromPrompt(prompt2, 9000)
	if len(parts2) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts2))
	}
	if parts2[0].ID != "boot" || parts2[1].ID != "vortex" || parts2[2].ID != "online" {
		t.Fatalf("unexpected named parts: %+v", parts2)
	}

	prompt3 := "ok make a 3 swarm designers of a 3 part swarm mark animation"
	parts3 := extractPartsFromPrompt(prompt3, 9000)
	if len(parts3) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts3))
	}
	if parts3[0].ID != "part-1" || parts3[1].ID != "part-2" || parts3[2].ID != "part-3" {
		t.Fatalf("unexpected general count parts: %+v", parts3)
	}
}

func TestExtractPartsFromHTML(t *testing.T) {
	html := `<!doctype html><html><body>
		<section id="part-1"></section>
		<section id="part-2"></section>
		<section id="part-3"></section>
	</body></html>`
	parts := extractPartsFromHTML(html, 9000)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts from HTML, got %d", len(parts))
	}
	if parts[0].ID != "part-1" || parts[0].StartMs != 0 || parts[0].EndMs != 3000 {
		t.Fatalf("unexpected part 1 from HTML: %+v", parts[0])
	}
	if parts[2].ID != "part-3" || parts[2].StartMs != 6000 || parts[2].EndMs != 9000 {
		t.Fatalf("unexpected part 3 from HTML: %+v", parts[2])
	}
}

func TestEnsureAnimationSeekSceneID(t *testing.T) {
	html := `<!doctype html><html><head><script>
		window.__SWARM_ANIMATION_V1__ = {
			version: "swarm.animation/v1",
			ready: async () => ({ duration_ms: 9000, fps: 60 }),
			seek: async (time_ms) => {
				return { time_ms: time_ms };
			}
		};
	</script></head><body></body></html>`
	parts := []pebblestore.SessionArtifactPart{
		{ID: "part-1", StartMs: 0, EndMs: 3000},
		{ID: "part-2", StartMs: 3000, EndMs: 6000},
		{ID: "part-3", StartMs: 6000, EndMs: 9000},
	}
	patched := ensureAnimationSeekSceneID(html, parts, 9000)
	if !strings.Contains(patched, "__swarmGetSceneId") {
		t.Fatalf("expected patched HTML to contain __swarmGetSceneId, got:\n%s", patched)
	}
	if !strings.Contains(patched, "scene_id:") {
		t.Fatalf("expected patched HTML seek to include scene_id, got:\n%s", patched)
	}
}
