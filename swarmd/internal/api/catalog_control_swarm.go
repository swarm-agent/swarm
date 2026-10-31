package api

import (
	"context"
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/tool"
)

// catalogControlSwarmHandlers are the swarm_* handlers a catalog action runs
// for a Swarm Control caller, so the action takes that tool's Swarm Control
// inputs (held waits) and answers its fields with the tool's own code: the
// catalog action replaces the tool (docs/tool-catalog.md).
var catalogControlSwarmHandlers = map[string]func(*controlMCPCall, map[string]any) (any, error){
	"swarm_get_project": controlMCPGetProject,
	"swarm_wait_task":   controlMCPWaitTask,
}

// controlSwarmCall is a Swarm Control caller's internal request: its own key
// narrowed to the scopes it holds (setupCall), never a conversation's.
func (s *Server) controlSwarmCall(ctx context.Context, p identity.Principal, who string) (*controlMCPCall, error) {
	held, control := tool.ControlScopesFromContext(ctx)
	if !control {
		return nil, errors.New("this option is for Swarm Control callers only")
	}
	if !p.Valid() || s.sessions == nil {
		return nil, errors.New("a Swarm Control option requires a principal")
	}
	return s.setupCall(ctx, p, who, true, held, tool.CatalogAction{})
}

// ControlSwarmAnswer runs one swarm_* handler (catalogControlSwarmHandlers)
// for a Swarm Control caller and returns its answer.
func (s *Server) ControlSwarmAnswer(ctx context.Context, p identity.Principal, who, swarmTool string, args map[string]any) (map[string]any, error) {
	handler, ok := catalogControlSwarmHandlers[swarmTool]
	if !ok {
		return nil, fmt.Errorf("%s is not served to a catalog action", swarmTool)
	}
	call, err := s.controlSwarmCall(ctx, p, who)
	if err != nil {
		return nil, err
	}
	out, err := handler(call, args)
	if err != nil {
		return nil, err
	}
	answer, _ := out.(map[string]any)
	if answer == nil {
		return nil, fmt.Errorf("%s answered no object", swarmTool)
	}
	return answer, nil
}

// ControlModelPreference checks a Swarm Control caller's model choice
// ({provider, model, thinking, fast}) as swarm_run_task does and returns the
// task's provider, model, thinking and service_tier.
func (s *Server) ControlModelPreference(ctx context.Context, p identity.Principal, who string, model map[string]any) (map[string]any, error) {
	call, err := s.controlSwarmCall(ctx, p, who)
	if err != nil {
		return nil, err
	}
	return call.modelPreference(model)
}
