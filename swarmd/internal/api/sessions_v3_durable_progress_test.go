package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type sessionsV3DurableProgressRecordingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu           sync.Mutex
	assistant    []sessionV3AssistantProgress
	phases       []string
	reasoning    []string
	providerTool []string
}

func newSessionsV3DurableProgressRecordingWriter(block bool) *sessionsV3DurableProgressRecordingWriter {
	w := &sessionsV3DurableProgressRecordingWriter{}
	if block {
		w.entered = make(chan struct{})
		w.release = make(chan struct{})
	}
	return w
}

func (w *sessionsV3DurableProgressRecordingWriter) maybeBlock() {
	if w == nil || w.entered == nil {
		return
	}
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.phases = append(w.phases, eventType)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.assistant = append(w.assistant, progress)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, summary string) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reasoning = append(w.reasoning, eventType+":"+delta+":"+summary)
	return sessionruntime.SessionMutationResult{}, nil
}

func (w *sessionsV3DurableProgressRecordingWriter) RecordProviderToolConstructionEvent(job sessionV3ExecutorJob, event provideriface.StreamEvent, step int, eventIndex int) (sessionruntime.SessionMutationResult, error) {
	w.maybeBlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.providerTool = append(w.providerTool, string(event.Type))
	return sessionruntime.SessionMutationResult{}, nil
}

func TestV3DurableProgressSinkCoalescesTenThousandDeltasByStream(t *testing.T) {
	writer := newSessionsV3DurableProgressRecordingWriter(true)
	sink := newSessionV3DurableProgressSinkWithWriter(&sessionV3Executor{deltaFlushMaxBytes: 1 << 20, deltaFlushMaxDelay: time.Hour}, sessionV3ExecutorJob{}, func() {}, writer)
	progress := sessionV3AssistantProgress{StreamID: "assistant:run:step:1", Step: 1, StepID: "step-1"}
	for i := 0; i < 10000; i++ {
		progress.LiveSeqStart = uint64(i + 1)
		progress.LiveSeqEnd = uint64(i + 1)
		progress.OffsetStart = uint64(i)
		progress.OffsetEnd = uint64(i + 1)
		progress.Text = "x"
		if i%10 == 9 {
			progress.Text = "\n"
		}
		if err := sink.TryAppendAssistant(progress); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	barrierDone := make(chan error, 1)
	go func() { barrierDone <- sink.FlushBarrier(context.Background()) }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatalf("writer did not enter after flush barrier")
	}
	snap := sink.snapshotForTest()
	if snap.CurrentAssistantKeys+snap.SealedAssistant+snap.InFlightAssistant > 3 {
		t.Fatalf("assistant aggregate count snapshot = %+v, want bounded small constant", snap)
	}
	if snap.PendingBytes+snap.InFlightBytes != 10000 {
		t.Fatalf("bytes snapshot = %+v, want 10000", snap)
	}
	select {
	case <-writer.release:
		t.Fatalf("writer release channel should not be closed before snapshot")
	default:
	}
	for stream := 0; stream < 100; stream++ {
		for i := 0; i < 100; i++ {
			p := sessionV3AssistantProgress{StreamID: fmt.Sprintf("assistant:run:synthetic:%03d", stream), Step: 1, StepID: "step-1", LiveSeqStart: uint64(i + 1), LiveSeqEnd: uint64(i + 1), OffsetStart: uint64(i), OffsetEnd: uint64(i + 1), Text: "y"}
			if err := sink.TryAppendAssistant(p); err != nil {
				t.Fatalf("synthetic stream %d append %d: %v", stream, i, err)
			}
		}
	}
	snap = sink.snapshotForTest()
	if snap.CurrentAssistantKeys > 101 {
		t.Fatalf("current assistant keys = %d, want <=101 snapshot=%+v", snap.CurrentAssistantKeys, snap)
	}
	close(writer.release)
	if err := <-barrierDone; err != nil {
		t.Fatalf("flush barrier: %v", err)
	}
	if err := sink.CloseAndFlush(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}
}

func TestV3DurableProgressBacklogFailsRunWithoutBlocking(t *testing.T) {
	cancelled := make(chan struct{})
	writer := newSessionsV3DurableProgressRecordingWriter(true)
	sink := newSessionV3DurableProgressSinkWithWriter(&sessionV3Executor{deltaFlushMaxBytes: 1 << 20, deltaFlushMaxDelay: time.Hour}, sessionV3ExecutorJob{}, func() { close(cancelled) }, writer)
	text := strings.Repeat("x", 1024)
	var err error
	for i := 0; i < 1100; i++ {
		err = sink.TryAppendAssistant(sessionV3AssistantProgress{StreamID: "assistant:run:step:1", Step: 1, StepID: "step-1", LiveSeqStart: uint64(i + 1), LiveSeqEnd: uint64(i + 1), OffsetStart: uint64(i * 1024), OffsetEnd: uint64((i + 1) * 1024), Text: text})
		if err != nil {
			break
		}
	}
	if !errors.Is(err, ErrSessionV3DurableProgressBacklog) {
		t.Fatalf("overflow error = %v, want backlog", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatalf("provider context was not cancelled")
	}
}

func TestV3DurableProgressWriterRunsWithoutHoldingSinkMutex(t *testing.T) {
	writer := newSessionsV3DurableProgressRecordingWriter(true)
	sink := newSessionV3DurableProgressSinkWithWriter(&sessionV3Executor{deltaFlushMaxBytes: 1, deltaFlushMaxDelay: time.Hour}, sessionV3ExecutorJob{}, func() {}, writer)
	if err := sink.TryAppendAssistant(sessionV3AssistantProgress{StreamID: "assistant:run:step:1", Step: 1, StepID: "step-1", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 1, Text: "x"}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatalf("writer did not block")
	}
	done := make(chan error, 1)
	go func() {
		done <- sink.TryAppendAssistant(sessionV3AssistantProgress{StreamID: "assistant:run:step:1", Step: 1, StepID: "step-1", LiveSeqStart: 2, LiveSeqEnd: 2, OffsetStart: 1, OffsetEnd: 2, Text: "y"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("TryAppendAssistant blocked behind writer")
	}
	close(writer.release)
	if err := sink.CloseAndFlush(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}
}

func TestV3DurableProgressCloseEnqueueRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		writer := newSessionsV3DurableProgressRecordingWriter(false)
		sink := newSessionV3DurableProgressSinkWithWriter(&sessionV3Executor{deltaFlushMaxBytes: 1 << 20, deltaFlushMaxDelay: time.Hour}, sessionV3ExecutorJob{}, func() {}, writer)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = sink.TryAppendAssistant(sessionV3AssistantProgress{StreamID: "assistant:run:step:1", Step: 1, StepID: "step-1", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 1, Text: "x"})
		}()
		go func() {
			defer wg.Done()
			_ = sink.CloseAndFlush(context.Background())
		}()
		wg.Wait()
		_ = sink.CloseAndFlush(context.Background())
	}
}
