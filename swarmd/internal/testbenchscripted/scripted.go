// Package testbenchscripted replaces the model provider with a scripted HTTP
// endpoint so a real daemon (and a real sealed container) can be driven by a
// test harness that plays a hijacked model: it decides exactly which tool
// calls the "model" emits. It is wired in only by an explicit test build
// overlay (scripts/testbench-scripted-overlay.py); no production build imports
// it and no environment variable enables it in a normal daemon.
package testbenchscripted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"swarm/packages/swarmd/internal/identity"
	p "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	"swarm/packages/swarmd/internal/testbenchcodex"
)

const maxResponse = 4 << 20

// WireRequest is what the harness receives for each model step.
type WireRequest struct {
	SessionID    string           `json:"session_id"`
	Model        string           `json:"model"`
	Instructions string           `json:"instructions"`
	Tools        []WireTool       `json:"tools"`
	Input        []map[string]any `json:"input"`
}

type WireTool struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
}

// WireResponse is what the harness answers: text, tool calls, or both.
type WireResponse struct {
	Text          string `json:"text"`
	FunctionCalls []struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function_calls"`
}

type Client struct {
	url  string
	http *http.Client
}

func (c *Client) ID() string { return "codex" }

func (c *Client) Status(ctx context.Context) (p.Status, error) {
	return p.Status{ID: "codex", Ready: true, DefaultModel: testbenchcodex.Model, DefaultThinking: testbenchcodex.Thinking}, nil
}

func (c *Client) ExecutionEpochLifecycle() p.ExecutionEpochLifecycleCapabilities {
	return p.ExecutionEpochLifecycleCapabilities{ContextMode: p.ExecutionEpochContextResponsesChain, TransportReusable: true}
}

func (c *Client) CreateResponse(ctx context.Context, r p.Request) (p.Response, error) {
	return c.CreateResponseStreaming(ctx, r, nil)
}

func (c *Client) CreateResponseStreaming(ctx context.Context, r p.Request, onEvent func(p.StreamEvent)) (p.Response, error) {
	if principal, ok := identity.PrincipalFromContext(ctx); !ok || !principal.Valid() {
		return p.Response{}, identity.ErrPrincipalRequired
	}
	wire := WireRequest{SessionID: r.SessionID, Model: r.Model, Instructions: r.Instructions, Input: r.Input}
	for _, tool := range r.Tools {
		wire.Tools = append(wire.Tools, WireTool{Name: tool.Name, Parameters: tool.Parameters})
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return p.Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return p.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return p.Response{}, errors.New("scripted model unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.Response{}, errors.New("scripted model rejected request")
	}
	var out WireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&out); err != nil {
		return p.Response{}, errors.New("invalid scripted model response")
	}
	response := p.Response{Text: out.Text}
	for _, call := range out.FunctionCalls {
		response.FunctionCalls = append(response.FunctionCalls, p.FunctionCall{CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
	}
	if len(response.FunctionCalls) == 0 {
		response.StopReason = "stop"
	}
	if onEvent != nil && out.Text != "" {
		onEvent(p.StreamEvent{Type: p.StreamEventOutputTextDelta, Delta: out.Text})
	}
	return response, nil
}

// Registry is called only by the explicit scripted test overlay.
func Registry(url string) *registry.Registry {
	c := &Client{url: url, http: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}}
	r := registry.New(c)
	r.RegisterRunner(c)
	return r
}
