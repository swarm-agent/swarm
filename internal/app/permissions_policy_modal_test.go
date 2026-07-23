package app

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestPermissionsBashProfilesExposeFourRadioChoices(t *testing.T) {
	if len(permissionsBashProfiles) != 4 {
		t.Fatalf("bash profile count = %d, want 4", len(permissionsBashProfiles))
	}
	want := []string{"current_rules", "allow_every_read", "allow_safe_reads", "only_critical_prompts"}
	for index, value := range want {
		if permissionsBashProfiles[index].Value != value {
			t.Fatalf("profile %d = %q, want %q", index, permissionsBashProfiles[index].Value, value)
		}
	}
	if !strings.Contains(permissionsBashProfiles[2].Label, "Recommended") {
		t.Fatalf("safe reads label = %q, want recommendation", permissionsBashProfiles[2].Label)
	}
}

func TestPermissionsBashProfileSelectionIsPreservedAndDisabledWhileOff(t *testing.T) {
	a := &App{keybinds: ui.NewDefaultKeyBindings()}
	a.homeModel.BypassPermissions = true
	a.openPermissionsPolicyModal(client.PermissionPolicy{
		Version:     1,
		BashProfile: "allow_safe_reads",
		Rules:       []client.PermissionRule{{ID: "rule-1", Kind: "bash_prefix", Decision: "allow", Pattern: "git"}},
	})

	if got := a.permissionsPolicyModal.Policy.BashProfile; got != "allow_safe_reads" {
		t.Fatalf("preserved profile = %q", got)
	}
	if !strings.Contains(a.permissionsPolicyModal.Status, "Permissions are OFF") {
		t.Fatalf("off-state status = %q", a.permissionsPolicyModal.Status)
	}
	if !a.handlePermissionsPolicyModalKey(tcell.NewEventKey(tcell.KeyRune, '4', tcell.ModNone)) {
		t.Fatal("profile key was not handled")
	}
	if got := a.permissionsPolicyModal.Policy.BashProfile; got != "allow_safe_reads" {
		t.Fatalf("disabled selection changed profile to %q", got)
	}
	if len(a.permissionsPolicyModal.Policy.Rules) != 1 {
		t.Fatalf("off-state changed granular rules: %#v", a.permissionsPolicyModal.Policy.Rules)
	}
}
