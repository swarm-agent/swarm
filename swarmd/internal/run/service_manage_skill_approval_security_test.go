package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/tool"
)

func TestManageSkillApprovalArgumentsAcceptsCanonicalRawAndWrappedArguments(t *testing.T) {
	canonical := map[string]any{
		"action":            "update",
		"skill":             "example",
		"content":           "updated",
		"confirm":           true,
		"expected_revision": "revision-token",
	}
	for name, payload := range map[string]map[string]any{
		"raw":     canonical,
		"wrapped": {"approved_arguments": canonical},
	} {
		t.Run(name, func(t *testing.T) {
			args := manageSkillApprovalArguments(payload)
			if got := mapString(args, "expected_revision"); got != "revision-token" {
				t.Fatalf("expected_revision = %q, want revision-token", got)
			}
			if !mapBool(args, "confirm") {
				t.Fatal("canonical approved arguments lost confirm=true")
			}
		})
	}
}

func TestManageSkillApprovalArgumentsPreservesLegacyPreviewRevision(t *testing.T) {
	payload := map[string]any{
		"action": "update",
		"change": map[string]any{
			"path":              "/workspace/.agents/skills/example/SKILL.md",
			"after":             "updated",
			"expected_revision": "revision-token",
		},
	}
	args := manageSkillApprovalArguments(payload)
	if got := mapString(args, "expected_revision"); got != "revision-token" {
		t.Fatalf("expected_revision = %q, want revision-token", got)
	}
}

func TestManageSkillPermissionRequirementsAreActionSensitive(t *testing.T) {
	for _, tc := range []struct {
		action       string
		want         string
		wantApproval bool
	}{
		{action: "inspect", want: "manage_skill"},
		{action: "list", want: "manage_skill"},
		{action: "get", want: "manage_skill"},
		{action: "create", want: "manage_skill"},
		{action: "update", want: "skill_change", wantApproval: true},
		{action: "delete", want: "skill_change", wantApproval: true},
	} {
		t.Run(tc.action, func(t *testing.T) {
			requirement, approval := permissionRequirement(sessionruntime.ModeAuto, "manage-skill", mustJSON(t, map[string]any{"action": tc.action}))
			if requirement != tc.want || approval != tc.wantApproval {
				t.Fatalf("permissionRequirement = %q/%t, want %q/%t", requirement, approval, tc.want, tc.wantApproval)
			}
		})
	}
}

func TestManageSkillCreateGateAutoApprovesCanonicalArguments(t *testing.T) {
	svc, sessionID, _, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	arguments := mustJSON(t, map[string]any{
		"action":  "create",
		"skill":   "example",
		"content": "---\nname: example\ndescription: create fixture\n---\nbody\n",
	})
	results, approved, _, mask, feedback, err := svc.gateToolCalls(context.Background(), sessionID, "run-create", 1, sessionruntime.ModeAuto, []tool.Call{{CallID: "call-create", Name: "manage-skill", Arguments: arguments}}, nil, nil)
	if err != nil {
		t.Fatalf("gate create: %v", err)
	}
	if len(results) != 1 || len(approved) != 1 || len(mask) != 1 || !mask[0] || len(feedback) != 0 {
		t.Fatalf("create gate result = results=%#v approved=%#v mask=%v feedback=%#v", results, approved, mask, feedback)
	}
}

