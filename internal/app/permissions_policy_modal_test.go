package app

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestPermissionsBashProfilesExposeThreeRadioChoices(t *testing.T) {
	if len(permissionsBashProfiles) != 3 {
		t.Fatalf("bash profile count = %d, want 3", len(permissionsBashProfiles))
	}
	wantValues := []string{"current_rules", "allow_every_read", "only_critical_prompts"}
	wantLabels := []string{"Default", "Allow every read", "Only critical prompts"}
	for index := range wantValues {
		if permissionsBashProfiles[index].Value != wantValues[index] {
			t.Fatalf("profile %d value = %q, want %q", index, permissionsBashProfiles[index].Value, wantValues[index])
		}
		if permissionsBashProfiles[index].Label != wantLabels[index] {
			t.Fatalf("profile %d label = %q, want %q", index, permissionsBashProfiles[index].Label, wantLabels[index])
		}
	}
}

func TestPermissionsBashProfileSelectionIsPreservedAndDisabledWhileOff(t *testing.T) {
	a := &App{keybinds: ui.NewDefaultKeyBindings()}
	a.homeModel.BypassPermissions = true
	a.openPermissionsPolicyModal(client.PermissionPolicy{
		Version:     1,
		BashProfile: "only_critical_prompts",
		Rules:       []client.PermissionRule{{ID: "rule-1", Kind: "bash_prefix", Decision: "allow", Pattern: "git"}},
	})

	if got := a.permissionsPolicyModal.Policy.BashProfile; got != "only_critical_prompts" {
		t.Fatalf("preserved profile = %q", got)
	}
	if !strings.Contains(a.permissionsPolicyModal.Status, "Permissions are OFF") {
		t.Fatalf("off-state status = %q", a.permissionsPolicyModal.Status)
	}
	if !a.handlePermissionsPolicyModalKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone)) {
		t.Fatal("profile key was not handled")
	}
	if got := a.permissionsPolicyModal.Policy.BashProfile; got != "only_critical_prompts" {
		t.Fatalf("disabled selection changed profile to %q", got)
	}
	if len(a.permissionsPolicyModal.Policy.Rules) != 1 {
		t.Fatalf("off-state changed granular rules: %#v", a.permissionsPolicyModal.Policy.Rules)
	}
}
