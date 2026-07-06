package api

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

var ErrSessionV3DurableProgressBacklog = errors.New("v3 durable progress backlog exceeded")

const (
	sessionV3DurableProgressMaxBytes        = 1 << 20
	sessionV3DurableProgressMaxSealedEpochs = 128
	sessionV3DurableProgressMaxControlItems = 64
)

type sessionV3AssistantProgress struct {
	StreamID     string
	Step         int
	StepID       string
	LiveSeqStart uint64
	LiveSeqEnd   uint64
	OffsetStart  uint64
	OffsetEnd    uint64
	Text         string
	RecordedAt   int64
}

type sessionV3DurableProgressWriter interface {
	RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error)
	RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error)
	RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, summary string) (sessionruntime.SessionMutationResult, error)
}

type sessionV3ExecutorDurableProgressWriter struct {
	exec *sessionV3Executor
}

func (w sessionV3ExecutorDurableProgressWriter) RecordRunPhase(job sessionV3ExecutorJob, phase RunPhase, eventType string) (sessionruntime.SessionMutationResult, error) {
	return w.exec.recordRunPhase(job, phase, eventType)
}

func (w sessionV3ExecutorDurableProgressWriter) RecordRunProgress(job sessionV3ExecutorJob, progress sessionV3AssistantProgress, deltaIndex int) (sessionruntime.SessionMutationResult, error) {
	return w.exec.recordRunProgress(job, progress, deltaIndex)
}

func (w sessionV3ExecutorDurableProgressWriter) RecordReasoningEvent(job sessionV3ExecutorJob, eventType string, step int, eventIndex int, reasoningKey string, delta string, summary string) (sessionruntime.SessionMutationResult, error) {
	return w.exec.recordReasoningEvent(job, eventType, step, eventIndex, reasoningKey, delta, summary)
}

type sessionV3AssistantAcceptedEnd struct {
	LiveSeqEnd uint64
	OffsetEnd  uint64
}

type sessionV3AssistantProgressAggregate struct {
	FirstOrder     uint64
	StreamID       string
	Step           int
	StepID         string
	LiveSeqStart   uint64
	LiveSeqEnd     uint64
	OffsetStart    uint64
	OffsetEnd      uint64
	RecordedAt     int64
	Text           bytes.Buffer
	Bytes          int
	FirstPendingAt time.Time
	FlushRequested bool
}

type sessionV3ReasoningProgressAggregate struct {
	FirstOrder     uint64
	Step           int
	ReasoningKey   string
	Snapshot       string
	Bytes          int
	FirstPendingAt time.Time
	FlushRequested bool
}

type sessionV3DurableProgressItemKind string

const (
	sessionV3DurableProgressItemPhase             sessionV3DurableProgressItemKind = "phase"
	sessionV3DurableProgressItemAssistant         sessionV3DurableProgressItemKind = "assistant"
	sessionV3DurableProgressItemReasoningDelta    sessionV3DurableProgressItemKind = "reasoning_delta"
	sessionV3DurableProgressItemReasoningStarted  sessionV3DurableProgressItemKind = "reasoning_started"
	sessionV3DurableProgressItemReasoningComplete sessionV3DurableProgressItemKind = "reasoning_completed"
)

type sessionV3DurableProgressItem struct {
	Order        uint64
	Kind         sessionV3DurableProgressItemKind
	Phase        RunPhase
	EventType    string
	Assistant    *sessionV3AssistantProgressAggregate
	Reasoning    *sessionV3ReasoningProgressAggregate
	Step         int
	ReasoningKey string
	Summary      string
}

type sessionV3DurableProgressEpoch struct {
	EpochID      uint64
	Items        []sessionV3DurableProgressItem
	Bytes        int
	ControlItems int
}

type sessionV3DurableProgressSink struct {
	mu sync.Mutex

	exec           *sessionV3Executor
	job            sessionV3ExecutorJob
	cancelProvider context.CancelFunc
	writer         sessionV3DurableProgressWriter

	currentAssistantByStream map[string]*sessionV3AssistantProgressAggregate
	currentReasoningByKey    map[string]*sessionV3ReasoningProgressAggregate
	acceptedAssistantEnd     map[string]sessionV3AssistantAcceptedEnd
	sealedEpochs             []sessionV3DurableProgressEpoch
	waiters                  map[uint64][]chan error

	pendingBytes  int
	inFlightBytes int
	controlItems  int

	nextOrder                  uint64
	nextEpochID                uint64
	committedEpochID           uint64
	nextAssistantDeltaIndex    int
	reasoningDeltaIndexByKey   map[string]int
	phaseRecordedByEventType   map[string]bool
	assistantPersistedCount    int
	reasoningPersistedCount    int
	lastWorkerObservedActivity time.Time

	notify     chan struct{}
	workerDone chan struct{}
	closed     bool
	firstErr   error
	cancelOnce sync.Once
}

