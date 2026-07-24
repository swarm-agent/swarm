package api

import (
	"fmt"
	"sync"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

const v3RealtimeSubscriberBufSize = 256

type v3RealtimeSlowConsumer struct {
	EndpointSeq uint64
	Reason      string
}

type v3RealtimeOutboxSubscriber struct {
	id   string
	send chan sessionruntime.RealtimeOutboxRecord
	slow chan v3RealtimeSlowConsumer
}

type v3RealtimeOutboxHub struct {
	mu      sync.Mutex
	nextSub uint64
	subs    map[string]*v3RealtimeOutboxSubscriber
}

func newV3RealtimeOutboxHub() *v3RealtimeOutboxHub {
	return &v3RealtimeOutboxHub{subs: make(map[string]*v3RealtimeOutboxSubscriber)}
}

func (h *v3RealtimeOutboxHub) diagnosticsSnapshot() map[string]any {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pending := 0
	for _, sub := range h.subs {
		if sub != nil {
			pending += len(sub.send)
		}
	}
	return map[string]any{"subscribers": len(h.subs), "pending_records": pending}
}

func (h *v3RealtimeOutboxHub) subscribe() *v3RealtimeOutboxSubscriber {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSub++
	sub := &v3RealtimeOutboxSubscriber{
		id:   fmt.Sprintf("v3realtime_%d", h.nextSub),
		send: make(chan sessionruntime.RealtimeOutboxRecord, v3RealtimeSubscriberBufSize),
		slow: make(chan v3RealtimeSlowConsumer, 1),
	}
	h.subs[sub.id] = sub
	return sub
}

func (h *v3RealtimeOutboxHub) unsubscribe(sub *v3RealtimeOutboxSubscriber) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub.id)
}

func (h *v3RealtimeOutboxHub) publish(record sessionruntime.RealtimeOutboxRecord) {
	if h == nil || record.EndpointSeq == 0 {
		return
	}
	h.mu.Lock()
	subs := make([]*v3RealtimeOutboxSubscriber, 0, len(h.subs))
	for _, sub := range h.subs {
		if sub != nil {
			subs = append(subs, sub)
		}
	}
	h.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.send <- record:
		default:
			h.markSlowConsumer(sub, record.EndpointSeq)
		}
	}
}

func (h *v3RealtimeOutboxHub) markSlowConsumer(sub *v3RealtimeOutboxSubscriber, endpointSeq uint64) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	if h.subs[sub.id] == nil {
		h.mu.Unlock()
		return
	}
	delete(h.subs, sub.id)
	h.mu.Unlock()

	notice := v3RealtimeSlowConsumer{
		EndpointSeq: endpointSeq,
		Reason:      fmt.Sprintf("slow_consumer: subscriber queue full before endpoint seq %d; reconnect required", endpointSeq),
	}
	select {
	case sub.slow <- notice:
	default:
	}
}
