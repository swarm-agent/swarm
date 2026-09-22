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
      version: "swarm.animation/v1",
      ready: async () => ({ duration_ms: 9000, fps: 60 }),
      seek: async (time_ms) => ({ time_ms: time_ms })
    };
  </script>
</head>
<body>
  <main id="swarm-animation-stage">Animation Content</main>
</body>
</html>`

	// 1. Valid HTML must pass
	if err := validateGeneratedDesignerHTML(validHTML, true); err != nil {
		t.Fatalf("expected valid HTML to pass validation, got: %v", err)
	}

	// 2. Empty output must fail
	if err := validateGeneratedDesignerHTML("", true); err == nil {
		t.Fatal("expected empty output to fail validation")
	}

	// 3. Missing <html> tags must fail
	if err := validateGeneratedDesignerHTML("<div>plain div</div>", true); err == nil {
		t.Fatal("expected missing html tags to fail validation")
	}

	// 4. Missing animation manifest must fail when isAnimation=true
	noManifest := `<html><head><script>window.__SWARM_ANIMATION_V1__ = { version: "swarm.animation/v1", ready: async()=>({}), seek: async()=>({}) };</script></head><body><main id="stage"></main></body></html>`
	if err := validateGeneratedDesignerHTML(noManifest, true); err == nil || !strings.Contains(err.Error(), "swarm-animation-manifest") {
		t.Fatalf("expected missing manifest error, got: %v", err)
	}

	// 5. Missing controller must fail when isAnimation=true
	noController := `<html><head><script id="swarm-animation-manifest" type="application/json">{}</script></head><body><main id="stage"></main></body></html>`
	if err := validateGeneratedDesignerHTML(noController, true); err == nil || !strings.Contains(err.Error(), "__SWARM_ANIMATION_V1__") {
		t.Fatalf("expected missing controller error, got: %v", err)
	}

	// 6. Missing stage element with id must fail
	noStage := `<!doctype html><html><head><script id="swarm-animation-manifest" type="application/json">{"version":"swarm.animation/v1","duration_ms":9000,"fps":60}</script><script>window.__SWARM_ANIMATION_V1__ = { version: "swarm.animation/v1", ready: async()=>({}), seek: async()=>({}) };</script></head><body><p>No id element</p></body></html>`
	if err := validateGeneratedDesignerHTML(noStage, true); err == nil || !strings.Contains(err.Error(), "stage container element") {
		t.Fatalf("expected missing stage error, got: %v", err)
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
	sys, user := composeDirectDesignerSwarmPrompt(
		"Create a cyberpunk HUD",
		"Neon Blue",
		nil,
		taskSwarmHydratedDelta{Index: 1, Title: "Cyberpunk HUD", Theme: "Neon Blue", Role: "Cyan circuits and glowing displays"},
		false,
		"",
		&pebblestore.SessionArtifactAnimationProfile{ProfileID: "motion_ui"},
		6000,
	)

	if !strings.Contains(sys, "swarm-animation-manifest") || !strings.Contains(sys, "__SWARM_ANIMATION_V1__") {
		t.Fatalf("system prompt missing animation contracts: %s", sys)
	}
	if !strings.Contains(user, "Create a cyberpunk HUD") {
		t.Fatalf("user prompt missing brief: %s", user)
	}
	if !strings.Contains(user, "Neon Blue") {
		t.Fatalf("user prompt missing theme: %s", user)
	}

	// Test revision prompt (whole animation iteration)
	existingHTML := `<!doctype html><html><body><main id="stage">Old animation</main></body></html>`
	_, userRev := composeDirectDesignerSwarmPrompt(
		"Upgrade vortex with counter-rotating rings and make the center brighter",
		"Quantum",
		nil,
		taskSwarmHydratedDelta{Index: 1, Title: "Vortex Upgrade", Theme: "Quantum"},
		true,
		existingHTML,
		nil,
		0,
	)

	if !strings.Contains(userRev, "ANIMATION REVISION") {
		t.Fatalf("revision prompt missing ANIMATION REVISION header: %s", userRev)
	}
	if !strings.Contains(userRev, "Upgrade vortex with counter-rotating rings and make the center brighter") {
		t.Fatalf("revision prompt missing user instructions: %s", userRev)
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
	brokenOutput := "```html\n<!doctype html><html><head><script id=\"swarm-animation-manifest\" type=\"application/json\">{\"version\":\"swarm.animation/v1\",\"duration_ms\":6000,\"fps\":60}</script></head><body><main id=\"stage\"></main></body></html>\n```"
	err := validateGeneratedDesignerHTML(extractHTMLFromModelOutput(brokenOutput), true)
	if err == nil {
		t.Fatal("expected broken output to fail validation")
	}

	// Model fixes it on repair turn
	fixedOutput := "```html\n<!doctype html><html><head>\n  <script id=\"swarm-animation-manifest\" type=\"application/json\">{\"version\":\"swarm.animation/v1\",\"duration_ms\":6000,\"fps\":60}</script>\n  <script>\n    window.__SWARM_ANIMATION_V1__ = {\n      version: \"swarm.animation/v1\",\n      ready: async () => ({ duration_ms: 6000, fps: 60 }),\n      seek: async (ms) => ({ time_ms: ms })\n    };\n  </script>\n</head><body><main id=\"stage\"></main></body></html>\n```"
	fixedErr := validateGeneratedDesignerHTML(extractHTMLFromModelOutput(fixedOutput), true)
	if fixedErr != nil {
		t.Fatalf("expected repaired output to pass validation, got: %v", fixedErr)
	}
}
