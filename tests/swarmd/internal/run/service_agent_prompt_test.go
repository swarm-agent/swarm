package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestComposeInstructionsIncludesAgentAndCallerOverrides(t *testing.T) {
	svc := &Service{}
	profile := pebblestore.AgentProfile{
		Name:   "swarm",
		Mode:   "primary",
		Prompt: "Primary prompt block.",
	}
	out := svc.composeInstructions("/tmp/workspace", profile, "Caller override block.")
	for _, expected := range []string{
		"Active agent profile:",
		"- name: swarm",
		"Primary prompt block.",
		"Caller additive instructions:",
		"Caller override block.",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in composed instructions:\n%s", expected, out)
		}
	}
}

func TestComposeInstructionsDoesNotEmbedAvailableSkills(t *testing.T) {
	workspace := t.TempDir()
	skillPath := filepath.Join(workspace, ".agents", "skills", "hot-reload-test", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	content := "---\nname: hot-reload-test\ndescription: Hot reload validation skill\n---\n\nUse this skill when requested."
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill fixture: %v", err)
	}

	svc := &Service{discovery: discovery.NewService()}
	out := svc.composeInstructions(workspace, pebblestore.AgentProfile{}, "")
	if strings.Contains(out, "Available skills in context:") {
		t.Fatalf("did not expect skills to be embedded in composed instructions:\n%s", out)
	}
	if strings.Contains(out, "hot-reload-test") {
		t.Fatalf("did not expect discovered skill names in composed instructions:\n%s", out)
	}
}

func TestResolvePrimaryAgentFallsBackWithoutRegistry(t *testing.T) {
	svc := &Service{}
	profile, err := svc.resolvePrimaryAgent("")
	if err != nil {
		t.Fatalf("resolvePrimaryAgent() error = %v", err)
	}
	if profile.Name != "swarm" {
		t.Fatalf("profile.Name = %q, want swarm", profile.Name)
	}
	if profile.Mode != "primary" {
		t.Fatalf("profile.Mode = %q, want primary", profile.Mode)
	}
	if !strings.Contains(profile.Prompt, "Match execution depth to request scope") {
		t.Fatalf("expected fallback prompt to include scope-aware guidance, got: %q", profile.Prompt)
	}
}
