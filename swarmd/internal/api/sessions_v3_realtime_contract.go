package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	V3RealtimeStreamPath                   = "/v3/realtime/stream"
	V3RealtimeProtocol                     = "v3.realtime"
	V3RealtimeProtocolVersion              = 1
	V3RealtimeKindHello                    = "hello"
	V3RealtimeKindEvent                    = "event"
	V3RealtimeKindReplayStart              = "replay.started"
	V3RealtimeKindReplayDone               = "replay.complete"
	V3RealtimeKindCursorError              = "cursor.error"
	V3RealtimeKindKeepalive                = "keepalive"
	V3RealtimeKindEndpointWatermark        = "endpoint.watermark"
	V3RealtimeKindHighWater                = "projection.high_watermark"
	V3RealtimeKindSubscribe                = "subscribe.session"
	V3RealtimeKindUnsubscribe              = "unsubscribe.session"
	V3RealtimeKindResume                   = "resume"
	V3RealtimeKindWorksetSessionDiscovered = "workset.session.discovered"
	V3RealtimeKindWorksetSessionUpdated    = "workset.session.updated"
	V3RealtimeKindWorksetSessionRemoved    = "workset.session.removed"
	V3RealtimeKindAuthDenied               = "auth.denied"
	V3RealtimeKindSlowConsumer             = "slow_consumer.reconnect_required"
)

type V3RealtimeSubscriptionRequest struct {
	SessionID      string `json:"session_id"`
	SubscriptionID string `json:"subscription_id"`
	EndpointCursor string `json:"endpoint_cursor,omitempty"`
}

type V3RealtimeWorksetSubscriptionRequest struct {
	WorksetID             string                    `json:"workset_id"`
	SubscriptionID        string                    `json:"subscription_id"`
	Surface               string                    `json:"surface,omitempty"`
	Selector              V3RealtimeWorksetSelector `json:"selector"`
	Resources             []string                  `json:"resources,omitempty"`
	AutoSubscribeSessions bool                      `json:"auto_subscribe_sessions"`
}

type V3RealtimeWorksetSelector struct {
	Kind           string                     `json:"kind,omitempty"`
	Global         bool                       `json:"global,omitempty"`
	WorkspacePath  string                     `json:"workspace_path,omitempty"`
	WorkspacePaths []string                   `json:"workspace_paths,omitempty"`
	SessionIDs     []string                   `json:"session_ids,omitempty"`
	Recent         sessionsV3WorksetRecent    `json:"recent,omitempty"`
	Attention      sessionsV3WorksetAttention `json:"attention,omitempty"`
}

type V3RealtimeMessage struct {
	Protocol                   string                                 `json:"protocol"`
	ProtocolVersion            int                                    `json:"protocol_version"`
	Kind                       string                                 `json:"kind"`
	SessionID                  string                                 `json:"session_id,omitempty"`
	SubscriptionID             string                                 `json:"subscription_id,omitempty"`
	WorksetID                  string                                 `json:"workset_id,omitempty"`
	WorksetSubscriptionID      string                                 `json:"workset_subscription_id,omitempty"`
	AutoSubscribed             bool                                   `json:"auto_subscribed,omitempty"`
	AfterSeq                   uint64                                 `json:"after_seq,omitempty"`
	AfterRev                   uint64                                 `json:"afterRev,omitempty"`
	LastSeq                    uint64                                 `json:"last_seq,omitempty"`
	NextSeq                    uint64                                 `json:"next_seq,omitempty"`
	HighWatermarkSeq           uint64                                 `json:"high_watermark_seq,omitempty"`
	EndpointCursor             string                                 `json:"endpoint_cursor,omitempty"`
	Subscriptions              []V3RealtimeSubscriptionRequest        `json:"subscriptions,omitempty"`
	Worksets                   []V3RealtimeWorksetSubscriptionRequest `json:"worksets,omitempty"`
	Rev                        uint64                                 `json:"rev,omitempty"`
	PrevRev                    uint64                                 `json:"prevRev"`
	EventType                  string                                 `json:"event_type,omitempty"`
	Event                      *sessionruntime.SessionEvent           `json:"event,omitempty"`
	Projection                 *sessionruntime.SessionProjection      `json:"projection,omitempty"`
	Session                    *pebblestore.SessionSnapshot           `json:"session,omitempty"`
	CurrentRunState            *pebblestore.V3SessionRunState         `json:"current_run_state,omitempty"`
	ErrorCode                  string                                 `json:"error_code,omitempty"`
	Error                      string                                 `json:"error,omitempty"`
	Reason                     string                                 `json:"reason,omitempty"`
	BootstrapRequired          bool                                   `json:"bootstrap_required,omitempty"`
	OldestAvailableEndpointSeq uint64                                 `json:"oldest_available_endpoint_seq,omitempty"`
	LatestEndpointSeq          uint64                                 `json:"latest_endpoint_seq,omitempty"`
	MissingEndpointSeq         uint64                                 `json:"missing_endpoint_seq,omitempty"`
}

