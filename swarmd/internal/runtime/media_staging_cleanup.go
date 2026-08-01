package runtime

import (
	"context"
	"log"
	"time"

	"swarm/packages/swarmd/internal/mediastaging"
)

const (
	mediaStagingCleanupInitialDelay = time.Minute
	mediaStagingCleanupInterval     = 5 * time.Minute
)

type mediaStagingCleanupPass func(nowUnixMs int64, limit int) (mediastaging.CleanupReport, error)

type mediaStagingCleanupTimer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type mediaStagingCleanupTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realMediaStagingCleanupTimer struct{ timer *time.Timer }

func (timer realMediaStagingCleanupTimer) Chan() <-chan time.Time { return timer.timer.C }
func (timer realMediaStagingCleanupTimer) Stop() bool             { return timer.timer.Stop() }

type realMediaStagingCleanupTicker struct{ ticker *time.Ticker }

func (ticker realMediaStagingCleanupTicker) Chan() <-chan time.Time { return ticker.ticker.C }
func (ticker realMediaStagingCleanupTicker) Stop()                  { ticker.ticker.Stop() }

type mediaStagingCleanupSchedule struct {
	InitialDelay time.Duration
	Interval     time.Duration
	Limit        int
	Now          func() time.Time
	Pass         mediaStagingCleanupPass
	Logf         func(string, ...any)
	NewTimer     func(time.Duration) mediaStagingCleanupTimer
	NewTicker    func(time.Duration) mediaStagingCleanupTicker
}

// startMediaStagingCleanup starts one cancellable scheduler. Production
// composition should pass the account-scoped staging service created over the
// daemon's canonical Pebble store.
func startMediaStagingCleanup(ctx context.Context, staging *mediastaging.Service) {
	if staging == nil {
		return
	}
	schedule := mediaStagingCleanupSchedule{
		InitialDelay: mediaStagingCleanupInitialDelay,
		Interval:     mediaStagingCleanupInterval,
		Limit:        mediastaging.DefaultCleanupLimit,
		Now:          time.Now,
		Pass:         staging.CleanupExpired,
		Logf:         log.Printf,
		NewTimer: func(delay time.Duration) mediaStagingCleanupTimer {
			return realMediaStagingCleanupTimer{timer: time.NewTimer(delay)}
		},
		NewTicker: func(interval time.Duration) mediaStagingCleanupTicker {
			return realMediaStagingCleanupTicker{ticker: time.NewTicker(interval)}
		},
	}
	go runMediaStagingCleanupSchedule(ctx, schedule)
}

func runMediaStagingCleanupSchedule(ctx context.Context, schedule mediaStagingCleanupSchedule) {
	if ctx == nil || schedule.Pass == nil || schedule.Now == nil || schedule.NewTimer == nil || schedule.NewTicker == nil ||
		schedule.InitialDelay <= 0 || schedule.Interval <= 0 || schedule.Limit <= 0 || schedule.Limit > mediastaging.MaximumCleanupLimit {
		if schedule.Logf != nil {
			schedule.Logf("warning: media staging cleanup scheduler is not configured")
		}
		return
	}
	logf := schedule.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	run := func() {
		report, err := schedule.Pass(schedule.Now().UnixMilli(), schedule.Limit)
		if err != nil {
			if ctx.Err() == nil {
				logf("warning: media staging cleanup pass failed: candidates=%d expired=%d bound=%d terminal=%d not_found=%d failed=%d: %v", report.Candidates, report.Expired, report.Bound, report.AlreadyTerminal, report.NotFound, report.Failed, err)
			}
			return
		}
		if report.Candidates > 0 {
			logf("media staging cleanup pass: candidates=%d expired=%d bound=%d terminal=%d not_found=%d more=%t", report.Candidates, report.Expired, report.Bound, report.AlreadyTerminal, report.NotFound, report.More)
		}
	}

	initial := schedule.NewTimer(schedule.InitialDelay)
	select {
	case <-ctx.Done():
		initial.Stop()
		return
	case <-initial.Chan():
		run()
	}

	ticker := schedule.NewTicker(schedule.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			run()
		}
	}
}
