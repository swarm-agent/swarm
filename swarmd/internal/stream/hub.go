package stream

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/transport/ws"
)

const (
	defaultSendQueueSize = 512
	replayLimit          = 2000
)

type Hub struct {
	events  *pebblestore.EventLog
	mu      sync.RWMutex
	clients map[string]*clientConn
	nextID  atomic.Uint64
}

type HubStats struct {
	ConnectedClients int `json:"connected_clients"`
}

type clientConn struct {
	id    string
	conn  *ws.Conn
	send  chan []byte
	done  chan struct{}
	subs  map[string]struct{}
	subsM sync.RWMutex
}

type inbound struct {
	Type          string          `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	LastSeenSeq   uint64          `json:"last_seen_seq,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type outbound struct {
	Type          string                     `json:"type"`
	OK            bool                       `json:"ok,omitempty"`
	Message       string                     `json:"message,omitempty"`
	Channel       string                     `json:"channel,omitempty"`
	ClientID      string                     `json:"client_id,omitempty"`
	CorrelationID string                     `json:"correlation_id,omitempty"`
	SentAtUnixMS  int64                      `json:"sent_at_unix_ms"`
	Event         *pebblestore.EventEnvelope `json:"event,omitempty"`
	Dropped       bool                       `json:"dropped,omitempty"`
}

func NewHub(events *pebblestore.EventLog) *Hub {
	return &Hub{
		events:  events,
		clients: make(map[string]*clientConn),
	}
}

func (h *Hub) Stats() HubStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HubStats{ConnectedClients: len(h.clients)}
}

func (h *Hub) HasClients() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients) > 0
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Accept(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	if clientID == "" {
		clientID = fmt.Sprintf("client_%06d", h.nextID.Add(1))
	}

	client := &clientConn{
		id:   clientID,
		conn: conn,
		send: make(chan []byte, defaultSendQueueSize),
		done: make(chan struct{}),
		subs: make(map[string]struct{}),
	}
	h.addClient(client)
	defer h.removeClient(client.id)

	go client.writeLoop()
	client.enqueue(outbound{Type: "connected", OK: true, ClientID: client.id, SentAtUnixMS: time.Now().UnixMilli()})

	for {
		raw, err := client.conn.ReadText()
		if err != nil {
			return
		}
		if err := h.handleInbound(client, raw); err != nil {
			client.enqueue(outbound{Type: "error", OK: false, Message: err.Error(), SentAtUnixMS: time.Now().UnixMilli()})
		}
	}
}

func (h *Hub) Publish(event pebblestore.EventEnvelope) {
	msg := outbound{
		Type:         "event",
		OK:           true,
		SentAtUnixMS: time.Now().UnixMilli(),
		Event:        &event,
	}

	h.mu.RLock()
	overflow := make([]string, 0, 4)

	for _, client := range h.clients {
		if !client.matches(event.Stream) {
			continue
		}
		if !client.enqueue(msg) {
			overflow = append(overflow, client.id)
		}
	}
	h.mu.RUnlock()

	for _, clientID := range overflow {
		log.Printf("stream: closing slow client %s (queue overflow)", clientID)
		h.removeClient(clientID)
	}
}

func (h *Hub) addClient(client *clientConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client.id] = client
}

func (h *Hub) removeClient(id string) {
	h.mu.Lock()
	client, ok := h.clients[id]
	if ok {
		delete(h.clients, id)
	}
	h.mu.Unlock()

	if ok {
		close(client.done)
		_ = client.conn.Close()
	}
}

func (h *Hub) handleInbound(client *clientConn, raw []byte) error {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("invalid websocket payload: %w", err)
	}

	switch msg.Type {
	case "ping":
		client.enqueue(outbound{Type: "pong", OK: true, CorrelationID: msg.CorrelationID, SentAtUnixMS: time.Now().UnixMilli()})
		return nil
	case "subscribe":
		channel := normalizeChannel(msg.Channel)
		if channel == "" {
			return fmt.Errorf("subscribe requires channel")
		}
		client.subscribe(channel)
		client.enqueue(outbound{Type: "subscribed", OK: true, Channel: channel, CorrelationID: msg.CorrelationID, SentAtUnixMS: time.Now().UnixMilli()})
		if msg.LastSeenSeq > 0 {
			return h.replayToClient(client, channel, msg.LastSeenSeq)
		}
		return nil
	case "unsubscribe":
		channel := normalizeChannel(msg.Channel)
		if channel == "" {
			return fmt.Errorf("unsubscribe requires channel")
		}
		client.unsubscribe(channel)
		client.enqueue(outbound{Type: "unsubscribed", OK: true, Channel: channel, CorrelationID: msg.CorrelationID, SentAtUnixMS: time.Now().UnixMilli()})
		return nil
	case "resume":
		channel := normalizeChannel(msg.Channel)
		if channel == "" {
			return fmt.Errorf("resume requires channel")
		}
		return h.replayToClient(client, channel, msg.LastSeenSeq)
	default:
		return fmt.Errorf("unsupported message type %q", msg.Type)
	}
}

func (h *Hub) replayToClient(client *clientConn, channel string, lastSeen uint64) error {
	start := uint64(1)
	if lastSeen > 0 {
		start = lastSeen + 1
	}
	events, err := h.events.ReadFrom(start, replayLimit)
	if err != nil {
		return fmt.Errorf("replay events: %w", err)
	}
	for i := range events {
		evt := events[i]
		if !matchesChannel(channel, evt.Stream) {
			continue
		}
		client.enqueue(outbound{Type: "event", OK: true, Event: &evt, SentAtUnixMS: time.Now().UnixMilli()})
	}
	client.enqueue(outbound{
		Type:         "resume-complete",
		OK:           true,
		Channel:      channel,
		Message:      "replay complete",
		SentAtUnixMS: time.Now().UnixMilli(),
	})
	return nil
}

func (c *clientConn) writeLoop() {
	for {
		select {
		case payload := <-c.send:
			if err := c.conn.WriteText(payload); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *clientConn) enqueue(msg outbound) bool {
	if msg.SentAtUnixMS == 0 {
		msg.SentAtUnixMS = time.Now().UnixMilli()
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return false
	}

	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (c *clientConn) subscribe(channel string) {
	c.subsM.Lock()
	defer c.subsM.Unlock()
	c.subs[channel] = struct{}{}
}

func (c *clientConn) unsubscribe(channel string) {
	c.subsM.Lock()
	defer c.subsM.Unlock()
	delete(c.subs, channel)
}

func (c *clientConn) matches(stream string) bool {
	c.subsM.RLock()
	defer c.subsM.RUnlock()
	if len(c.subs) == 0 {
		return false
	}
	for pattern := range c.subs {
		if matchesChannel(pattern, stream) {
			return true
		}
	}
	return false
}

func normalizeChannel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return value
}

func matchesChannel(pattern, stream string) bool {
	if pattern == stream {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(stream, prefix)
	}
	return false
}

func ParseLastSeen(value string) uint64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	seq, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return seq
}