func NewV3RealtimeMessage(kind string) V3RealtimeMessage {
	return V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: kind}
}

func ValidateV3RealtimeMessage(message V3RealtimeMessage) error {
	return ValidateV3RealtimeSchemaMessage(message)
}

func ValidateV3RealtimeSchemaMessage(message V3RealtimeMessage) error {
	if message.Protocol != V3RealtimeProtocol {
		return fmt.Errorf("v3 realtime protocol must be %q", V3RealtimeProtocol)
	}
	if message.ProtocolVersion != V3RealtimeProtocolVersion {
		return fmt.Errorf("v3 realtime protocol_version must be %d", V3RealtimeProtocolVersion)
	}
	kind := strings.TrimSpace(message.Kind)
	if kind == "" {
		return errors.New("v3 realtime kind is required")
	}
	if !v3RealtimeKindAllowed(kind) {
		return fmt.Errorf("unsupported v3 realtime kind %q", kind)
	}
	if message.AfterSeq != 0 {
		return errors.New("v3 realtime uses endpoint_cursor for transport resume; after_seq is not accepted")
	}
	if message.AfterRev != 0 {
		return errors.New("v3 realtime uses endpoint_cursor for transport resume; afterRev is not accepted")
	}
	switch kind {
	case V3RealtimeKindHello:
		if strings.TrimSpace(message.EndpointCursor) == "" {
			return errors.New("v3 realtime hello requires endpoint_cursor")
		}
		return nil
	case V3RealtimeKindEvent:
		return validateV3RealtimeEventMessage(message)
	case V3RealtimeKindWorksetSessionDiscovered, V3RealtimeKindWorksetSessionUpdated, V3RealtimeKindWorksetSessionRemoved:
		return validateV3RealtimeWorksetSessionMessage(message)
	case V3RealtimeKindReplayStart:
		return validateV3RealtimeSessionCursorMessage(message)
	case V3RealtimeKindReplayDone, V3RealtimeKindHighWater:
		return validateV3RealtimeSessionCursorMessage(message)
	case V3RealtimeKindKeepalive:
		return nil
	case V3RealtimeKindEndpointWatermark:
		if strings.TrimSpace(message.EndpointCursor) == "" {
			return errors.New("v3 realtime endpoint.watermark requires endpoint_cursor")
		}
		return nil
	case V3RealtimeKindCursorError, V3RealtimeKindAuthDenied, V3RealtimeKindSlowConsumer:
		if strings.TrimSpace(message.ErrorCode) == "" {
			return fmt.Errorf("v3 realtime %s requires error_code", kind)
		}
		return nil
	case V3RealtimeKindSubscribe:
		if strings.TrimSpace(message.SessionID) == "" {
			return errors.New("v3 realtime subscribe.session requires session_id")
		}
		if strings.TrimSpace(message.SubscriptionID) == "" {
			return errors.New("v3 realtime subscribe.session requires subscription_id")
		}
		return nil
	case V3RealtimeKindUnsubscribe:
		if strings.TrimSpace(message.SessionID) == "" && strings.TrimSpace(message.SubscriptionID) == "" {
			return errors.New("v3 realtime unsubscribe.session requires session_id or subscription_id")
		}
		return nil
	case V3RealtimeKindResume:
		if strings.TrimSpace(message.EndpointCursor) == "" {
			return errors.New("v3 realtime resume requires endpoint_cursor")
		}
		if err := validateV3RealtimeSubscriptionRequests(message.Subscriptions); err != nil {
			return err
		}
		return validateV3RealtimeWorksetSubscriptionRequests(message.Worksets)
	default:
		return fmt.Errorf("unsupported v3 realtime kind %q", kind)
	}
}

