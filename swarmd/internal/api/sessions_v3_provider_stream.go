package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type sessionV3AssistantLiveTracker struct {
	SessionID string
	RunID     string
	Step      int
	StepID    string
	StreamID  string
	NextSeq   uint64
	Offset    uint64
}

func sessionV3AssistantLiveStreamID(runID string, step int) string {
	if step <= 0 {
		step = 1
	}
	return fmt.Sprintf("assistant:%s:step:%d", strings.TrimSpace(runID), step)
}

func newSessionV3AssistantLiveTracker(sessionID, runID string, step int) *sessionV3AssistantLiveTracker {
	if step <= 0 {
		step = 1
	}
	stepID := sessionV3ProviderToolStepID(step)
	return &sessionV3AssistantLiveTracker{
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
		Step:      step,
		StepID:    stepID,
		StreamID:  sessionV3AssistantLiveStreamID(runID, step),
		NextSeq:   1,
	}
}

func (t *sessionV3AssistantLiveTracker) Append(text string, recordedAt int64) (V3RealtimeLivePatch, sessionV3AssistantProgress) {
	if t == nil || text == "" {
		return V3RealtimeLivePatch{}, sessionV3AssistantProgress{}
	}
	startSeq := t.NextSeq
	endSeq := startSeq
	offsetStart := t.Offset
	offsetEnd := offsetStart + uint64(len([]byte(text)))
	patch := V3RealtimeLivePatch{
		SessionID:    t.SessionID,
		RunID:        t.RunID,
		StreamID:     t.StreamID,
		StreamKind:   "assistant_text",
		Operation:    "append",
		Step:         t.Step,
		StepID:       t.StepID,
		LiveSeqStart: startSeq,
		LiveSeqEnd:   endSeq,
		OffsetStart:  offsetStart,
		OffsetEnd:    offsetEnd,
		Text:         text,
		RecordedAt:   recordedAt,
	}
	durable := sessionV3AssistantProgress{
		StreamID:     t.StreamID,
		Step:         t.Step,
		StepID:       t.StepID,
		LiveSeqStart: startSeq,
		LiveSeqEnd:   endSeq,
		OffsetStart:  offsetStart,
		OffsetEnd:    offsetEnd,
		Text:         text,
		RecordedAt:   recordedAt,
	}
	t.NextSeq = endSeq + 1
	t.Offset = offsetEnd
	return patch, durable
}

type sessionV3ProviderStreamState struct {
	mu sync.Mutex

	exec     *sessionV3Executor
	job      sessionV3ExecutorJob
	sink     *sessionV3DurableProgressSink
	tracker  *sessionV3AssistantLiveTracker
	streamed strings.Builder

	providerFirstEventRecorded bool
	outputStreamingRecorded    bool
	streamEventCount           int
	progressErr                error

	activeReasoningKey     string
	reasoningByKey         map[string]string
	reasoningOrder         []string
	providerToolEventIndex int
}

func newSessionV3ProviderStreamState(exec *sessionV3Executor, job sessionV3ExecutorJob, sink *sessionV3DurableProgressSink, step int) *sessionV3ProviderStreamState {
	return &sessionV3ProviderStreamState{
		exec:           exec,
		job:            job,
		sink:           sink,
		tracker:        newSessionV3AssistantLiveTracker(job.SessionID, job.RunID, step),
		reasoningByKey: make(map[string]string, 4),
	}
}

func (s *sessionV3ProviderStreamState) Handle(event provideriface.StreamEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progressErr != nil {
		return
	}
	if event.Type == "" {
		return
	}

	s.streamEventCount++
	if !s.providerFirstEventRecorded {
		s.progressErr = s.sink.TryRecordPhase(RunPhaseProviderFirstEvent, "session.provider.first_event")
		s.providerFirstEventRecorded = s.progressErr == nil
	}
	if s.progressErr != nil {
		return
	}

	switch event.Type {
	case provideriface.StreamEventOutputTextDelta:
		if event.Delta == "" {
			return
		}
		if !s.outputStreamingRecorded {
			s.progressErr = s.sink.TryRecordPhase(RunPhaseOutputStreaming, "session.output.streaming")
			s.outputStreamingRecorded = s.progressErr == nil
		}
		if s.progressErr != nil {
			return
		}
		now := time.Now().UnixMilli()
		s.streamed.WriteString(event.Delta)
		patch, durable := s.tracker.Append(event.Delta, now)
		if s.exec != nil && s.exec.server != nil && s.exec.server.v3LiveHub != nil {
			s.exec.server.v3LiveHub.publish(s.job.Principal.AccountScopeID, patch)
		}
		s.progressErr = s.sink.TryAppendAssistant(durable)
	case provideriface.StreamEventReasoningSummaryDelta:
		s.handleReasoningLocked(event)
	case provideriface.StreamEventToolCallStarted,
		provideriface.StreamEventToolCallArgumentsDelta,
		provideriface.StreamEventToolCallArgumentsSnapshot,
		provideriface.StreamEventToolCallCompleted:
		s.providerToolEventIndex++
		s.progressErr = s.sink.TryRecordProviderToolConstruction(s.tracker.Step, s.providerToolEventIndex, event)
		if s.progressErr == nil && s.exec != nil && s.exec.server != nil && s.exec.server.v3LiveHub != nil {
			s.exec.server.v3LiveHub.publish(s.job.Principal.AccountScopeID, sessionV3ProviderToolConstructionLivePatch(s.job, s.tracker.Step, s.providerToolEventIndex, event))
		}
	}
}

