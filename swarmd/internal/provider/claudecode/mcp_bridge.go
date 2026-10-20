package claudecode

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

const (
	mcpServerName          = "swarm"
	mcpToolPrefix          = "mcp__" + mcpServerName + "__"
	mcpDefaultProtocol     = "2025-06-18"
	mcpMaxRequestBytes     = 32 << 20
	mcpToolUseIDMetaKey    = "claudecode/toolUseId"
	mcpCorrelationWaitTime = 2 * time.Second
	// mcpToolTimeoutMargin is how much longer than Swarm's longest tool call
	// (provideriface.MaxToolCallDuration) the CLI waits for one, so the
	// tool's own timeout always ends a long command first and reports it.
	mcpToolTimeoutMargin = 2 * time.Minute
)

// While a tool call runs, the bridge keeps its HTTP answer alive with an
// event every mcpStreamKeepAlive (a progress notification when the client
// asked for progress, otherwise an event-stream comment), and tells the
// provider-attempt watchdog every mcpToolHeartbeat that the turn is waiting
// on Swarm, not stuck. Variables so tests can shorten them.
var (
	mcpStreamKeepAlive = 15 * time.Second
	mcpToolHeartbeat   = 30 * time.Second
)

// MCPToolTimeoutMillis is the MCP tool timeout the Claude Code CLI runs
// with: Swarm's longest tool call plus mcpToolTimeoutMargin.
func MCPToolTimeoutMillis() int64 {
	return (provideriface.MaxToolCallDuration + mcpToolTimeoutMargin).Milliseconds()
}

// mcpToolTimeoutEnv is the one place that gives the Claude Code CLI its MCP
// tool timeouts, both set to MCPToolTimeoutMillis:
//
//   - MCP_TOOL_TIMEOUT: the CLI's per-call timeout (and, per the earlier
//     inspection noted at streamToolCall, its per-POST timer).
//   - CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT: the CLI's idle watchdog for a tool
//     call that sends no progress. Earlier inspection of the published CLI
//     2.1.303 (not repeated here) found it defaults to 300 s for an HTTP
//     server and that progress notifications reset it only when the CLI
//     itself passes _meta.progressToken on tools/call.
//
// What was checked for this job: streamToolCall sends notifications/progress
// only when tools/call carries _meta.progressToken (otherwise just
// event-stream comments). No Claude Code CLI binary or package was available
// in the sandbox to check whether the CLI sends that token, so the bridge's
// progress cannot be relied on to reset the idle watchdog, and the watchdog
// is raised to Swarm's own limit instead. The unit is assumed to be
// milliseconds like MCP_TOOL_TIMEOUT; were it seconds, the value would be
// longer still, so a call within Swarm's limit is never cut short either way.
func mcpToolTimeoutEnv() []string {
	value := strconv.FormatInt(MCPToolTimeoutMillis(), 10)
	return []string{
		"MCP_TOOL_TIMEOUT=" + value,
		"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=" + value,
	}
}

var mcpSupportedProtocols = map[string]struct{}{
	"2024-11-05": {},
	"2025-03-26": {},
	"2025-06-18": {},
	"2025-11-25": {},
}

var mcpToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// mcpBridge exposes one turn's Swarm tools to the Claude Code CLI over a
// loopback-only MCP endpoint guarded by a per-turn bearer token. Every tool call
// is executed by the Swarm ToolInvoker, so Swarm permissions, persistence and
// workspace authority apply exactly as for any other provider.
type mcpBridge struct {
	ctx      context.Context
	invoker  provideriface.ToolInvoker
	tools    []mcpTool
	byName   map[string]string
	token    string
	listener net.Listener
	server   *http.Server

	onRestart   func()
	correlation *toolUseCorrelator
	callSeq     atomic.Int64
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

func newMCPBridge(ctx context.Context, definitions []provideriface.ToolDefinition, invoker provideriface.ToolInvoker, correlation *toolUseCorrelator, onRestart func()) (*mcpBridge, error) {
	if invoker == nil {
		return nil, errors.New("claude code tools require a Swarm tool invoker")
	}
	tools, byName, err := buildMCPTools(definitions)
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for claude code tool bridge: %w", err)
	}
	bridge := &mcpBridge{
		ctx:         ctx,
		invoker:     invoker,
		tools:       tools,
		byName:      byName,
		token:       token,
		listener:    listener,
		onRestart:   onRestart,
		correlation: correlation,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", bridge.handle)
	bridge.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = bridge.server.Serve(listener) }()
	return bridge, nil
}

func (b *mcpBridge) URL() string {
	return "http://" + b.listener.Addr().String() + "/mcp"
}

// Config is the --mcp-config document. It holds the per-turn bearer token and
// must only be written to a private (0600) file, never passed on argv.
func (b *mcpBridge) Config() ([]byte, error) {
	return json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			mcpServerName: map[string]any{
				"type":    "http",
				"url":     b.URL(),
				"headers": map[string]string{"Authorization": "Bearer " + b.token},
			},
		},
	})
}

func (b *mcpBridge) Close() {
	if b == nil || b.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.server.Shutdown(ctx)
}

