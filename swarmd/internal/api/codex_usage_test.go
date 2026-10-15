package api

import (
	"context"

	"swarm/packages/swarmd/internal/provider/codex"
)

type fakeCodexAccountClient struct {
	consume      codex.ConsumeResetCreditRequest
	usageErr     error
	resetCredits codex.ResetCredits
}

func (f *fakeCodexAccountClient) GetAccountUsage(context.Context) (codex.AccountUsage, error) {
	return codex.AccountUsage{PlanType: "plus"}, f.usageErr
}
func (f *fakeCodexAccountClient) GetResetCredits(context.Context) (codex.ResetCredits, error) {
	if f.resetCredits.Credits == nil {
		return codex.ResetCredits{AvailableCount: 1}, nil
	}
	return f.resetCredits, nil
}
func (f *fakeCodexAccountClient) ConsumeResetCredit(_ context.Context, req codex.ConsumeResetCreditRequest) (codex.ConsumeResetCreditResponse, error) {
	f.consume = req
	return codex.ConsumeResetCreditResponse{Code: "reset", WindowsReset: 2}, nil
}
