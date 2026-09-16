package testbenchcodex

// Purpose: the runner-only Broker.Handler must reject model drift and malformed
// operations before calling Codex, isolate lane affinity and serialize refresh
// ownership. Hermetic HTTP tests exercise the boundary without live credentials.
import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	p "swarm/packages/swarmd/internal/provider/interfaces"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrokerRejectsBeforeExecution(t *testing.T) {
	var calls atomic.Int32
	b := NewBroker(func(context.Context, codex.Request, func(codex.StreamEvent)) (codex.Response, error) {
		calls.Add(1)
		return codex.Response{}, nil
	}, func(context.Context) bool { return false })
	for _, body := range []string{`{}`, `{"Model":"gpt-5.6-luna","Thinking":"high"}`, `{"Model":"gpt-5.6-luna","Thinking":"medium","token":"secret"}`, `{"Model":"gpt-5.6-luna","Thinking":"medium"} {}`} {
		w := httptest.NewRecorder()
		b.Handler("lane").ServeHTTP(w, httptest.NewRequest("POST", "/response", bytes.NewBufferString(body)))
		if w.Code != 400 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	for _, path := range []string{"/credentials", "/response?url=https://example.invalid"} {
		w := httptest.NewRecorder()
		b.Handler("lane").ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected requests executed")
	}
	w := httptest.NewRecorder()
	b.Handler("lane").ServeHTTP(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != 503 {
		t.Fatal("missing login marked ready")
	}
}
func TestBrokerSerializesAndSeparatesLanes(t *testing.T) {
	var active, max atomic.Int32
	var mu sync.Mutex
	keys := map[string]bool{}
	b := NewBroker(func(_ context.Context, r codex.Request, _ func(codex.StreamEvent)) (codex.Response, error) {
		n := active.Add(1)
		if n > max.Load() {
			max.Store(n)
		}
		defer active.Add(-1)
		mu.Lock()
		keys[r.SessionAffinityKey] = true
		mu.Unlock()
		time.Sleep(time.Millisecond * 5)
		return codex.Response{ID: "result"}, nil
	}, func(context.Context) bool { return true })
	data, _ := json.Marshal(codex.Request{Model: Model, Thinking: Thinking, SessionAffinityKey: "same", SessionID: "same"})
	var wg sync.WaitGroup
	for _, lane := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			b.Handler(lane).ServeHTTP(w, httptest.NewRequest("POST", "/response", bytes.NewReader(data)))
			if w.Code != http.StatusOK {
				t.Error(w.Code)
			}
		}()
	}
	wg.Wait()
	if max.Load() != 1 || len(keys) != 2 || keys["same"] {
		t.Fatalf("serialization/isolation violated max=%d keys=%v", max.Load(), keys)
	}
}

// Purpose: the actual UNIX client/server boundary must carry streamed output
// without a candidate credential and reject missing principals before transport.
func TestUnixClientStreamsWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	broker := NewBroker(func(_ context.Context, _ codex.Request, emit func(codex.StreamEvent)) (codex.Response, error) {
		emit(codex.StreamEvent{Type: codex.StreamEventOutputTextDelta, Delta: "hello"})
		return codex.Response{ID: "result"}, nil
	}, func(context.Context) bool { return true })
	server := &http.Server{Handler: broker.Handler("test-lane")}
	defer server.Close()
	go server.Serve(listener)
	client := NewClient(path)
	request := p.Request{Model: Model, Thinking: Thinking}
	if _, err := client.CreateResponse(context.Background(), request); err == nil {
		t.Fatal("missing principal accepted")
	}
	ctx := identity.ContextWithPrincipal(context.Background(), identity.Principal{Type: identity.PrincipalTypeUser, UserID: "test-user", AccountScopeID: "test-account"})
	status, err := client.Status(ctx)
	if err != nil || !status.Ready {
		t.Fatal("broker not ready", err)
	}
	delta := ""
	out, err := client.CreateResponseStreaming(ctx, request, func(e p.StreamEvent) { delta += e.Delta })
	if err != nil || out.ID != "result" || delta != "hello" {
		t.Fatalf("stream: %+v %q %v", out, delta, err)
	}
}
