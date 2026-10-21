package run

import (
	"context"

	"strings"

	"swarm/packages/swarmd/internal/identity"

	"swarm/packages/swarmd/internal/provider/codex"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type principalCapturingAITaskRunner struct {
	id                   string
	principal            identity.Principal
	request              provideriface.Request
	requests             []provideriface.Request
	responses            []string
	calls                int
	convertedServiceTier string
}

func (r *principalCapturingAITaskRunner) ID() string {
	if strings.TrimSpace(r.id) == "" {
		return "codex"
	}
	return r.id
}

func (r *principalCapturingAITaskRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return provideriface.Response{}, identity.ErrPrincipalRequired
	}
	r.principal, r.request = principal, req
	r.requests = append(r.requests, req)
	r.calls++
	r.convertedServiceTier = codex.ToRequest(req).ServiceTier
	response := `{"title":"Fix trusted task","worktree_name":"fix-trusted-task"}`
	if len(r.responses) >= r.calls {
		response = r.responses[r.calls-1]
	}
	return provideriface.Response{Text: response}, nil
}

func (r *principalCapturingAITaskRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.CreateResponse(ctx, req)
}
