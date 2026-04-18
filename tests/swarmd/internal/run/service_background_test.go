package run

import (
	"testing"

	"swarm/packages/swarmd/internal/permission"
	"swarm/packages/swarmd/internal/tool"
)

func TestCompileRunToolScopeAllowToolsPreservesPendingPermissions(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()

	svc := &Service{tools: tool.NewRuntime(1)}
	overlay, disabled, err := svc.compileRunToolScope(RunToolScope{
		AllowTools: []string{"read", "ask_user", "exit_plan_mode", "bash"},
	})
	if err != nil {
		t.Fatalf("compile run tool scope: %v", err)
	}
	if overlay == nil {
		t.Fatal("expected compiled overlay policy")
	}
	if disabled["read"] {
		t.Fatalf("read should remain enabled, disabled=%v", disabled["read"])
	}
	if !disabled["write"] {
		t.Fatalf("write should be disabled outside the explicit allow list")
	}
	if !disabled["plan_manage"] {
		t.Fatalf("plan_manage should be disabled outside the explicit allow list")
	}

	cases := []struct {
		name      string
		mode      string
		toolName  string
		arguments string
		want      permission.AuthorizationDecision
	}{
		{name: "read remains allowed", mode: "auto", toolName: "read", arguments: `{"path":"README.md"}`, want: permission.AuthorizationApprove},
		{name: "manage_agent inspect stays allowed", mode: "auto", toolName: "manage-agent", arguments: `{"action":"inspect"}`, want: permission.AuthorizationApprove},
		{name: "manage_theme stays allowed", mode: "auto", toolName: "manage-theme", arguments: `{"action":"set","theme_id":"midnight"}`, want: permission.AuthorizationApprove},
		{name: "ask_user remains pending", mode: "auto", toolName: "ask_user", arguments: `{"prompt":"Need input"}`, want: permission.AuthorizationPending},
		{name: "exit_plan_mode remains pending", mode: "plan", toolName: "exit_plan_mode", arguments: `{"title":"Plan","plan":"1. test"}`, want: permission.AuthorizationPending},
		{name: "bash remains pending", mode: "auto", toolName: "bash", arguments: `{"command":"echo test"}`, want: permission.AuthorizationPending},
		{name: "write becomes denied", mode: "auto", toolName: "write", arguments: `{"path":"out.txt","content":"x"}`, want: permission.AuthorizationDeny},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
				SessionID:     "session_scope_allow",
				RunID:         "run_scope_allow",
				CallID:        tc.name,
				ToolName:      tc.toolName,
				ToolArguments: tc.arguments,
				Mode:          tc.mode,
				Overlay:       overlay,
			})
			if err != nil {
				t.Fatalf("authorize tool call: %v", err)
			}
			if result.Decision != tc.want {
				t.Fatalf("decision = %q, want %q", result.Decision, tc.want)
			}
		})
	}
}

func TestCompileRunToolScopeReadWritePresetKeepsInteractivePlanTools(t *testing.T) {
	permSvc, store := openRunPermissionService(t)
	defer func() {
		_ = store.Close()
	}()

	svc := &Service{tools: tool.NewRuntime(1)}
	overlay, disabled, err := svc.compileRunToolScope(RunToolScope{Preset: "read_write"})
	if err != nil {
		t.Fatalf("compile read_write scope: %v", err)
	}
	if overlay == nil {
		t.Fatal("expected compiled overlay policy")
	}
	if disabled["ask_user"] {
		t.Fatal("ask_user should remain enabled in read_write preset")
	}
	if disabled["exit_plan_mode"] {
		t.Fatal("exit_plan_mode should remain enabled in read_write preset")
	}
	if disabled["plan_manage"] {
		t.Fatal("plan_manage should remain enabled in read_write preset")
	}
	if !disabled["bash"] {
		t.Fatal("bash should remain disabled in read_write preset")
	}

	cases := []struct {
		name      string
		mode      string
		toolName  string
		arguments string
		want      permission.AuthorizationDecision
	}{
		{name: "plan_manage stays allowed", mode: "plan", toolName: "plan_manage", arguments: `{"op":"set","title":"Plan","plan":"1. step"}`, want: permission.AuthorizationApprove},
		{name: "ask_user stays pending", mode: "plan", toolName: "ask_user", arguments: `{"prompt":"approve?"}`, want: permission.AuthorizationPending},
		{name: "exit_plan_mode stays pending", mode: "plan", toolName: "exit_plan_mode", arguments: `{"title":"Plan","plan":"1. step"}`, want: permission.AuthorizationPending},
		{name: "bash stays denied", mode: "auto", toolName: "bash", arguments: `{"command":"git status"}`, want: permission.AuthorizationDeny},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result, err := permSvc.AuthorizeToolCall(permission.AuthorizationInput{
				SessionID:     "session_scope_preset",
				RunID:         "run_scope_preset",
				CallID:        tc.name,
				ToolName:      tc.toolName,
				ToolArguments: tc.arguments,
				Mode:          tc.mode,
				Overlay:       overlay,
			})
			if err != nil {
				t.Fatalf("authorize tool call: %v", err)
			}
			if result.Decision != tc.want {
				t.Fatalf("decision = %q, want %q", result.Decision, tc.want)
			}
		})
	}
}