func newSessionV3DurableProgressSink(exec *sessionV3Executor, job sessionV3ExecutorJob, cancelProvider context.CancelFunc) *sessionV3DurableProgressSink {
	return newSessionV3DurableProgressSinkWithWriter(exec, job, cancelProvider, sessionV3ExecutorDurableProgressWriter{exec: exec})
}

func newSessionV3DurableProgressSinkWithWriter(exec *sessionV3Executor, job sessionV3ExecutorJob, cancelProvider context.CancelFunc, writer sessionV3DurableProgressWriter) *sessionV3DurableProgressSink {
	if cancelProvider == nil {
		cancelProvider = func() {}
	}
	s := &sessionV3DurableProgressSink{
		exec:                     exec,
		job:                      job,
		cancelProvider:           cancelProvider,
		writer:                   writer,
		currentAssistantByStream: make(map[string]*sessionV3AssistantProgressAggregate),
		currentReasoningByKey:    make(map[string]*sessionV3ReasoningProgressAggregate),
		acceptedAssistantEnd:     make(map[string]sessionV3AssistantAcceptedEnd),
		waiters:                  make(map[uint64][]chan error),
		reasoningDeltaIndexByKey: make(map[string]int),
		phaseRecordedByEventType: make(map[string]bool),
		notify:                   make(chan struct{}, 1),
		workerDone:               make(chan struct{}),
	}
	go s.worker()
	return s
}

func (s *sessionV3DurableProgressSink) TryRecordPhase(phase RunPhase, eventType string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	eventType = strings.TrimSpace(eventType)
	if s.phaseRecordedByEventType[eventType] {
		return nil
	}
	if err := s.reserveControlLocked(1); err != nil {
		return err
	}
	s.phaseRecordedByEventType[eventType] = true
	s.sealAllCurrentLocked()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	if err := s.reserveSealedEpochsLocked(1); err != nil {
		return err
	}
	s.addControlEpochLocked(sessionV3DurableProgressItem{Kind: sessionV3DurableProgressItemPhase, Phase: phase, EventType: eventType})
	s.signalLocked()
	return nil
}

func (s *sessionV3DurableProgressSink) TryAppendAssistant(progress sessionV3AssistantProgress) error {
	if s == nil || progress.Text == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	progress.StreamID = strings.TrimSpace(progress.StreamID)
	if progress.StreamID == "" {
		err := errors.New("v3 assistant progress stream_id is required")
		s.failLocked(err)
		return err
	}
	prior := s.acceptedAssistantEnd[progress.StreamID]
	if prior.LiveSeqEnd != 0 || prior.OffsetEnd != 0 {
		if progress.LiveSeqStart != prior.LiveSeqEnd+1 || progress.OffsetStart != prior.OffsetEnd {
			err := errors.New("v3 assistant progress discontinuity")
			s.failLocked(err)
			return err
		}
	} else if progress.LiveSeqStart != 1 || progress.OffsetStart != 0 {
		err := errors.New("v3 assistant progress must start at sequence 1 and offset 0")
		s.failLocked(err)
		return err
	}
	additionalBytes := len([]byte(progress.Text))
	if err := s.reserveBytesLocked(additionalBytes); err != nil {
		return err
	}
	agg := s.currentAssistantByStream[progress.StreamID]
	if agg == nil {
		s.nextOrder++
		agg = &sessionV3AssistantProgressAggregate{
			FirstOrder:     s.nextOrder,
			StreamID:       progress.StreamID,
			Step:           progress.Step,
			StepID:         progress.StepID,
			LiveSeqStart:   progress.LiveSeqStart,
			OffsetStart:    progress.OffsetStart,
			FirstPendingAt: time.Now(),
		}
		s.currentAssistantByStream[progress.StreamID] = agg
	}
	agg.LiveSeqEnd = progress.LiveSeqEnd
	agg.OffsetEnd = progress.OffsetEnd
	agg.RecordedAt = progress.RecordedAt
	_, _ = agg.Text.WriteString(progress.Text)
	agg.Bytes += additionalBytes
	s.pendingBytes += additionalBytes
	s.acceptedAssistantEnd[progress.StreamID] = sessionV3AssistantAcceptedEnd{LiveSeqEnd: progress.LiveSeqEnd, OffsetEnd: progress.OffsetEnd}
	if agg.Bytes >= s.assistantFlushMaxBytesLocked() || strings.Contains(progress.Text, "\n") {
		agg.FlushRequested = true
	}
	s.signalLocked()
	return nil
}

