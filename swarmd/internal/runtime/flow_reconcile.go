package runtime

import (
	"strings"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func reconcileFlowRunsFromLifecycles(flows *pebblestore.FlowStore, sessions *sessionruntime.Service) error {
	if flows == nil || sessions == nil {
		return nil
	}
	sessionList, err := sessions.ListSessions(100000)
	if err != nil {
		return err
	}
	for _, session := range sessionList {
		if session.Lifecycle == nil || session.Lifecycle.Active {
			continue
		}
		flowID, ok := stringMetadata(session.Metadata, "flow_id")
		if !ok || flowID == "" {
			continue
		}
		revision := int64Metadata(session.Metadata, "flow_revision")
		scheduledAt, _ := timeMetadata(session.Metadata, "scheduled_at")
		runID := strings.TrimSpace(session.Lifecycle.RunID)
		if runID == "" {
			continue
		}
		status := flowRunStatusFromLifecycle(session.Lifecycle)
		startedAt := unixMillisToTime(session.Lifecycle.StartedAt)
		finishedAt := unixMillisToTime(session.Lifecycle.EndedAt)
		durationMS := int64(0)
		if !startedAt.IsZero() && !finishedAt.IsZero() {
			durationMS = finishedAt.Sub(startedAt).Milliseconds()
			if durationMS < 0 {
				durationMS = 0
			}
		}
		if scheduledAt.IsZero() {
			scheduledAt = startedAt
		}
		summary := strings.TrimSpace(session.Lifecycle.Error)
		if summary == "" {
			summary = strings.TrimSpace(session.Lifecycle.StopReason)
		}
		if _, err := flows.PutTargetRun(pebblestore.FlowRunSummaryRecord{
			RunID:       runID,
			FlowID:      flowID,
			Revision:    revision,
			ScheduledAt: scheduledAt,
			StartedAt:   startedAt,
			FinishedAt:  finishedAt,
			DurationMS:  durationMS,
			Status:      status,
			Summary:     summary,
			SessionID:   session.ID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func flowRunStatusFromLifecycle(lifecycle *pebblestore.SessionLifecycleSnapshot) string {
	if lifecycle == nil {
		return pebblestore.FlowRunStatusRunning
	}
	switch strings.ToLower(strings.TrimSpace(lifecycle.Phase)) {
	case "completed":
		return pebblestore.FlowRunStatusSuccess
	case "errored", "interrupted", "cancelled":
		return pebblestore.FlowRunStatusFailed
	case "blocked":
		return pebblestore.FlowRunStatusReview
	default:
		if lifecycle.Active {
			return pebblestore.FlowRunStatusRunning
		}
		return pebblestore.FlowRunStatusSkipped
	}
}

func stringMetadata(metadata map[string]any, key string) (string, bool) {
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func int64Metadata(metadata map[string]any, key string) int64 {
	switch value := metadata[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case jsonNumber:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

type jsonNumber interface {
	Int64() (int64, error)
}

func timeMetadata(metadata map[string]any, key string) (time.Time, bool) {
	text, ok := stringMetadata(metadata, key)
	if !ok || text == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func unixMillisToTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
