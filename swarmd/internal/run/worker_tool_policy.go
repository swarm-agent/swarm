package run

import (
	"context"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// UnattendedPermissionDeniedError ends an unattended worker run whose tool
// call would need an interactive approval nobody can give. It carries the
// permission service's refusal so the run fails at once with that reason,
// instead of the model reporting itself blocked.
type UnattendedPermissionDeniedError struct {
	Tool   string
	Reason string
}

func (e *UnattendedPermissionDeniedError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = strings.TrimSpace(e.Tool) + " denied: unattended worker jobs cannot request interactive permission"
	}
	return reason
}

// WorkerToolPolicyOverlay returns the permission overlay of a worker job's run
// on the V3 provider path, and nil for any other session. The worker's own
// compiled tool contract grants the tools it enables (bash in its sandbox
// included), so a worker run does not depend on the daemon-wide bypass. It is
// the same for every run of the job: its first run, an owner follow-up and a
// workflow reopen.
//
// Precedence: explicit deny rules always win (they are evaluated before any
// allow), the account bash profile is evaluated before explicit rules, and the
// account's own rules come before the worker's grants, so an account allow or
// ask rule decides before a worker grant does. Tools the contract disables are
// not repeated here: the step's offered-tool set already refuses them.
func (s *Service) WorkerToolPolicyOverlay(accountScopeID, sessionID string, profile pebblestore.AgentProfile) (*permission.Policy, error) {
	if s == nil || s.permissions == nil {
		return nil, nil
	}
	worker, err := s.permissions.WorkerRunSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !worker {
		return nil, nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	states, knownTools, err := s.resolveValidatedStoredV3AgentToolStates(accountScopeID, profile)
	if err != nil {
		return nil, err
	}
	// The account policy is placed ahead of the grants below, so the
	// contract's own copy of it is not needed.
	states.InheritPolicy = false
	_, contract, _, err := s.compileAgentToolPolicy(accountScopeID, profile, states, knownTools)
	if err != nil {
		return nil, err
	}
	grants := permission.Policy{Version: 1}
	if contract != nil {
		for _, rule := range contract.Rules {
			if rule.Decision == permission.PolicyDecisionDeny {
				continue
			}
			grants.Rules = append(grants.Rules, rule)
		}
	}
	account, err := s.permissions.CurrentPolicyForAccount(accountScopeID)
	if err != nil {
		return nil, err
	}
	merged := mergePermissionPolicies(&account, &grants)
	return &merged, nil
}

// unattendedDenialKey marks a provider-managed call whose run must end when the
// permission gate refuses it as an unattended worker prompt.
type unattendedDenialKey struct{}

type unattendedDenialRecorder struct {
	mu     sync.Mutex
	denial *UnattendedPermissionDeniedError
}

func withUnattendedDenialRecorder(ctx context.Context, recorder *unattendedDenialRecorder) context.Context {
	return context.WithValue(ctx, unattendedDenialKey{}, recorder)
}

// recordUnattendedDenial keeps the first worker_noninteractive refusal of the
// call for a run that asked to fail on one. ask_user is an input boundary, not
// a tool permission: its refusal stays an ordinary tool result.
func recordUnattendedDenial(ctx context.Context, toolName string, auth permission.AuthorizationResult) {
	if ctx == nil || auth.Decision != permission.AuthorizationDeny || auth.Source != permission.WorkerNonInteractiveSource {
		return
	}
	if canonicalToolName(toolName) == "ask_user" {
		return
	}
	recorder, ok := ctx.Value(unattendedDenialKey{}).(*unattendedDenialRecorder)
	if !ok || recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.denial == nil {
		recorder.denial = &UnattendedPermissionDeniedError{Tool: strings.TrimSpace(toolName), Reason: strings.TrimSpace(auth.Reason)}
	}
}

func (r *unattendedDenialRecorder) recorded() *UnattendedPermissionDeniedError {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.denial
}