func TestManageSkillAutomaticPolicyApprovalReturnsCanonicalRevisionArguments(t *testing.T) {
	svc, sessionID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	create := tool.Call{Name: "manage-skill", Arguments: mustJSON(t, map[string]any{
		"action":  "create",
		"skill":   "example",
		"content": "---\nname: example\ndescription: create fixture\n---\noriginal\n",
	})}
	if output, err := svc.executeManageSkillTool(sessionID, create, ""); err != nil || !strings.Contains(output, `"applied":true`) {
		t.Fatalf("create output=%s err=%v", output, err)
	}
	if _, err := permissions.UpsertRuleForAccount("test-account", permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "skill_change"}); err != nil {
		t.Fatalf("persist skill mutation rule: %v", err)
	}
	arguments := mustJSON(t, map[string]any{
		"action":  "update",
		"skill":   "example",
		"content": "---\nname: example\ndescription: update fixture\n---\nupdated\n",
	})
	_, approved, _, mask, feedback, err := svc.gateToolCalls(context.Background(), sessionID, "run-update", 1, sessionruntime.ModeAuto, []tool.Call{{CallID: "call-update", Name: "manage-skill", Arguments: arguments}}, nil, nil)
	if err != nil {
		t.Fatalf("gate update: %v", err)
	}
	if len(approved) != 1 || len(mask) != 1 || !mask[0] || len(feedback) != 1 {
		t.Fatalf("automatic update approval = approved=%#v mask=%v feedback=%#v", approved, mask, feedback)
	}
	var canonical map[string]any
	if err := json.Unmarshal([]byte(feedback[0].ApprovedArguments), &canonical); err != nil {
		t.Fatalf("decode canonical approved arguments: %v", err)
	}
	if !mapBool(canonical, "confirm") || mapString(canonical, "expected_revision") == "" {
		t.Fatalf("canonical approved arguments = %#v, want confirm and expected revision", canonical)
	}
}

func TestBuildManageSkillPermissionPayloadCarriesApprovedRevision(t *testing.T) {
	svc, sessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	create := tool.Call{Name: "manage-skill", Arguments: mustJSON(t, map[string]any{
		"action":  "create",
		"skill":   "example",
		"content": "---\nname: example\ndescription: create fixture\n---\noriginal\n",
	})}
	if output, err := svc.executeManageSkillTool(sessionID, create, "{}"); err != nil || !strings.Contains(output, `"applied":true`) {
		t.Fatalf("create output=%s err=%v", output, err)
	}
	payload, err := svc.buildManageSkillPermissionPayload(sessionID, tool.Call{Name: "manage-skill", Arguments: mustJSON(t, map[string]any{
		"action":  "update",
		"skill":   "example",
		"content": "---\nname: example\ndescription: update fixture\n---\nupdated\n",
	})})
	if err != nil {
		t.Fatalf("build update permission payload: %v", err)
	}
	approved, ok := payload["approved_arguments"].(map[string]any)
	if !ok || !mapBool(approved, "confirm") || mapString(approved, "expected_revision") == "" {
		t.Fatalf("approved arguments = %#v, want confirm and expected revision", payload["approved_arguments"])
	}
	raw, err := json.Marshal(approved)
	if err != nil {
		t.Fatalf("marshal approved arguments: %v", err)
	}
	output, err := svc.executeManageSkillTool(sessionID, tool.Call{Name: "manage-skill"}, string(raw))
	if err != nil || !strings.Contains(output, `"action":"update"`) || !strings.Contains(output, `"applied":true`) {
		t.Fatalf("approved update output=%s err=%v", output, err)
	}

	deletePayload, err := svc.buildManageSkillPermissionPayload(sessionID, tool.Call{Name: "manage-skill", Arguments: mustJSON(t, map[string]any{
		"action": "delete",
		"skill":  "example",
	})})
	if err != nil {
		t.Fatalf("build delete permission payload: %v", err)
	}
	deleteApproved, ok := deletePayload["approved_arguments"].(map[string]any)
	if !ok || !mapBool(deleteApproved, "confirm") || mapString(deleteApproved, "expected_revision") == "" {
		t.Fatalf("delete approved arguments = %#v, want confirm and expected revision", deletePayload["approved_arguments"])
	}
	wrapped, err := json.Marshal(map[string]any{"approved_arguments": deleteApproved})
	if err != nil {
		t.Fatalf("marshal wrapped delete arguments: %v", err)
	}
	output, err = svc.executeManageSkillTool(sessionID, tool.Call{Name: "manage-skill"}, string(wrapped))
	if err != nil || !strings.Contains(output, `"action":"delete"`) || !strings.Contains(output, `"applied":true`) {
		t.Fatalf("approved delete output=%s err=%v", output, err)
	}
}
