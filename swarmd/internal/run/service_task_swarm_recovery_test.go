package run

import (
	"context"

	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// Purpose: configuredTaskSwarmRouter.Hydrate and hydrateTaskSwarmLaunches must
// recover malformed model JSON once without changing the brief or admitting a
// partial wave. Fake streaming providers prove the orchestration gate directly,
// without model credentials, child processes, rendering, or ambient session state.
type swarmRecoveryRunner struct {
	requests  []provideriface.Request
	deadlines []time.Time
	respond   func(context.Context, int, func(provideriface.StreamEvent)) (provideriface.Response, error)
}

func (r *swarmRecoveryRunner) ID() string { return "test" }
func (r *swarmRecoveryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}
func (r *swarmRecoveryRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, emit func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.requests = append(r.requests, req)
	deadline, _ := ctx.Deadline()
	r.deadlines = append(r.deadlines, deadline)
	return r.respond(ctx, len(r.requests), emit)
}