// sessionV3ProviderToolConstructionLivePatch is a low-latency accelerator.
// Durable recovery remains authoritative through session.provider_tool_call.*
// events and their realtime outbox records.
func sessionV3ProviderToolConstructionLivePatch(job sessionV3ExecutorJob, step, eventIndex int, event provideriface.StreamEvent) V3RealtimeLivePatch {
	status := strings.TrimSpace(event.Status)
	if status == "" {
		switch event.Type {
		case provideriface.StreamEventToolCallStarted:
			status = "started"
		case provideriface.StreamEventToolCallCompleted:
			status = "completed"
		default:
			status = "building"
		}
	}
	recordedAt := event.RecordedAtUnixMs
	if recordedAt <= 0 {
		recordedAt = time.Now().UnixMilli()
	}
	startedAt := event.StartedAtUnixMs
	if startedAt <= 0 {
		startedAt = recordedAt
	}
	metadata := cloneSessionsV3Metadata(event.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["path_id"] = "run.v3.provider-tool-construction.v1"
	metadata["type"] = sessionV3ProviderToolConstructionEventType(event.Type)
	metadata["run_id"] = strings.TrimSpace(job.RunID)
	metadata["step"] = step
	metadata["event_index"] = eventIndex
	metadata["call_id"] = strings.TrimSpace(event.ToolCallID)
	metadata["tool_name"] = strings.TrimSpace(event.ToolName)
	metadata["provider"] = strings.TrimSpace(event.ProviderID)
	metadata["model"] = strings.TrimSpace(event.Model)
	metadata["status"] = status
	metadata["recorded_at"] = recordedAt
	metadata["started_at"] = startedAt
	if event.ToolCallIndex != nil {
		metadata["output_index"] = *event.ToolCallIndex
	}
	if event.Arguments != "" {
		metadata["arguments"] = event.Arguments
	}
	if event.ArgumentsDelta != "" {
		metadata["arguments_delta"] = event.ArgumentsDelta
	}
	if event.ArgumentsSnapshot != "" {
		metadata["arguments_snapshot"] = event.ArgumentsSnapshot
	}
	raw, _ := json.Marshal(metadata)
	return V3RealtimeLivePatch{
		SessionID:    strings.TrimSpace(job.SessionID),
		RunID:        strings.TrimSpace(job.RunID),
		StreamID:     fmt.Sprintf("provider-tool:%s:step:%d:event:%d", strings.TrimSpace(job.RunID), step, eventIndex),
		StreamKind:   "provider_tool_call",
		Operation:    "append",
		Step:         step,
		StepID:       sessionV3ProviderToolStepID(step),
		LiveSeqStart: 1,
		LiveSeqEnd:   1,
		OffsetStart:  0,
		OffsetEnd:    uint64(len(raw)),
		Text:         string(raw),
		RecordedAt:   recordedAt,
	}
}

func sessionV3ProviderToolConstructionEventType(eventType provideriface.StreamEventType) string {
	switch eventType {
	case provideriface.StreamEventToolCallStarted:
		return "session.provider_tool_call.started"
	case provideriface.StreamEventToolCallArgumentsDelta:
		return "session.provider_tool_call.arguments.delta"
	case provideriface.StreamEventToolCallArgumentsSnapshot:
		return "session.provider_tool_call.arguments.snapshot"
	case provideriface.StreamEventToolCallCompleted:
		return "session.provider_tool_call.completed"
	default:
		return ""
	}
}

func cloneSessionV3ProviderToolStreamEvent(event provideriface.StreamEvent) provideriface.StreamEvent {
	cloned := event
	if event.ToolCallIndex != nil {
		index := *event.ToolCallIndex
		cloned.ToolCallIndex = &index
	}
	cloned.Metadata = cloneSessionsV3Metadata(event.Metadata)
	return cloned
}

func (s *sessionV3ProviderStreamState) handleReasoningLocked(event provideriface.StreamEvent) {
	reasoningKey := sessionV3NormalizeReasoningKey(event.ReasoningKey)
	if _, ok := s.reasoningByKey[reasoningKey]; !ok {
		s.reasoningOrder = append(s.reasoningOrder, reasoningKey)
	}
	if s.activeReasoningKey != reasoningKey {
		if s.activeReasoningKey != "" {
			summary := strings.TrimSpace(s.reasoningByKey[s.activeReasoningKey])
			s.progressErr = s.sink.TryCompleteReasoning(s.tracker.Step, s.activeReasoningKey, summary)
			if s.progressErr != nil {
				return
			}
		}
		s.activeReasoningKey = reasoningKey
		s.progressErr = s.sink.TryStartReasoning(s.tracker.Step, reasoningKey)
		if s.progressErr != nil {
			return
		}
	}
	previous := s.reasoningByKey[reasoningKey]
	next := sessionV3ApplyReasoningUpdate(previous, event.Delta, event.DeltaMode)
	if next == "" || next == previous {
		return
	}
	s.reasoningByKey[reasoningKey] = next
	s.progressErr = s.sink.TryReplaceReasoning(s.tracker.Step, reasoningKey, next)
}

func sessionV3ApplyReasoningUpdate(previous, incoming string, mode provideriface.StreamEventDeltaMode) string {
	if incoming == "" {
		return previous
	}
	switch mode {
	case provideriface.StreamEventDeltaModeAppend:
		return previous + incoming
	case provideriface.StreamEventDeltaModeReplace:
		return strings.TrimSpace(incoming)
	default:
		// Legacy adapters emitted untyped snapshots. Keep their historical
		// containment behavior rather than guessing append/replace from timing.
		return sessionV3MergeReasoningSnapshotOrChunk(previous, incoming)
	}
}

func (s *sessionV3ProviderStreamState) FinishStep() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progressErr != nil {
		return s.progressErr
	}
	if s.activeReasoningKey != "" {
		summary := strings.TrimSpace(s.reasoningByKey[s.activeReasoningKey])
		s.progressErr = s.sink.TryCompleteReasoning(s.tracker.Step, s.activeReasoningKey, summary)
		if s.progressErr != nil {
			return s.progressErr
		}
		s.activeReasoningKey = ""
	}
	return s.progressErr
}