func ValidateV3RealtimeInboundClientMessage(message V3RealtimeMessage) error {
	if err := ValidateV3RealtimeSchemaMessage(message); err != nil {
		return err
	}
	kind := strings.TrimSpace(message.Kind)
	switch kind {
	case V3RealtimeKindSubscribe, V3RealtimeKindUnsubscribe, V3RealtimeKindResume:
		return nil
	default:
		return fmt.Errorf("v3 realtime client message kind %q is not allowed", kind)
	}
}

func ValidateV3RealtimeOutboundServerMessage(message V3RealtimeMessage) error {
	if err := ValidateV3RealtimeSchemaMessage(message); err != nil {
		return err
	}
	kind := strings.TrimSpace(message.Kind)
	switch kind {
	case V3RealtimeKindHello,
		V3RealtimeKindEvent,
		V3RealtimeKindReplayStart,
		V3RealtimeKindReplayDone,
		V3RealtimeKindCursorError,
		V3RealtimeKindKeepalive,
		V3RealtimeKindEndpointWatermark,
		V3RealtimeKindHighWater,
		V3RealtimeKindWorksetSessionDiscovered,
		V3RealtimeKindWorksetSessionUpdated,
		V3RealtimeKindWorksetSessionRemoved,
		V3RealtimeKindAuthDenied,
		V3RealtimeKindSlowConsumer:
		return nil
	default:
		return fmt.Errorf("v3 realtime server message kind %q is not allowed", kind)
	}
}

func v3RealtimeKindAllowed(kind string) bool {
	switch kind {
	case V3RealtimeKindHello, V3RealtimeKindEvent, V3RealtimeKindReplayStart, V3RealtimeKindReplayDone, V3RealtimeKindCursorError, V3RealtimeKindKeepalive, V3RealtimeKindEndpointWatermark, V3RealtimeKindHighWater, V3RealtimeKindSubscribe, V3RealtimeKindUnsubscribe, V3RealtimeKindResume, V3RealtimeKindWorksetSessionDiscovered, V3RealtimeKindWorksetSessionUpdated, V3RealtimeKindWorksetSessionRemoved, V3RealtimeKindAuthDenied, V3RealtimeKindSlowConsumer:
		return true
	default:
		return false
	}
}

func validateV3RealtimeSessionCursorMessage(message V3RealtimeMessage) error {
	if strings.TrimSpace(message.SessionID) == "" {
		return fmt.Errorf("v3 realtime %s requires session_id", message.Kind)
	}
	if strings.TrimSpace(message.EndpointCursor) == "" {
		return fmt.Errorf("v3 realtime %s requires endpoint_cursor", message.Kind)
	}
	return nil
}

func validateV3RealtimeSubscriptionRequests(subscriptions []V3RealtimeSubscriptionRequest) error {
	for _, sub := range subscriptions {
		if strings.TrimSpace(sub.SessionID) == "" {
			return errors.New("v3 realtime resume subscription requires session_id")
		}
		if strings.TrimSpace(sub.SubscriptionID) == "" {
			return errors.New("v3 realtime resume subscription requires subscription_id")
		}
	}
	return nil
}

func validateV3RealtimeWorksetSubscriptionRequests(worksets []V3RealtimeWorksetSubscriptionRequest) error {
	for _, workset := range worksets {
		if strings.TrimSpace(workset.WorksetID) == "" {
			return errors.New("v3 realtime resume workset requires workset_id")
		}
		if strings.TrimSpace(workset.SubscriptionID) == "" {
			return errors.New("v3 realtime resume workset requires subscription_id")
		}
		selector := workset.Selector
		kind := strings.TrimSpace(selector.Kind)
		if kind == "" {
			return errors.New("v3 realtime resume workset requires selector.kind")
		}
		switch kind {
		case "global", "session_ids", "workspace", "recent":
		default:
			return fmt.Errorf("unsupported v3 realtime selector.kind %q", kind)
		}
		if kind != "global" && !selector.Global && len(selector.SessionIDs) == 0 && strings.TrimSpace(selector.WorkspacePath) == "" && len(selector.WorkspacePaths) == 0 && selector.Recent.Limit <= 0 {
			return errors.New("v3 realtime resume workset requires a concrete selector")
		}
	}
	return nil
}

