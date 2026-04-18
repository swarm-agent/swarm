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
	CausationID   string          `json:"causation_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
}

type EventLog struct {
	store *Store
	mu    sync.Mutex
	seq   uint64
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
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	envelope := EventEnvelope{
		GlobalSeq:     l.seq,
		Stream:        stream,
		EventType:     eventType,
		EntityID:      entityID,
		Payload:       append([]byte(nil), payload...),
		TsUnixMs:      time.Now().UnixMilli(),
		CausationID:   causationID,
		CorrelationID: correlationID,
	}
	serialized, err := json.Marshal(envelope)
	if err != nil {
		l.seq--
		return EventEnvelope{}, fmt.Errorf("marshal event envelope: %w", err)
	}

	batch := l.store.NewBatch()
	defer batch.Close()

	if err := batch.Set([]byte(EventKey(l.seq)), serialized, nil); err != nil {
		l.seq--
		return EventEnvelope{}, fmt.Errorf("write event payload: %w", err)
	}
	if err := batch.Set([]byte(keyGlobalSequenceCounter), uint64ToBytes(l.seq), nil); err != nil {
		l.seq--
		return EventEnvelope{}, fmt.Errorf("write global sequence: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		l.seq--
		return EventEnvelope{}, fmt.Errorf("commit event batch: %w", err)
	}

	return envelope, nil
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
