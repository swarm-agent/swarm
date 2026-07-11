package pebblestore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

type EventEnvelope struct {
	GlobalSeq     uint64          `json:"global_seq"`
	Stream        string          `json:"stream"`
	EventType     string          `json:"event_type"`
	EntityID      string          `json:"entity_id"`
	Payload       json.RawMessage `json:"payload"`
	TsUnixMs      int64           `json:"ts_unix_ms"`
	Source        string          `json:"source,omitempty"`
	SourceSeq     uint64          `json:"source_seq,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
}

type EventLog struct {
	store *Store
	mu    sync.Mutex
	seq   uint64
}

type EventAppend struct {
	Stream        string
	EventType     string
	EntityID      string
	Payload       []byte
	Source        string
	SourceSeq     uint64
	CausationID   string
	CorrelationID string
}

func NewEventLog(store *Store) (*EventLog, error) {
	seq := uint64(0)
	raw, ok, err := store.GetBytes(keyGlobalSequenceCounter)
	if err != nil {
		return nil, fmt.Errorf("load global sequence: %w", err)
	}
	if ok {
		loaded, err := bytesToUint64(raw)
		if err != nil {
			return nil, fmt.Errorf("decode global sequence: %w", err)
		}
		seq = loaded
	}
	return &EventLog{store: store, seq: seq}, nil
}

func (l *EventLog) CurrentSequence() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

func (l *EventLog) Append(stream, eventType, entityID string, payload []byte, causationID, correlationID string) (EventEnvelope, error) {
	return l.AppendWithSource(stream, eventType, entityID, payload, "", causationID, correlationID)
}

func (l *EventLog) AppendWithSource(stream, eventType, entityID string, payload []byte, source, causationID, correlationID string) (EventEnvelope, error) {
	return l.AppendWithSourceSeq(stream, eventType, entityID, payload, source, 0, causationID, correlationID)
}

func (l *EventLog) AppendWithSourceSeq(stream, eventType, entityID string, payload []byte, source string, sourceSeq uint64, causationID, correlationID string) (EventEnvelope, error) {
	envelopes, err := l.AppendBatch([]EventAppend{{Stream: stream, EventType: eventType, EntityID: entityID, Payload: payload, Source: source, SourceSeq: sourceSeq, CausationID: causationID, CorrelationID: correlationID}})
	if err != nil {
		return EventEnvelope{}, err
	}
	return envelopes[0], nil
}

func (l *EventLog) AppendBatch(appends []EventAppend) ([]EventEnvelope, error) {
	if len(appends) == 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	startSeq := l.seq
	envelopes := make([]EventEnvelope, 0, len(appends))
	serialized := make([][]byte, 0, len(appends))
	now := time.Now().UnixMilli()
	for _, appendInput := range appends {
		l.seq++
		envelope := EventEnvelope{
			GlobalSeq:     l.seq,
			Stream:        appendInput.Stream,
			EventType:     appendInput.EventType,
			EntityID:      appendInput.EntityID,
			Payload:       append([]byte(nil), appendInput.Payload...),
			TsUnixMs:      now,
			Source:        appendInput.Source,
			SourceSeq:     appendInput.SourceSeq,
			CausationID:   appendInput.CausationID,
			CorrelationID: appendInput.CorrelationID,
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			l.seq = startSeq
			return nil, fmt.Errorf("marshal event envelope: %w", err)
		}
		envelopes = append(envelopes, envelope)
		serialized = append(serialized, payload)
	}

	batch := l.store.NewBatch()
	defer batch.Close()
	for i, payload := range serialized {
		if err := batch.Set([]byte(EventKey(envelopes[i].GlobalSeq)), payload, nil); err != nil {
			l.seq = startSeq
			return nil, fmt.Errorf("write event payload: %w", err)
		}
	}
	if err := batch.Set([]byte(keyGlobalSequenceCounter), uint64ToBytes(l.seq), nil); err != nil {
		l.seq = startSeq
		return nil, fmt.Errorf("write global sequence: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		l.seq = startSeq
		return nil, fmt.Errorf("commit event batch: %w", err)
	}
	return envelopes, nil
}

func (l *EventLog) ReadFrom(startSequence uint64, limit int) ([]EventEnvelope, error) {
	if limit <= 0 {
		limit = 100
	}

	l.mu.Lock()
	max := l.seq
	l.mu.Unlock()

	if startSequence == 0 {
		startSequence = 1
	}

	out := make([]EventEnvelope, 0, limit)
	for seq := startSequence; seq <= max && len(out) < limit; seq++ {
		raw, ok, err := l.store.GetBytes(EventKey(seq))
		if err != nil {
			return nil, fmt.Errorf("read event %d: %w", seq, err)
		}
		if !ok {
			continue
		}
		var envelope EventEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode event %d: %w", seq, err)
		}
		out = append(out, envelope)
	}
	return out, nil
}

func uint64ToBytes(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}

func bytesToUint64(raw []byte) (uint64, error) {
	if len(raw) != 8 {
		return 0, fmt.Errorf("invalid uint64 byte length %d", len(raw))
	}
	return binary.BigEndian.Uint64(raw), nil
}