func validateV3RealtimeEventMessage(message V3RealtimeMessage) error {
	if strings.TrimSpace(message.SessionID) == "" {
		return errors.New("v3 realtime event requires session_id")
	}
	if strings.TrimSpace(message.EndpointCursor) == "" {
		return errors.New("v3 realtime event requires endpoint_cursor")
	}
	if message.Rev == 0 {
		return errors.New("v3 realtime event requires rev")
	}
	if message.Rev != message.PrevRev+1 {
		return errors.New("v3 realtime event requires continuous rev/prevRev")
	}
	if message.Event == nil {
		return errors.New("v3 realtime event requires event")
	}
	if strings.TrimSpace(message.Event.SessionID) == "" {
		return errors.New("v3 realtime event payload requires session_id")
	}
	if message.Event.SessionID != message.SessionID {
		return errors.New("v3 realtime event session_id conflicts with payload session_id")
	}
	if message.Event.Seq == 0 {
		return errors.New("v3 realtime event requires event.seq")
	}
	if message.LastSeq != 0 && message.LastSeq != message.Event.Seq {
		return errors.New("v3 realtime event last_seq must equal event.seq")
	}
	if strings.TrimSpace(message.Event.EventType) == "" {
		return errors.New("v3 realtime event requires event_type")
	}
	if strings.TrimSpace(message.EventType) == "" {
		return errors.New("v3 realtime event requires top-level event_type")
	}
	if message.EventType != message.Event.EventType {
		return errors.New("v3 realtime event_type conflicts with payload event_type")
	}
	if len(message.Event.Payload) == 0 || string(message.Event.Payload) == "null" {
		return errors.New("v3 realtime event requires durable event payload")
	}
	if strings.HasPrefix(message.Event.EventType, "session.tool.") {
		if err := validateV3RealtimeToolIdentity(message.Event.EventType, message.Event.Payload); err != nil {
			return err
		}
	}
	return nil
}

func validateV3RealtimeWorksetSessionMessage(message V3RealtimeMessage) error {
	if strings.TrimSpace(message.WorksetID) == "" {
		return fmt.Errorf("v3 realtime %s requires workset_id", message.Kind)
	}
	if strings.TrimSpace(message.WorksetSubscriptionID) == "" {
		return fmt.Errorf("v3 realtime %s requires workset_subscription_id", message.Kind)
	}
	if strings.TrimSpace(message.SessionID) == "" {
		return fmt.Errorf("v3 realtime %s requires session_id", message.Kind)
	}
	if strings.TrimSpace(message.SubscriptionID) == "" && message.Kind == V3RealtimeKindWorksetSessionDiscovered && message.AutoSubscribed {
		return errors.New("v3 realtime workset.session.discovered auto_subscribed requires subscription_id")
	}
	if strings.TrimSpace(message.EndpointCursor) == "" {
		return fmt.Errorf("v3 realtime %s requires endpoint_cursor", message.Kind)
	}
	if message.Rev == 0 {
		return fmt.Errorf("v3 realtime %s requires rev", message.Kind)
	}
	if message.Rev != message.PrevRev+1 {
		return fmt.Errorf("v3 realtime %s requires continuous rev/prevRev", message.Kind)
	}
	if strings.TrimSpace(message.EventType) == "" {
		return fmt.Errorf("v3 realtime %s requires event_type", message.Kind)
	}
	return nil
}

func validateV3RealtimeToolIdentity(eventType string, raw json.RawMessage) error {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode v3 realtime tool payload: %w", err)
	}
	for _, key := range []string{"run_id", "step_id", "call_id", "tool_instance_id", "tool_name", "recorded_at"} {
		value, ok := payload[key]
		if !ok || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return fmt.Errorf("v3 realtime tool event requires %s", key)
		}
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "session.tool.completed" || eventType == "session.tool.failed" || eventType == "session.tool.cancelled" || eventType == "session.tool.canceled" {
		if strings.TrimSpace(fmt.Sprint(payload["status"])) == "" {
			return errors.New("v3 realtime terminal tool event requires status")
		}
	}
	return nil
}
