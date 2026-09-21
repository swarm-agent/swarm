package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EventOccurrenceStarted        = "worker.occurrence.started"
	EventOccurrenceSucceeded      = "worker.occurrence.succeeded"
	EventOccurrenceFailed         = "worker.occurrence.failed"
	EventOccurrenceRetryExhausted = "worker.occurrence.retry_exhausted"
	EventTestPing                 = "worker.test_ping"
)

type WebhookEvent struct {
	Type           string         `json:"event"`
	EventID        string         `json:"event_id"`
	Timestamp      int64          `json:"timestamp"`
	AccountID      string         `json:"account_id"`
	WorkspaceID    string         `json:"workspace_id,omitempty"`
	WorkerID       string         `json:"worker_id,omitempty"`
	WorkerTitle    string         `json:"worker_title,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	OccurrenceID   string         `json:"occurrence_id,omitempty"`
	State          string         `json:"state,omitempty"`
	Detail         string         `json:"detail,omitempty"`
	AttemptCount   int            `json:"attempt_count,omitempty"`
	TriggerContext map[string]any `json:"trigger_context,omitempty"`
}

type Destination struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Secret  string   `json:"secret,omitempty"`
	Format  string   `json:"format,omitempty"` // "generic" | "slack" | "discord" | "telegram"
	Events  []string `json:"events,omitempty"`
	Enabled bool     `json:"enabled"`
}

type DeliveryResult struct {
	StatusCode int    `json:"status_code"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Body       string `json:"body,omitempty"`
	Signature  string `json:"signature,omitempty"`
}

type deliveryJob struct {
	event WebhookEvent
	dest  Destination
}

type Dispatcher struct {
	client    *http.Client
	queue     chan deliveryJob
	wg        sync.WaitGroup
	closed    atomic.Bool
	closeOnce sync.Once
}

func NewDispatcher(client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	d := &Dispatcher{
		client: client,
		queue:  make(chan deliveryJob, 256),
	}
	for i := 0; i < 4; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.queue)
		d.wg.Wait()
	})
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for job := range d.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = d.DeliverSync(ctx, job.event, job.dest)
		cancel()
	}
}

func (d *Dispatcher) DispatchAsync(event WebhookEvent, destinations []Destination) {
	if d == nil || d.closed.Load() {
		return
	}
	if event.EventID == "" {
		event.EventID = generateEventID()
	}
	if event.Timestamp <= 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	for _, dest := range destinations {
		if !dest.Enabled || !MatchesEvent(dest.Events, event.Type) {
			continue
		}
		select {
		case d.queue <- deliveryJob{event: event, dest: dest}:
		default:
			log.Printf("webhook queue full, dropping delivery for %s", dest.URL)
		}
	}
}

func (d *Dispatcher) DeliverSync(ctx context.Context, event WebhookEvent, dest Destination) (DeliveryResult, error) {
	var result DeliveryResult
	if d == nil || d.client == nil {
		return result, errors.New("dispatcher client not configured")
	}
	if event.EventID == "" {
		event.EventID = generateEventID()
	}
	if event.Timestamp <= 0 {
		event.Timestamp = time.Now().UnixMilli()
	}

	payload, contentType, err := FormatPayload(event, dest.Format)
	if err != nil {
		return result, fmt.Errorf("format payload failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest.URL, bytes.NewReader(payload))
	if err != nil {
		return result, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Swarm-Webhook/1.0")
	req.Header.Set("X-Swarm-Event", event.Type)
	req.Header.Set("X-Swarm-Delivery", event.EventID)
	req.Header.Set("X-Swarm-Timestamp", fmt.Sprintf("%d", event.Timestamp))

	if dest.Secret != "" {
		sig := ComputeSignature(dest.Secret, event.Timestamp, payload)
		req.Header.Set("X-Swarm-Signature", "sha256="+sig)
		result.Signature = "sha256=" + sig
	}

	start := time.Now()
	resp, err := d.client.Do(req)
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	result.Status = resp.Status

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result.Body = string(bodyBytes)

	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("webhook endpoint returned HTTP %d: %s", resp.StatusCode, result.Body)
	}
	return result, nil
}

func MatchesEvent(destEvents []string, eventType string) bool {
	if eventType == EventTestPing {
		return true
	}
	if len(destEvents) == 0 {
		return true
	}
	for _, e := range destEvents {
		clean := strings.ToLower(strings.TrimSpace(e))
		if clean == "*" || clean == "all" {
			return true
		}
		if clean == eventType || strings.HasSuffix(eventType, "."+clean) {
			return true
		}
	}
	return false
}

func ComputeSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func FormatPayload(event WebhookEvent, format string) ([]byte, string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "slack":
		title := event.WorkerTitle
		if title == "" {
			title = event.WorkerID
		}
		stateText := strings.ToUpper(event.State)
		if stateText == "" {
			stateText = event.Type
		}
		text := fmt.Sprintf("🤖 *[Swarm Worker]* *%s* -> *%s*\n> *Detail*: %s\n> *Worker ID*: `%s`",
			title, stateText, event.Detail, event.WorkerID)
		if event.AttemptCount > 1 {
			text += fmt.Sprintf("\n> *Attempt*: %d", event.AttemptCount)
		}
		b, err := json.Marshal(map[string]string{"text": text})
		return b, "application/json", err

	case "discord":
		title := event.WorkerTitle
		if title == "" {
			title = event.WorkerID
		}
		stateText := strings.ToUpper(event.State)
		if stateText == "" {
			stateText = event.Type
		}
		content := fmt.Sprintf("🤖 **[Swarm Worker]** **%s** -> **%s**\n> **Detail**: %s\n> **Worker ID**: `%s`",
			title, stateText, event.Detail, event.WorkerID)
		if event.AttemptCount > 1 {
			content += fmt.Sprintf("\n> **Attempt**: %d", event.AttemptCount)
		}
		b, err := json.Marshal(map[string]string{"content": content})
		return b, "application/json", err

	case "telegram":
		title := event.WorkerTitle
		if title == "" {
			title = event.WorkerID
		}
		stateText := strings.ToUpper(event.State)
		if stateText == "" {
			stateText = event.Type
		}
		text := fmt.Sprintf("🤖 [Swarm Worker] %s -> %s\nDetail: %s\nWorker ID: %s",
			title, stateText, event.Detail, event.WorkerID)
		if event.AttemptCount > 1 {
			text += fmt.Sprintf("\nAttempt: %d", event.AttemptCount)
		}
		b, err := json.Marshal(map[string]string{"text": text})
		return b, "application/json", err

	default: // "generic" or empty
		b, err := json.Marshal(event)
		return b, "application/json", err
	}
}

func generateEventID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "whk_evt_" + hex.EncodeToString(b)
}
