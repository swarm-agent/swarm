package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/mediastaging"
)

type cleanupTestTimer struct {
	channel chan time.Time
	stopped bool
}

func (timer *cleanupTestTimer) Chan() <-chan time.Time { return timer.channel }
func (timer *cleanupTestTimer) Stop() bool {
	timer.stopped = true
	return true
}

type cleanupTestTicker struct {
	channel chan time.Time
	stopped bool
}

func (ticker *cleanupTestTicker) Chan() <-chan time.Time { return ticker.channel }
func (ticker *cleanupTestTicker) Stop()                  { ticker.stopped = true }

func TestMediaStagingCleanupScheduleIsBoundedAndCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initial := &cleanupTestTimer{channel: make(chan time.Time, 1)}
	ticker := &cleanupTestTicker{channel: make(chan time.Time, 1)}
	passCalled := make(chan struct{}, 2)
	var limits []int
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMediaStagingCleanupSchedule(ctx, mediaStagingCleanupSchedule{
			InitialDelay: time.Second,
			Interval:     time.Minute,
			Limit:        7,
			Now:          func() time.Time { return time.UnixMilli(1234) },
			Pass: func(nowUnixMs int64, limit int) (mediastaging.CleanupReport, error) {
				if nowUnixMs != 1234 {
					t.Errorf("now=%d", nowUnixMs)
				}
				limits = append(limits, limit)
				passCalled <- struct{}{}
				return mediastaging.CleanupReport{}, nil
			},
			NewTimer:  func(time.Duration) mediaStagingCleanupTimer { return initial },
			NewTicker: func(time.Duration) mediaStagingCleanupTicker { return ticker },
		})
	}()
	initial.channel <- time.Now()
	<-passCalled
	ticker.channel <- time.Now()
	<-passCalled
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
	if len(limits) != 2 || limits[0] != 7 || limits[1] != 7 || !ticker.stopped {
		t.Fatalf("limits=%v ticker_stopped=%v", limits, ticker.stopped)
	}
}

func TestMediaStagingCleanupScheduleLogsPassFailureHonestly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	initial := &cleanupTestTimer{channel: make(chan time.Time, 1)}
	ticker := &cleanupTestTicker{channel: make(chan time.Time)}
	logged := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMediaStagingCleanupSchedule(ctx, mediaStagingCleanupSchedule{
			InitialDelay: time.Second,
			Interval:     time.Minute,
			Limit:        1,
			Now:          time.Now,
			Pass: func(int64, int) (mediastaging.CleanupReport, error) {
				return mediastaging.CleanupReport{Candidates: 1, Failed: 1}, errors.New("storage unavailable")
			},
			Logf: func(format string, args ...any) {
				logged <- fmt.Sprintf(format, args...)
				cancel()
			},
			NewTimer:  func(time.Duration) mediaStagingCleanupTimer { return initial },
			NewTicker: func(time.Duration) mediaStagingCleanupTicker { return ticker },
		})
	}()
	initial.channel <- time.Now()
	select {
	case message := <-logged:
		if !strings.Contains(message, "cleanup pass failed") || !strings.Contains(message, "storage unavailable") {
			t.Fatalf("log=%q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("failure was not logged")
	}
	<-done
}

func TestMediaStagingCleanupScheduleRejectsUnboundedConfiguration(t *testing.T) {
	called := false
	runMediaStagingCleanupSchedule(context.Background(), mediaStagingCleanupSchedule{
		InitialDelay: time.Second,
		Interval:     time.Second,
		Limit:        mediastaging.MaximumCleanupLimit + 1,
		Now:          time.Now,
		Pass: func(int64, int) (mediastaging.CleanupReport, error) {
			called = true
			return mediastaging.CleanupReport{}, nil
		},
		Logf:     func(string, ...any) {},
		NewTimer: func(time.Duration) mediaStagingCleanupTimer { return &cleanupTestTimer{channel: make(chan time.Time)} },
		NewTicker: func(time.Duration) mediaStagingCleanupTicker {
			return &cleanupTestTicker{channel: make(chan time.Time)}
		},
	})
	if called {
		t.Fatal("unbounded scheduler ran a pass")
	}
}