func buildMCPTools(definitions []provideriface.ToolDefinition) ([]mcpTool, map[string]string, error) {
	tools := make([]mcpTool, 0, len(definitions))
	byName := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		original := strings.TrimSpace(definition.Name)
		if original == "" {
			continue
		}
		if !mcpToolNamePattern.MatchString(original) {
			return nil, nil, fmt.Errorf("swarm tool %q cannot be exposed to Claude Code: MCP tool names must match %s", original, mcpToolNamePattern.String())
		}
		if _, exists := byName[original]; exists {
			return nil, nil, fmt.Errorf("duplicate swarm tool %q", original)
		}
		byName[original] = original
		schema := cloneJSONMap(definition.Parameters)
		if schema == nil {
			schema = map[string]any{}
		}
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}
		tools = append(tools, mcpTool{Name: original, Description: strings.TrimSpace(definition.Description), InputSchema: schema})
	}
	return tools, byName, nil
}

type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

func (b *mcpBridge) handle(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default:
		// No server-initiated stream: this bridge only answers requests.
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxRequestBytes+1))
	if err != nil {
		http.Error(w, "read request", http.StatusBadRequest)
		return
	}
	if len(body) > mcpMaxRequestBytes {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []jsonRPCMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			writeJSON(w, jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &jsonRPCError{Code: -32700, Message: "parse error"}})
			return
		}
		responses := make([]jsonRPCResponse, 0, len(batch))
		for _, message := range batch {
			if response, ok := b.dispatch(r.Context(), message); ok {
				responses = append(responses, response)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, responses)
		return
	}
	var message jsonRPCMessage
	if err := json.Unmarshal(body, &message); err != nil {
		writeJSON(w, jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &jsonRPCError{Code: -32700, Message: "parse error"}})
		return
	}
	if message.Method == "tools/call" && len(message.ID) > 0 && string(message.ID) != "null" && acceptsEventStream(r) {
		if flusher, ok := w.(http.Flusher); ok {
			b.streamToolCall(w, flusher, r, message)
			return
		}
	}
	response, ok := b.dispatch(r.Context(), message)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, response)
}

// acceptsEventStream reports whether the client takes an event-stream answer
// to its POST (a Streamable HTTP MCP client sends Accept: application/json,
// text/event-stream).
func acceptsEventStream(r *http.Request) bool {
	for _, value := range r.Header.Values("Accept") {
		for _, part := range strings.Split(value, ",") {
			if media, _, _ := strings.Cut(strings.TrimSpace(part), ";"); strings.EqualFold(strings.TrimSpace(media), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

// streamToolCall answers one tools/call as an event stream: the response
// starts at once, carries a keep-alive while the tool runs, and ends with
// the JSON-RPC response as its last event. A client that drops the request
// still cancels the tool.
//
// Why (finding from inspecting the published Claude Code CLI 2.1.303; not
// yet confirmed by a live claudecode turn): the CLI's wrapFetchWithTimeout
// aborts each POST to an HTTP MCP server with "The operation timed out."
// after the server's timeout, else MCP_TOOL_TIMEOUT, else 60000 ms, and
// clears that timer once the response headers arrive, so a response that
// starts at once is not cut off. Its idle watchdog (300 s for HTTP servers)
// is reset by the progress notifications sent here only when the CLI passes
// a progressToken, which was not confirmed; mcpToolTimeoutEnv raises that
// watchdog to Swarm's own limit for that reason.
func (b *mcpBridge) streamToolCall(w http.ResponseWriter, flusher http.Flusher, r *http.Request, message jsonRPCMessage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	var mu sync.Mutex
	write := func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		if _, err := io.WriteString(w, chunk); err == nil {
			flusher.Flush()
		}
	}
	var params struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(message.Params, &params)
	done := make(chan struct{})
	var keepAlive sync.WaitGroup
	keepAlive.Add(1)
	go func() {
		defer keepAlive.Done()
		ticker := time.NewTicker(mcpStreamKeepAlive)
		defer ticker.Stop()
		for progress := 1; ; progress++ {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
			if token := bytes.TrimSpace(params.Meta.ProgressToken); len(token) > 0 && string(token) != "null" {
				notification, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress",
					"params": map[string]any{"progressToken": json.RawMessage(token), "progress": progress, "message": "the Swarm tool call is still running"}})
				if err == nil {
					write("event: message\ndata: " + string(notification) + "\n\n")
					continue
				}
			}
			write(": the Swarm tool call is still running\n\n")
		}
	}()
	response, _ := b.dispatch(r.Context(), message)
	close(done)
	keepAlive.Wait()
	payload, err := json.Marshal(response)
	if err != nil {
		payload, _ = json.Marshal(jsonRPCResponse{JSONRPC: "2.0", ID: message.ID, Error: &jsonRPCError{Code: -32603, Message: "encode tool result"}})
	}
	write("event: message\ndata: " + string(payload) + "\n\n")
}

func (b *mcpBridge) authorized(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(header[len(prefix):])), []byte(b.token)) == 1
}

