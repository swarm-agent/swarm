package api

import (
	"context"

	"net/http"

	"swarm/packages/swarmd/internal/automation"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationAPIAccess struct{}

func (automationAPIAccess) Workspace(_ context.Context, p automation.Principal, s store.AutomationScope, _ string) error {
	if p.AccountID != "account" || s.WorkspaceID != "workspace" {
		return automation.ErrDenied
	}
	return nil
}

type automationAPIRuntime struct{}

func (automationAPIRuntime) Ensure(context.Context, automation.Principal, store.AutomationRecord, store.AutomationRecord) (string, error) {
	panic("HTTP must not dispatch")
}

type automationAPIEvents struct{}

func (automationAPIEvents) VerifyEvent(r *http.Request, _ automation.Principal, _ store.AutomationScope, _ string, _ uint64, _ automation.Trigger) error {
	if r.Header.Get("X-Test-Credential") != "valid" {
		return automation.ErrDenied
	}
	return nil
}
