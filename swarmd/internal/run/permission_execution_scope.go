package run

import (
	"context"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

// toolExecutionSessionKey marks a context whose gated tool calls execute in
// the named session's own resolved workspace scope.
type toolExecutionSessionKey struct{}

type toolExecutionSession struct {
	sessionID string
	principal identity.Principal
}

type executionSessionToolInvoker struct {
	inner   provideriface.ToolInvoker
	binding toolExecutionSession
}

// BindToolExecutionSession wraps a provider-managed tool invoker whose calls
// are both permission-gated and executed in sessionID (the V3 executor path,
// where the permission session is the run session). The permission gate can
// then resolve the exact workspace scope, and thus bash working directory,
// those calls run in, so path-reading bash commands confined to it need no
// approval. Callers whose permission session differs from the executing
// session, or whose execution scope can be overridden per run, must not bind.
func BindToolExecutionSession(inner provideriface.ToolInvoker, sessionID string, principal identity.Principal) provideriface.ToolInvoker {
	sessionID = strings.TrimSpace(sessionID)
	if inner == nil || sessionID == "" || !principal.Valid() {
		return inner
	}
	return executionSessionToolInvoker{inner: inner, binding: toolExecutionSession{sessionID: sessionID, principal: principal}}
}

func (i executionSessionToolInvoker) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return i.inner.ExecuteTool(context.WithValue(ctx, toolExecutionSessionKey{}, i.binding), invocation)
}

// permissionExecutionScope resolves the workspace scope in which a call gated
// for permissionSessionID will execute, exactly as provider-managed execution
// does (providerManagedWorkspaceContext -> syncWorkspaceScopeFromSession ->
// resolveRunWorkspaceScope, cwd = PrimaryPath). It returns nil, keeping the
// default approval, unless ctx carries a binding for that same session.
func (s *Service) permissionExecutionScope(ctx context.Context, permissionSessionID string) *tool.WorkspaceScope {
	if s == nil || s.sessions == nil || ctx == nil {
		return nil
	}
	binding, ok := ctx.Value(toolExecutionSessionKey{}).(toolExecutionSession)
	if !ok || binding.sessionID == "" || binding.sessionID != strings.TrimSpace(permissionSessionID) {
		return nil
	}
	session, found, err := s.sessions.GetSession(binding.sessionID)
	if err != nil || !found {
		return nil
	}
	scope, err := s.resolveRunWorkspaceScope(session, binding.principal)
	if err != nil || strings.TrimSpace(scope.PrimaryPath) == "" {
		return nil
	}
	if len(scope.Roots) == 0 {
		scope.Roots = []string{scope.PrimaryPath}
	}
	return &scope
}