// dispatch returns false for notifications, which receive no response.
func (b *mcpBridge) dispatch(requestCtx context.Context, message jsonRPCMessage) (jsonRPCResponse, bool) {
	if len(message.ID) == 0 || string(message.ID) == "null" {
		return jsonRPCResponse{}, false
	}
	response := jsonRPCResponse{JSONRPC: "2.0", ID: message.ID}
	switch message.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(message.Params, &params)
		version := mcpDefaultProtocol
		if _, ok := mcpSupportedProtocols[strings.TrimSpace(params.ProtocolVersion)]; ok {
			version = strings.TrimSpace(params.ProtocolVersion)
		}
		response.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": mcpServerName, "version": "1"},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": b.tools}
	case "tools/call":
		result, rpcErr := b.callTool(requestCtx, message.Params)
		if rpcErr != nil {
			response.Error = rpcErr
		} else {
			response.Result = result
		}
	default:
		response.Error = &jsonRPCError{Code: -32601, Message: "method not found"}
	}
	return response, true
}

func (b *mcpBridge) callTool(requestCtx context.Context, rawParams json.RawMessage) (map[string]any, *jsonRPCError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      map[string]any  `json:"_meta"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid tools/call params"}
	}
	original, ok := b.byName[strings.TrimSpace(params.Name)]
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", params.Name)}
	}
	arguments := strings.TrimSpace(string(params.Arguments))
	if arguments == "" || arguments == "null" {
		arguments = "{}"
	}

	// The tool runs under the turn context, and stops early if Claude Code drops
	// the request (for example when the turn is interrupted).
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	stop := context.AfterFunc(requestCtx, cancel)
	defer stop()

	callID := ""
	if params.Meta != nil {
		if value, ok := params.Meta[mcpToolUseIDMetaKey].(string); ok {
			callID = strings.TrimSpace(value)
		}
	}
	if callID == "" && b.correlation != nil {
		callID = b.correlation.Claim(ctx, mcpToolPrefix+original, arguments, mcpCorrelationWaitTime)
	}
	if callID == "" {
		callID = fmt.Sprintf("claudecode_call_%d", b.callSeq.Add(1))
	}

	provideriface.ReportAttemptActivity(b.ctx, provideriface.AttemptActivityStreamEvent)
	defer provideriface.ReportAttemptActivity(b.ctx, provideriface.AttemptActivityStreamEvent)
	// The CLI is silent while Swarm runs the tool: keep the provider-attempt
	// watchdog (an inactivity timeout of minutes) from ending the turn under
	// a long but live tool call.
	heartbeat := time.NewTicker(mcpToolHeartbeat)
	defer heartbeat.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				provideriface.ReportAttemptActivity(b.ctx, provideriface.AttemptActivityStreamEvent)
			}
		}
	}()
	result, err := b.invoker.ExecuteTool(ctx, provideriface.ToolInvocation{
		CallID:    callID,
		Name:      original,
		Arguments: arguments,
		Metadata: map[string]any{
			ProviderID: map[string]any{
				"mcp_tool_name": mcpToolPrefix + original,
				"tool_use_id":   callID,
			},
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, &jsonRPCError{Code: -32603, Message: "tool call cancelled"}
		}
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}, nil
	}
	if result.RestartTurn && b.onRestart != nil {
		b.onRestart()
	}
	text := strings.TrimSpace(result.TextForModel)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	isError := strings.TrimSpace(result.Error) != ""
	if text == "" && isError {
		text = strings.TrimSpace(result.Error)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}, nil
}

// toolUseCorrelator matches MCP tool calls to the tool_use blocks Claude Code
// reports on its output stream, so Swarm records the model's own call IDs.
type toolUseCorrelator struct {
	mu      sync.Mutex
	pending []pendingToolUse
	notify  chan struct{}
}

type pendingToolUse struct {
	id        string
	name      string
	arguments string
}

func newToolUseCorrelator() *toolUseCorrelator {
	return &toolUseCorrelator{notify: make(chan struct{})}
}

func (c *toolUseCorrelator) Observe(id, name string, input json.RawMessage) {
	if c == nil || strings.TrimSpace(id) == "" {
		return
	}
	c.mu.Lock()
	for _, existing := range c.pending {
		if existing.id == id {
			c.mu.Unlock()
			return
		}
	}
	c.pending = append(c.pending, pendingToolUse{id: strings.TrimSpace(id), name: strings.TrimSpace(name), arguments: canonicalJSON(input)})
	close(c.notify)
	c.notify = make(chan struct{})
	c.mu.Unlock()
}

func (c *toolUseCorrelator) Claim(ctx context.Context, name, arguments string, wait time.Duration) string {
	if c == nil {
		return ""
	}
	want := canonicalJSON(json.RawMessage(arguments))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		c.mu.Lock()
		for i, candidate := range c.pending {
			if candidate.name == name && candidate.arguments == want {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				c.mu.Unlock()
				return candidate.id
			}
		}
		notify := c.notify
		c.mu.Unlock()
		select {
		case <-notify:
		case <-timer.C:
			return ""
		case <-ctx.Done():
			return ""
		}
	}
}

func canonicalJSON(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate claude code tool bridge token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func cloneJSONMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return value
	}
}
