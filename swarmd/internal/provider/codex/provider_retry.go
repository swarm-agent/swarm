package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	store "swarm/packages/swarmd/internal/store/pebble"
)

const providerTurnAttempts = 4
const providerRetryAfterKey = "_swarm_retry_after"

type providerTurnStreamedKey struct{}

func providerTurnAlreadyStreamed(ctx context.Context) bool {
	streamed, _ := ctx.Value(providerTurnStreamedKey{}).(bool)
	return streamed
}

var errResponseStreamIncomplete = errors.New("codex response stream ended before response.completed")

func transientProviderFailure(status int, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if status >= 400 {
		return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
	}
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, errResponseStreamIncomplete) {
		return true
	}
	return shouldRetryStartedWebsocketStream(err) || isWebsocketServiceRestart(err) || websocket.IsUnexpectedCloseError(err, websocket.ClosePolicyViolation, websocket.CloseUnsupportedData)
}

func providerRetryDelay(attempt int, decoded map[string]any) time.Duration {
	delay := 10 * time.Second * time.Duration(1<<(attempt-1))
	value, _ := decoded[providerRetryAfterKey].(string)
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
		if seconds >= 40 {
			return 40 * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil {
		if d := time.Until(deadline); d > 0 {
			return min(d, 40*time.Second)
		}
	}
	return min(delay, 40*time.Second)
}

// Retry only the provider turn: no worker restart, tool execution, or Git mutation.
// Preserve emitted text and withhold tool construction until a complete replay.
func (c *Client) sendRequest(ctx context.Context, record store.CodexAuthRecord, req Request, onEvent func(StreamEvent)) (map[string]any, int, error) {
	emitter := &retryAwareStreamEmitter{onEvent: onEvent}
	for attempt := 1; attempt <= providerTurnAttempts; attempt++ {
		if err := contextErr(ctx); err != nil {
			return nil, 0, err
		}
		emitter.beginAttempt()
		decoded, status, err := c.sendRequestOnce(ctx, record, req, emitter.emit)
		if ctxErr := contextErr(ctx); ctxErr != nil {
			return nil, 0, ctxErr
		}
		if emitter.replayErr != nil {
			return nil, 0, emitter.replayErr
		}
		if !transientProviderFailure(status, err) {
			if err == nil && status < 400 {
				if emitter.attemptOutputText != emitter.emittedOutputText {
					return nil, 0, errors.New("codex reconnect ended before preserved partial output was replayed")
				}
				emitter.completeAttempt()
			}
			return decoded, status, err
		}
		if attempt == providerTurnAttempts {
			reason := "stream interrupted"
			if status >= 400 {
				reason = fmt.Sprintf("HTTP %d", status)
			}
			return nil, status, fmt.Errorf("provider unavailable (%s) after %d attempts", reason, attempt)
		}
		reason := "stream interrupted"
		if status >= 400 {
			reason = fmt.Sprintf("HTTP %d", status)
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: StreamEventAssistantCommentary, Delta: fmt.Sprintf("\nRetrying provider (%s), attempt %d/%d\n", reason, attempt+1, providerTurnAttempts)})
		} else {
			log.Printf("codex provider retry status=%s attempt=%d/%d", reason, attempt+1, providerTurnAttempts)
		}
		wait := c.reconnectWaitFn
		if wait == nil {
			wait = sleepWithContext
		}
		if err := wait(ctx, providerRetryDelay(attempt, decoded)); err != nil {
			return nil, 0, err
		}
		// A failed socket's continuation is not authoritative. Rebuild full input
		// for the same turn, retaining the run and all previously committed work.
		req.ResetTransport = true
		req.AllowContinuation = false
		ctx = c.contextWithResponsesLifecycle(ctx, record, req, true)
		ctx = context.WithValue(ctx, providerTurnStreamedKey{}, emitter.sawEvent)
	}
	panic("unreachable provider retry bound")
}