func (s *sessionV3ProviderStreamState) EnsureResponseText(text string) error {
	if s == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progressErr != nil {
		return s.progressErr
	}
	if !s.providerFirstEventRecorded {
		s.progressErr = s.sink.TryRecordPhase(RunPhaseProviderFirstEvent, "session.provider.first_event")
		s.providerFirstEventRecorded = s.progressErr == nil
	}
	if s.progressErr != nil {
		return s.progressErr
	}
	if !s.outputStreamingRecorded {
		s.progressErr = s.sink.TryRecordPhase(RunPhaseOutputStreaming, "session.output.streaming")
		s.outputStreamingRecorded = s.progressErr == nil
	}
	if s.progressErr != nil {
		return s.progressErr
	}
	now := time.Now().UnixMilli()
	s.streamed.WriteString(text)
	patch, durable := s.tracker.Append(text, now)
	if s.exec != nil && s.exec.server != nil && s.exec.server.v3LiveHub != nil {
		s.exec.server.v3LiveHub.publish(s.job.Principal.AccountScopeID, patch)
	}
	s.progressErr = s.sink.TryAppendAssistant(durable)
	return s.progressErr
}

func (s *sessionV3ProviderStreamState) StreamedText() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamed.String()
}

func (s *sessionV3ProviderStreamState) OffsetEnd() uint64 {
	if s == nil || s.tracker == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tracker.Offset
}

func (s *sessionV3ProviderStreamState) StreamID() string {
	if s == nil || s.tracker == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tracker.StreamID
}

func (s *sessionV3ProviderStreamState) Step() int {
	if s == nil || s.tracker == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tracker.Step
}

func sessionV3ProviderStepAssistantText(response provideriface.Response, streamed string) string {
	if streamed != "" {
		return streamed
	}
	if strings.TrimSpace(response.Text) != "" {
		return response.Text
	}
	parts := make([]string, 0, len(response.AssistantMessages))
	for _, message := range response.AssistantMessages {
		if message.Phase != "" && message.Phase != provideriface.AssistantPhaseFinalAnswer {
			continue
		}
		if strings.TrimSpace(message.Text) != "" {
			parts = append(parts, message.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func firstNonNilErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateSessionV3AssistantStreamCompletion(content string, offset uint64) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if offset != uint64(len([]byte(content))) {
		return errors.New("v3 assistant stream offset does not match content byte length")
	}
	return nil
}
