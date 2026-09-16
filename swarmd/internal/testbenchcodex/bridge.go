// Package testbenchcodex is a runner-only bridge, never registered by host Swarm.
// Candidate builds opt in through the maintained Go overlay. OAuth credentials
// stay in the separate broker process; candidates receive response data only.
package testbenchcodex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	p "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
)

const Model = "gpt-5.6-luna"
const Thinking = "medium"
const MaxRequest = 24 << 20
const MaxResponse = 32 << 20

// Wire requests have no credential fields or arbitrary destination. The socket
// endpoint is provisioned for one lane, whose prefix is assigned by the broker.
type Frame struct {
	Event    *codex.StreamEvent `json:"event,omitempty"`
	Response *codex.Response    `json:"response,omitempty"`
	Error    string             `json:"error,omitempty"`
}
type Execute func(context.Context, codex.Request, func(codex.StreamEvent)) (codex.Response, error)
type Broker struct {
	gate    chan struct{}
	pending chan struct{}
	execute Execute
	ready   func(context.Context) bool
}

func NewBroker(execute Execute, ready func(context.Context) bool) *Broker {
	return &Broker{gate: make(chan struct{}, 1), pending: make(chan struct{}, 8), execute: execute, ready: ready}
}

// Handler is bound by trusted provisioning, never by a client-supplied lane ID.
// The single gate spans refresh, persistence and response execution. This trades
// throughput for an unambiguous single refresh owner across candidate lanes.
func (b *Broker) Handler(lane string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case b.pending <- struct{}{}:
			defer func() { <-b.pending }()
		default:
			http.Error(w, "broker capacity exhausted", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		select {
		case b.gate <- struct{}{}:
			defer func() { <-b.gate }()
		case <-ctx.Done():
			http.Error(w, "broker busy", 503)
			return
		}
		if lane == "" {
			http.Error(w, "lane required", 403)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/ready" && r.URL.RawQuery == "" {
			if !b.ready(ctx) {
				http.Error(w, "dedicated login required", 503)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/response" || r.URL.RawQuery != "" {
			http.Error(w, "unsupported operation", 404)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequest)
		var req codex.Request
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			http.Error(w, "trailing request data", 400)
			return
		}
		if req.Model != Model || req.Thinking != Thinking || (req.ReasoningProviderValue != "" && req.ReasoningProviderValue != Thinking) || req.ServiceTier != "" || req.ContextMode != "" {
			http.Error(w, "Codex Luna medium required; overrides unsupported", 400)
			return
		}
		// Avoid cache/continuation collisions between independently writable databases.
		prefix := func(v string) string {
			if v == "" {
				return ""
			}
			sum := sha256.Sum256([]byte(lane + "\x00" + v))
			return hex.EncodeToString(sum[:])
		}
		req.SessionID = prefix(req.SessionID)
		req.ProviderLineageID = prefix(req.ProviderLineageID)
		req.ProviderCacheKey = prefix(req.ProviderCacheKey)
		req.SessionAffinityKey = prefix(req.SessionAffinityKey)
		req.TransportAffinityKey = prefix(req.TransportAffinityKey)
		req.ContextBranchID = prefix(req.ContextBranchID)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		written := 0
		var writeErr error
		var mu sync.Mutex
		send := func(f Frame) {
			mu.Lock()
			defer mu.Unlock()
			if writeErr != nil {
				return
			}
			data, err := json.Marshal(f)
			if err != nil || written+len(data)+1 > MaxResponse {
				writeErr = errors.New("response limit")
				cancel()
				return
			}
			written += len(data) + 1
			writeErr = enc.Encode(f)
			if writeErr != nil {
				cancel()
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		out, err := b.execute(ctx, req, func(e codex.StreamEvent) { send(Frame{Event: &e}) })
		if err != nil {
			send(Frame{Error: "Codex request failed; login may need renewal"})
			return
		}
		send(Frame{Response: &out})
	})
}

type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	transport := &http.Transport{MaxConnsPerHost: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 6 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}}
}
func (c *Client) ID() string { return "codex" }
func (c *Client) Status(ctx context.Context) (p.Status, error) {
	s := p.Status{ID: "codex", DefaultModel: Model, DefaultThinking: Thinking, Reason: "dedicated testbench broker unavailable"}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return s, identity.ErrPrincipalRequired
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://broker/ready", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return s, nil
	}
	defer resp.Body.Close()
	s.Ready = resp.StatusCode == 204
	if s.Ready {
		s.Reason = ""
	}
	return s, nil
}
func (c *Client) ExecutionEpochLifecycle() p.ExecutionEpochLifecycleCapabilities {
	return p.ExecutionEpochLifecycleCapabilities{ContextMode: p.ExecutionEpochContextResponsesChain, EpochScopedCacheKey: true, EpochScopedSessionAffinity: true, TransportReusable: true}
}
func (c *Client) CreateResponse(ctx context.Context, r p.Request) (p.Response, error) {
	return c.CreateResponseStreaming(ctx, r, nil)
}
func (c *Client) CreateResponseStreaming(ctx context.Context, r p.Request, onEvent func(p.StreamEvent)) (p.Response, error) {
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return p.Response{}, identity.ErrPrincipalRequired
	}
	if r.Model != Model || r.Thinking != Thinking {
		return p.Response{}, errors.New("testbench requires Codex Luna medium")
	}
	wire := codex.ToRequest(r)
	// Account separation remains required inside each candidate lane.
	sum := sha256.Sum256([]byte(principal.AccountScopeID + "\x00" + wire.SessionID))
	wire.SessionID = hex.EncodeToString(sum[:])
	scope := func(v string) string {
		if v == "" {
			return ""
		}
		h := sha256.Sum256([]byte(principal.AccountScopeID + "\x00" + v))
		return hex.EncodeToString(h[:])
	}
	wire.ProviderLineageID = scope(wire.ProviderLineageID)
	wire.ProviderCacheKey = scope(wire.ProviderCacheKey)
	wire.SessionAffinityKey = scope(wire.SessionAffinityKey)
	wire.TransportAffinityKey = scope(wire.TransportAffinityKey)
	wire.ContextBranchID = scope(wire.ContextBranchID)
	data, err := json.Marshal(wire)
	if err != nil || len(data) > MaxRequest {
		return p.Response{}, errors.New("invalid bridge request")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://broker/response", bytes.NewReader(data))
	if err != nil {
		return p.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return p.Response{}, errors.New("testbench broker unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return p.Response{}, errors.New("testbench broker rejected request")
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, MaxResponse+1))
	emit := codex.ToProviderStreamEventCallbackWithContext(onEvent, "codex", Model)
	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			return p.Response{}, errors.New("incomplete broker response")
		}
		if frame.Error != "" {
			return p.Response{}, errors.New("Codex broker request failed")
		}
		if frame.Event != nil && onEvent != nil {
			emit(*frame.Event)
		}
		if frame.Response != nil {
			return codex.FromResponse(*frame.Response), nil
		}
	}
}

// Registry is called only by an explicit testbench build overlay. No environment
// variable or production runtime route can enable this bridge in host Swarm.
func Registry(socket string) *registry.Registry {
	c := NewClient(socket)
	r := registry.New(c)
	r.RegisterRunner(c)
	return r
}
