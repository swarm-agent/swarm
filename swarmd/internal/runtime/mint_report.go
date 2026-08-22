package runtime

import (
	"context"
	"log"

	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

type pendingMintReporter interface {
	ReportPending(context.Context) error
}

func startMintReport(ctx context.Context, service *swarmruntime.Service) {
	startPendingMintReport(ctx, swarmruntime.NewMintReporter(service))
}

func startPendingMintReport(ctx context.Context, reporter pendingMintReporter) {
	if ctx == nil || reporter == nil {
		return
	}
	go func() {
		// Reporting is deliberately best-effort. The pending marker remains
		// durable on every error so a later daemon start can retry.
		if err := reporter.ReportPending(ctx); err != nil && ctx.Err() == nil {
			log.Printf("warning: anonymous swarm mint report remains pending: %v", err)
		}
	}()
}
