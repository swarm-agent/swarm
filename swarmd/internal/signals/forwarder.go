package signals

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Delivery protocol, version 1. Each POST carries a JSON body:
//
//	{"version":1,"sink_id":"sink_…","machine":"name","sent_at":<ms>,
//	 "heartbeat":bool,"signals":[…],"next_after":N,"latest_seq":N,"gap":bool}
//
// and the headers X-Swarm-Sink (sink id), X-Swarm-Timestamp (sent_at, ms) and
// X-Swarm-Signature: "v1=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
// A receiver recomputes the signature with the sink secret, rejects a
// timestamp far from its clock (replays), and drops duplicate signal ids:
// delivery is at least once. Any 2xx accepts the batch; anything else
// (including a redirect) is retried later with backoff.
const (
	DeliveryVersion       = 1
	HeaderSink            = "X-Swarm-Sink"
	HeaderTimestamp       = "X-Swarm-Timestamp"
	HeaderSignature       = "X-Swarm-Signature"
	signatureVersion      = "v1="
	deliveryBatch         = 100
	forwarderTick         = 5 * time.Second
	deliveryTimeout       = 15 * time.Second
	maxDeliveryBackoff    = 10 * time.Minute
	maxReceiverReplyBytes = 64 << 10
)

// Sign returns the X-Swarm-Signature value for body sent at timestampMilli.
func Sign(secret string, timestampMilli int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMilli, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signatureVersion + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature matches body and timestampMilli under
// secret, and the timestamp is within maxSkew of now.
func Verify(secret string, timestampMilli int64, body []byte, signature string, now time.Time, maxSkew time.Duration) bool {
	if d := now.Sub(time.UnixMilli(timestampMilli)); d > maxSkew || d < -maxSkew {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, timestampMilli, body)), []byte(signature))
}

type deliveryBody struct {
	Version   int                  `json:"version"`
	SinkID    string               `json:"sink_id"`
	Machine   string               `json:"machine,omitempty"`
	SentAt    int64                `json:"sent_at"`
	Heartbeat bool                 `json:"heartbeat"`
	Signals   []pebblestore.Signal `json:"signals"`
	NextAfter uint64               `json:"next_after"`
	LatestSeq uint64               `json:"latest_seq"`
	Gap       bool                 `json:"gap"`
}

// Forwarder pushes each sink's share of the signal feed to its URL. It only
// sends; it never accepts input from a sink.
type Forwarder struct {
	feed    *pebblestore.SignalStore
	sinks   *pebblestore.SignalSinkStore
	client  *http.Client
	machine func() string
	now     func() time.Time

	mu       sync.Mutex
	lastSent map[string]time.Time
	retryAt  map[string]time.Time
}

func NewForwarder(feed *pebblestore.SignalStore, sinks *pebblestore.SignalSinkStore, machine func() string) *Forwarder {
	return &Forwarder{
		feed:  feed,
		sinks: sinks,
		client: &http.Client{
			Timeout: deliveryTimeout,
			// A sink is the exact URL the owner configured; never follow a
			// redirect somewhere else with the machine's signals.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		machine:  machine,
		now:      time.Now,
		lastSent: map[string]time.Time{},
		retryAt:  map[string]time.Time{},
	}
}

// Run delivers until ctx ends: promptly after new signals, and on a short tick
// for heartbeats and retries.
func (f *Forwarder) Run(ctx context.Context) {
	ticker := time.NewTicker(forwarderTick)
	defer ticker.Stop()
	for {
		f.DeliverOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-f.feed.Appended():
		}
	}
}

// DeliverOnce makes at most one delivery attempt per sink that is due.
func (f *Forwarder) DeliverOnce(ctx context.Context) {
	sinks, err := f.sinks.ListAll()
	if err != nil {
		log.Printf("swarmd signal forwarder: list sinks: %v", err)
		return
	}
	for _, sink := range sinks {
		if ctx.Err() != nil {
			return
		}
		f.deliver(ctx, sink)
	}
}

func (f *Forwarder) deliver(ctx context.Context, sink pebblestore.SignalSink) {
	now := f.now()
	f.mu.Lock()
	retryAt := f.retryAt[sink.ID]
	lastSent := f.lastSent[sink.ID]
	f.mu.Unlock()
	if now.Before(retryAt) {
		return
	}
	page, err := f.feed.ListAfter(sink.Cursor, deliveryBatch, pebblestore.SignalFilter{Account: sink.Account, KindPrefixes: sink.KindPrefixes, MinSeverity: sink.MinSeverity})
	if err != nil {
		log.Printf("swarmd signal forwarder: read feed for %s: %v", sink.ID, err)
		return
	}
	heartbeatDue := now.Sub(lastSent) >= time.Duration(sink.HeartbeatSeconds)*time.Second
	if len(page.Signals) == 0 && !page.Gap && !heartbeatDue {
		if page.NextAfter > sink.Cursor {
			// Only filtered-out signals: move the cursor past them quietly.
			_ = f.sinks.RecordDelivery(sink.Account, sink.ID, page.NextAfter, nil)
		}
		return
	}
	sendErr := f.post(ctx, sink, page, len(page.Signals) == 0, now)
	if recordErr := f.sinks.RecordDelivery(sink.Account, sink.ID, page.NextAfter, sendErr); recordErr != nil {
		log.Printf("swarmd signal forwarder: record delivery for %s: %v", sink.ID, recordErr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if sendErr != nil {
		backoff := forwarderTick << min(sink.Failures, 8)
		if backoff > maxDeliveryBackoff {
			backoff = maxDeliveryBackoff
		}
		f.retryAt[sink.ID] = now.Add(backoff)
		return
	}
	f.lastSent[sink.ID] = now
	delete(f.retryAt, sink.ID)
}

func (f *Forwarder) post(ctx context.Context, sink pebblestore.SignalSink, page pebblestore.SignalPage, heartbeat bool, now time.Time) error {
	machine := ""
	if f.machine != nil {
		machine = f.machine()
	}
	sentAt := now.UnixMilli()
	body, err := json.Marshal(deliveryBody{
		Version:   DeliveryVersion,
		SinkID:    sink.ID,
		Machine:   machine,
		SentAt:    sentAt,
		Heartbeat: heartbeat,
		Signals:   page.Signals,
		NextAfter: page.NextAfter,
		LatestSeq: page.LatestSeq,
		Gap:       page.Gap,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sink.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "swarmd-signals/1")
	req.Header.Set(HeaderSink, sink.ID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(sentAt, 10))
	req.Header.Set(HeaderSignature, Sign(sink.Secret, sentAt, body))
	resp, err := f.client.Do(req)
	if err != nil {
		// Keep the cause only: the URL may carry a token in its query.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("deliver: %w", urlErr.Err)
		}
		return fmt.Errorf("deliver: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiverReplyBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered HTTP %d", resp.StatusCode)
	}
	return nil
}
