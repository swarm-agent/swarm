package api

import (
	"context"
	"errors"
	"log"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	flowOutboxDeliveryInterval = 30 * time.Second
	flowOutboxDeliveryLimit    = 25
)

func (s *Server) StartFlowOutboxDeliveryLoop(ctx context.Context) {
	if s == nil || s.flows == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.runFlowOutboxDelivery(ctx)
	ticker := time.NewTicker(flowOutboxDeliveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runFlowOutboxDelivery(ctx)
		}
	}
}

func (s *Server) runFlowOutboxDelivery(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	results, err := s.DeliverPendingFlowAssignmentCommands(ctx, flowOutboxDeliveryLimit)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("warning: flow outbox delivery failed: %v", err)
		}
		return
	}
	if len(results) == 0 {
		if pendingCount, countErr := s.flows.CountOutboxCommands(pebblestore.FlowOutboxStatusPending); countErr == nil && pendingCount > 0 {
			log.Printf("flow outbox delivery idle pending=%d", pendingCount)
		}
		return
	}
	delivered := 0
	pending := 0
	rejected := 0
	for _, result := range results {
		if result.Delivered {
			delivered++
		}
		if result.PendingSync {
			pending++
		}
		if !result.PendingSync && !result.Delivered {
			rejected++
		}
	}
	if delivered+pending+rejected > 0 {
		log.Printf("flow outbox delivery results delivered=%d pending=%d rejected=%d", delivered, pending, rejected)
	}
}
