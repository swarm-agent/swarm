package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

type captureContextInvoker struct{ ctx context.Context }

func (c *captureContextInvoker) ExecuteTool(ctx context.Context, _ provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	c.ctx = ctx
	return provideriface.ToolExecutionResult{}, nil
}

// Purpose: the permission gate may approve a path-reading bash command only
// against the exact workspace scope the call will execute in. That scope is
// bound only by the V3 provider-managed invoker (BindToolExecutionSession),
// whose permission session is the executing session; any other context, or a
// binding for a different session (a delegated child gated in its parent's
// session), must yield no scope so the command keeps asking. With the binding,
// gateToolCalls approves an in-workspace read without creating a pending
// approval, while the same authorization for a symlink escaping the workspace
// still pends. Owner: BindToolExecutionSession / permissionExecutionScope /
// gateToolCalls -> permission.Service.AuthorizeToolCall; the run service with
// a temp store and workspace is the narrowest layer that resolves the scope.
func TestPermissionExecutionScopeBindsOnlyTheExecutingSession(t *testing.T) {
	svc, sessionID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()
	session, ok, err := svc.sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("load session: ok=%t err=%v", ok, err)
	}
	workspace := session.WorkspacePath
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "data.txt"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link-out")); err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: session.AccountScopeID}

	if scope := svc.permissionExecutionScope(context.Background(), sessionID); scope != nil {
		t.Fatalf("unbound context resolved a scope: %+v", scope)
	}
	capture := &captureContextInvoker{}
	if _, err := BindToolExecutionSession(capture, "other-session", principal).ExecuteTool(context.Background(), provideriface.ToolInvocation{}); err != nil {
		t.Fatal(err)
	}
	if scope := svc.permissionExecutionScope(capture.ctx, sessionID); scope != nil {
		t.Fatalf("binding for another session resolved a scope: %+v", scope)
	}
	if same := BindToolExecutionSession(capture, sessionID, identity.Principal{}); same != provideriface.ToolInvoker(capture) {
		t.Fatal("invalid principal must not bind")
	}
	if _, err := BindToolExecutionSession(capture, sessionID, principal).ExecuteTool(context.Background(), provideriface.ToolInvocation{}); err != nil {
		t.Fatal(err)
	}
	bound := capture.ctx
	scope := svc.permissionExecutionScope(bound, sessionID)
	resolvedWorkspace, _ := filepath.EvalSymlinks(workspace)
	if scope == nil || scope.PrimaryPath != resolvedWorkspace {
		t.Fatalf("bound scope = %+v, want primary %q", scope, resolvedWorkspace)
	}

	read := `{"command":"cat README.md","explanation":["Show the readme"],"category":"read","critical":false}`
	_, approved, _, _, _, err := svc.gateToolCalls(bound, sessionID, "run-scoped-read", 1, "auto", []tool.Call{{CallID: "call-read", Name: "bash", Arguments: read}}, nil, nil)
	if err != nil || len(approved) != 1 {
		t.Fatalf("in-workspace read: approved=%d err=%v", len(approved), err)
	}
	if count, err := permissions.PendingCount(sessionID); err != nil || count != 0 {
		t.Fatalf("in-workspace read left %d pending approvals (err=%v)", count, err)
	}

	escape := `{"command":"cat link-out/data.txt","explanation":["Show data"],"category":"read","critical":false}`
	auth, err := permissions.AuthorizeToolCall(permission.AuthorizationInput{
		SessionID: sessionID, AccountScopeID: session.AccountScopeID, RunID: "run-scoped-read", CallID: "call-escape",
		ToolName: "bash", ToolArguments: escape, Mode: "auto", ExecutionScope: scope,
	})
	if err != nil || auth.Decision != permission.AuthorizationPending || auth.Record == nil {
		t.Fatalf("symlink escape = %+v, err=%v; want a pending approval", auth, err)
	}
}