func (s *sessionV3DurableProgressSink) TryStartReasoning(step int, reasoningKey string) error {
	if s == nil {
		return nil
	}
	reasoningKey = sessionV3NormalizeReasoningKey(reasoningKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	if err := s.reserveControlLocked(1); err != nil {
		return err
	}
	s.sealAllCurrentLocked()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	if err := s.reserveSealedEpochsLocked(1); err != nil {
		return err
	}
	s.addControlEpochLocked(sessionV3DurableProgressItem{Kind: sessionV3DurableProgressItemReasoningStarted, EventType: "session.reasoning.started", Step: step, ReasoningKey: reasoningKey})
	s.signalLocked()
	return nil
}

func (s *sessionV3DurableProgressSink) TryReplaceReasoning(step int, reasoningKey string, snapshot string) error {
	if s == nil {
		return nil
	}
	reasoningKey = sessionV3NormalizeReasoningKey(reasoningKey)
	snapshot = strings.TrimSpace(snapshot)
	if snapshot == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	agg := s.currentReasoningByKey[reasoningKey]
	oldBytes := 0
	if agg != nil {
		oldBytes = agg.Bytes
	}
	newBytes := len([]byte(snapshot))
	additionalBytes := newBytes - oldBytes
	if additionalBytes < 0 {
		additionalBytes = 0
	}
	if err := s.reserveBytesLocked(additionalBytes); err != nil {
		return err
	}
	if agg == nil {
		s.nextOrder++
		agg = &sessionV3ReasoningProgressAggregate{FirstOrder: s.nextOrder, Step: step, ReasoningKey: reasoningKey, FirstPendingAt: time.Now()}
		s.currentReasoningByKey[reasoningKey] = agg
	}
	s.pendingBytes += newBytes - agg.Bytes
	agg.Snapshot = snapshot
	agg.Bytes = newBytes
	if agg.Bytes >= s.reasoningFlushMaxBytesLocked() {
		agg.FlushRequested = true
	}
	s.signalLocked()
	return nil
}

func (s *sessionV3DurableProgressSink) TryCompleteReasoning(step int, reasoningKey string, summary string) error {
	if s == nil {
		return nil
	}
	reasoningKey = sessionV3NormalizeReasoningKey(reasoningKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	if err := s.reserveControlLocked(1); err != nil {
		return err
	}
	s.sealAllCurrentLocked()
	if err := s.callbackErrLocked(); err != nil {
		return err
	}
	if err := s.reserveSealedEpochsLocked(1); err != nil {
		return err
	}
	s.addControlEpochLocked(sessionV3DurableProgressItem{Kind: sessionV3DurableProgressItemReasoningComplete, EventType: "session.reasoning.completed", Step: step, ReasoningKey: reasoningKey, Summary: strings.TrimSpace(summary)})
	s.signalLocked()
	return nil
}

func (s *sessionV3DurableProgressSink) FlushBarrier(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.firstErr != nil {
		err := s.firstErr
		s.mu.Unlock()
		return err
	}
	s.sealAllCurrentLocked()
	if s.firstErr != nil {
		err := s.firstErr
		s.mu.Unlock()
		return err
	}
	target := s.nextEpochID
	if target == 0 || (s.pendingBytes == 0 && s.inFlightBytes == 0 && len(s.sealedEpochs) == 0) || s.committedEpochID >= target {
		s.mu.Unlock()
		return nil
	}
	waiter := make(chan error, 1)
	s.waiters[target] = append(s.waiters[target], waiter)
	s.signalLocked()
	s.mu.Unlock()
	select {
	case err := <-waiter:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sessionV3DurableProgressSink) CloseAndFlush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	err := s.closeAndBarrier(ctx)
	select {
	case <-s.workerDone:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

func (s *sessionV3DurableProgressSink) closeAndBarrier(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	s.closed = true
	if s.firstErr != nil {
		err := s.firstErr
		s.signalLocked()
		s.mu.Unlock()
		return err
	}
	s.sealAllCurrentLocked()
	if s.firstErr != nil {
		err := s.firstErr
		s.signalLocked()
		s.mu.Unlock()
		return err
	}
	target := s.nextEpochID
	if target == 0 || (s.pendingBytes == 0 && s.inFlightBytes == 0 && len(s.sealedEpochs) == 0) || s.committedEpochID >= target {
		s.signalLocked()
		s.mu.Unlock()
		return nil
	}
	waiter := make(chan error, 1)
	s.waiters[target] = append(s.waiters[target], waiter)
	s.signalLocked()
	s.mu.Unlock()
	select {
	case err := <-waiter:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sessionV3DurableProgressSink) Err() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

func (s *sessionV3DurableProgressSink) AssistantFlushCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assistantPersistedCount
}

func (s *sessionV3DurableProgressSink) callbackErrLocked() error {
	if s.firstErr != nil {
		return s.firstErr
	}
	if s.closed {
		return errors.New("v3 durable progress sink is closed")
	}
	return nil
}

func (s *sessionV3DurableProgressSink) reserveBytesLocked(additionalBytes int) error {
	if additionalBytes <= 0 {
		return nil
	}
	if s.pendingBytes+s.inFlightBytes+additionalBytes > sessionV3DurableProgressMaxBytes {
		s.failLocked(ErrSessionV3DurableProgressBacklog)
		return ErrSessionV3DurableProgressBacklog
	}
	return nil
}

func (s *sessionV3DurableProgressSink) reserveControlLocked(additional int) error {
	if s.controlItems+additional > sessionV3DurableProgressMaxControlItems {
		s.failLocked(ErrSessionV3DurableProgressBacklog)
		return ErrSessionV3DurableProgressBacklog
	}
	return nil
}

func (s *sessionV3DurableProgressSink) reserveSealedEpochsLocked(additional int) error {
	if s.sealedControlEpochsLocked()+additional > sessionV3DurableProgressMaxSealedEpochs {
		s.failLocked(ErrSessionV3DurableProgressBacklog)
		return ErrSessionV3DurableProgressBacklog
	}
	return nil
}

func (s *sessionV3DurableProgressSink) sealedControlEpochsLocked() int {
	count := 0
	for _, epoch := range s.sealedEpochs {
		if epoch.ControlItems > 0 {
			count++
		}
	}
	return count
}

func (s *sessionV3DurableProgressSink) addControlEpochLocked(item sessionV3DurableProgressItem) {
	s.nextOrder++
	item.Order = s.nextOrder
	s.nextEpochID++
	s.sealedEpochs = append(s.sealedEpochs, sessionV3DurableProgressEpoch{EpochID: s.nextEpochID, Items: []sessionV3DurableProgressItem{item}, ControlItems: 1})
	s.controlItems++
}

func (s *sessionV3DurableProgressSink) addByteBackedEpochLocked(item sessionV3DurableProgressItem, bytesInEpoch int) {
	if bytesInEpoch <= 0 {
		bytesInEpoch = 1
	}
	s.nextOrder++
	item.Order = s.nextOrder
	s.nextEpochID++
	s.sealedEpochs = append(s.sealedEpochs, sessionV3DurableProgressEpoch{EpochID: s.nextEpochID, Items: []sessionV3DurableProgressItem{item}, Bytes: bytesInEpoch})
	s.pendingBytes += bytesInEpoch
}

func (s *sessionV3DurableProgressSink) sealAllCurrentLocked() {
	s.sealMatchingCurrentLocked(func(_ time.Time, _ bool) bool { return true })
}

func (s *sessionV3DurableProgressSink) sealDueCurrentLocked(now time.Time) {
	s.sealMatchingCurrentLocked(func(first time.Time, flushRequested bool) bool {
		if flushRequested {
			return true
		}
		if first.IsZero() {
			return false
		}
		return now.Sub(first) >= s.minFlushDelayLocked()
	})
}

func (s *sessionV3DurableProgressSink) sealMatchingCurrentLocked(shouldSeal func(time.Time, bool) bool) {
	items := make([]sessionV3DurableProgressItem, 0, len(s.currentAssistantByStream)+len(s.currentReasoningByKey))
	bytesInEpoch := 0
	for key, agg := range s.currentAssistantByStream {
		if agg == nil || !shouldSeal(agg.FirstPendingAt, agg.FlushRequested) {
			continue
		}
		items = append(items, sessionV3DurableProgressItem{Order: agg.FirstOrder, Kind: sessionV3DurableProgressItemAssistant, Assistant: agg})
		bytesInEpoch += agg.Bytes
		delete(s.currentAssistantByStream, key)
	}
	for key, agg := range s.currentReasoningByKey {
		if agg == nil || !shouldSeal(agg.FirstPendingAt, agg.FlushRequested) {
			continue
		}
		items = append(items, sessionV3DurableProgressItem{Order: agg.FirstOrder, Kind: sessionV3DurableProgressItemReasoningDelta, Reasoning: agg, Step: agg.Step, ReasoningKey: agg.ReasoningKey})
		bytesInEpoch += agg.Bytes
		delete(s.currentReasoningByKey, key)
	}
	if len(items) == 0 {
		return
	}
	if err := s.reserveSealedEpochsLocked(1); err != nil {
		for _, item := range items {
			switch item.Kind {
			case sessionV3DurableProgressItemAssistant:
				if item.Assistant != nil {
					s.currentAssistantByStream[item.Assistant.StreamID] = item.Assistant
				}
			case sessionV3DurableProgressItemReasoningDelta:
				if item.Reasoning != nil {
					s.currentReasoningByKey[item.Reasoning.ReasoningKey] = item.Reasoning
				}
			}
		}
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	s.nextEpochID++
	s.sealedEpochs = append(s.sealedEpochs, sessionV3DurableProgressEpoch{EpochID: s.nextEpochID, Items: items, Bytes: bytesInEpoch})
}

func (s *sessionV3DurableProgressSink) worker() {
	defer close(s.workerDone)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var epoch sessionV3DurableProgressEpoch
		haveEpoch := false
		s.mu.Lock()
		if s.firstErr != nil {
			s.currentAssistantByStream = make(map[string]*sessionV3AssistantProgressAggregate)
			s.currentReasoningByKey = make(map[string]*sessionV3ReasoningProgressAggregate)
			s.sealedEpochs = nil
			s.pendingBytes = 0
			s.inFlightBytes = 0
			s.completeWaitersLocked(s.firstErr)
			s.mu.Unlock()
			return
		}
		s.sealDueCurrentLocked(time.Now())
		if len(s.sealedEpochs) > 0 {
			epoch = s.sealedEpochs[0]
			copy(s.sealedEpochs, s.sealedEpochs[1:])
			s.sealedEpochs = s.sealedEpochs[:len(s.sealedEpochs)-1]
			s.pendingBytes -= epoch.Bytes
			s.inFlightBytes += epoch.Bytes
			haveEpoch = true
		} else if s.closed && s.pendingBytes == 0 && s.inFlightBytes == 0 && len(s.currentAssistantByStream) == 0 && len(s.currentReasoningByKey) == 0 {
			s.completeWaitersLocked(nil)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		if !haveEpoch {
			select {
			case <-s.notify:
			case <-ticker.C:
			}
			continue
		}

		err := s.persistEpoch(epoch)
		s.mu.Lock()
		if err != nil {
			s.inFlightBytes -= epoch.Bytes
			s.controlItems -= epoch.ControlItems
			s.failLocked(err)
			s.mu.Unlock()
			continue
		}
		s.inFlightBytes -= epoch.Bytes
		s.controlItems -= epoch.ControlItems
		if s.controlItems < 0 {
			s.controlItems = 0
		}
		if epoch.EpochID > s.committedEpochID {
			s.committedEpochID = epoch.EpochID
		}
		s.completeWaitersLocked(nil)
		s.mu.Unlock()
	}
}

func (s *sessionV3DurableProgressSink) persistEpoch(epoch sessionV3DurableProgressEpoch) error {
	if s.writer == nil {
		return errors.New("v3 durable progress writer is not configured")
	}
	for _, item := range epoch.Items {
		switch item.Kind {
		case sessionV3DurableProgressItemPhase:
			if _, err := s.writer.RecordRunPhase(s.job, item.Phase, item.EventType); err != nil {
				return err
			}
		case sessionV3DurableProgressItemAssistant:
			if item.Assistant == nil {
				continue
			}
			progress := sessionV3AssistantProgress{
				StreamID:     item.Assistant.StreamID,
				Step:         item.Assistant.Step,
				StepID:       item.Assistant.StepID,
				LiveSeqStart: item.Assistant.LiveSeqStart,
				LiveSeqEnd:   item.Assistant.LiveSeqEnd,
				OffsetStart:  item.Assistant.OffsetStart,
				OffsetEnd:    item.Assistant.OffsetEnd,
				Text:         item.Assistant.Text.String(),
				RecordedAt:   item.Assistant.RecordedAt,
			}
			s.mu.Lock()
			s.nextAssistantDeltaIndex++
			deltaIndex := s.nextAssistantDeltaIndex
			s.mu.Unlock()
			if _, err := s.writer.RecordRunProgress(s.job, progress, deltaIndex); err != nil {
				return err
			}
			s.mu.Lock()
			s.assistantPersistedCount++
			s.mu.Unlock()
		case sessionV3DurableProgressItemReasoningDelta:
			if item.Reasoning == nil {
				continue
			}
			s.mu.Lock()
			s.reasoningDeltaIndexByKey[item.Reasoning.ReasoningKey]++
			eventIndex := s.reasoningDeltaIndexByKey[item.Reasoning.ReasoningKey]
			s.mu.Unlock()
			if _, err := s.writer.RecordReasoningEvent(s.job, "session.reasoning.delta", item.Reasoning.Step, eventIndex, item.Reasoning.ReasoningKey, item.Reasoning.Snapshot, ""); err != nil {
				return err
			}
			s.mu.Lock()
			s.reasoningPersistedCount++
			s.mu.Unlock()
		case sessionV3DurableProgressItemReasoningStarted:
			if _, err := s.writer.RecordReasoningEvent(s.job, "session.reasoning.started", item.Step, 0, item.ReasoningKey, "", ""); err != nil {
				return err
			}
		case sessionV3DurableProgressItemReasoningComplete:
			if _, err := s.writer.RecordReasoningEvent(s.job, "session.reasoning.completed", item.Step, 0, item.ReasoningKey, "", item.Summary); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *sessionV3DurableProgressSink) failLocked(err error) {
	if err == nil {
		return
	}
	if s.firstErr == nil {
		s.firstErr = err
		s.cancelOnce.Do(s.cancelProvider)
	}
	s.completeWaitersLocked(s.firstErr)
	s.signalLocked()
}

func (s *sessionV3DurableProgressSink) completeWaitersLocked(err error) {
	if err == nil {
		err = s.firstErr
	}
	for target, waiters := range s.waiters {
		if err != nil || target <= s.committedEpochID {
			for _, waiter := range waiters {
				select {
				case waiter <- err:
				default:
				}
			}
			delete(s.waiters, target)
		}
	}
}

func (s *sessionV3DurableProgressSink) signalLocked() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *sessionV3DurableProgressSink) assistantFlushMaxBytesLocked() int {
	if s.exec != nil && s.exec.deltaFlushMaxBytes > 0 {
		return s.exec.deltaFlushMaxBytes
	}
	return sessionV3AssistantDeltaFlushMaxBytes
}

func (s *sessionV3DurableProgressSink) reasoningFlushMaxBytesLocked() int {
	if s.exec != nil && s.exec.reasoningDeltaFlushMaxBytes > 0 {
		return s.exec.reasoningDeltaFlushMaxBytes
	}
	return sessionV3ReasoningDeltaFlushMaxBytes
}

func (s *sessionV3DurableProgressSink) minFlushDelayLocked() time.Duration {
	delay := sessionV3AssistantDeltaFlushMaxDelay
	if s.exec != nil && s.exec.deltaFlushMaxDelay > 0 {
		delay = s.exec.deltaFlushMaxDelay
	}
	if s.exec != nil && s.exec.reasoningDeltaFlushMaxDelay > 0 && s.exec.reasoningDeltaFlushMaxDelay < delay {
		delay = s.exec.reasoningDeltaFlushMaxDelay
	}
	if delay <= 0 {
		return 10 * time.Millisecond
	}
	return delay
}

type sessionV3DurableProgressSinkSnapshot struct {
	CurrentAssistantKeys int
	SealedAssistant      int
	InFlightAssistant    int
	PendingBytes         int
	InFlightBytes        int
	ControlItems         int
}

func (s *sessionV3DurableProgressSink) snapshotForTest() sessionV3DurableProgressSinkSnapshot {
	if s == nil {
		return sessionV3DurableProgressSinkSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := sessionV3DurableProgressSinkSnapshot{CurrentAssistantKeys: len(s.currentAssistantByStream), PendingBytes: s.pendingBytes, InFlightBytes: s.inFlightBytes, ControlItems: s.controlItems}
	for _, epoch := range s.sealedEpochs {
		for _, item := range epoch.Items {
			if item.Kind == sessionV3DurableProgressItemAssistant {
				snap.SealedAssistant++
			}
		}
	}
	return snap
}
