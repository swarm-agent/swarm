package api

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/flow"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	flowSchedulerInterval  = 15 * time.Second
	flowSchedulerTickLimit = 25
	flowSchedulerRunLease  = 30 * time.Minute
)

func (s *Server) StartFlowScheduler(ctx context.Context) {
	if s == nil || s.flows == nil || s.runner == nil || s.sessions == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scheduler := flow.Scheduler{
		Store:    pebblestore.NewFlowSchedulerStore(s.flows),
		Runner:   s.NewTargetLocalFlowRunner(),
		LeaseFor: flowSchedulerRunLease,
	}
	s.runFlowSchedulerTick(ctx, scheduler)
	ticker := time.NewTicker(flowSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runFlowSchedulerTick(ctx, scheduler)
		}
	}
}

func (s *Server) RunFlowSchedulerTick(ctx context.Context, limit int) ([]flow.RunStart, error) {
	if s == nil || s.flows == nil {
		return nil, errors.New("flow store is not configured")
	}
	if s.runner == nil {
		return nil, errors.New("run service not configured")
	}
	if s.sessions == nil {
		return nil, errors.New("session service not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scheduler := flow.Scheduler{
		Store:    pebblestore.NewFlowSchedulerStore(s.flows),
		Runner:   s.NewTargetLocalFlowRunner(),
		LeaseFor: flowSchedulerRunLease,
	}
	return scheduler.Tick(ctx, limit)
}

func (s *Server) runFlowSchedulerTick(ctx context.Context, scheduler flow.Scheduler) {
	if err := ctx.Err(); err != nil {
		return
	}
	starts, err := scheduler.Tick(ctx, flowSchedulerTickLimit)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			log.Printf("warning: flow scheduler tick failed: %v", err)
		}
		return
	}
	if len(starts) > 0 {
		log.Printf("flow scheduler launched %d run(s)", len(starts))
	}
}
