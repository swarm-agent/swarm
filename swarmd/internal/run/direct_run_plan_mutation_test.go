package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type directRunPlanMutationProvider struct {
	calls int
}

func (r *directRunPlanMutationProvider) ID() string { return "fake" }

func (r *directRunPlanMutationProvider) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *directRunPlanMutationProvider) CreateResponseStreaming(_ context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.calls++
	if r.calls == 1 {
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{
			CallID:    "call-plan",
			Name:      "plan_manage",
			Arguments: `{"action":"start_session_checkpoint","change_request":"prove direct plan persistence","checkpoint_title":"Direct plan persistence","tasks":["Persist the checkpoint"],"acceptance_criteria":["The checkpoint is durably active"],"notes":"Focused direct-run regression"}`,
		}}}, nil
	}
	return provideriface.Response{Text: "checkpoint started"}, nil
}
