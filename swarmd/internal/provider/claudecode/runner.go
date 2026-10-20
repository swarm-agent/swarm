package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	usageSource          = "claudecode_cli_usage"
	usageRawPath         = "usage"
	processWaitDelay     = 10 * time.Second
	maxStreamLineBytes   = 64 << 20
	maxErrorDetailLength = 600
)

// Runner executes one Swarm provider request as one non-interactive Claude Code
// turn. The CLI runs with every built-in tool removed, user/project settings
// ignored (--restricted), no session persistence, and only Swarm's tools
// available through a per-turn loopback MCP bridge, so tool execution and
// permission decisions stay inside Swarm.
type Runner struct {
	authStore *pebblestore.AuthStore
	cli       *CLI
}

func NewRunner(authStore *pebblestore.AuthStore, cli *CLI) *Runner {
	if cli == nil {
		cli = NewCLI()
	}
	return &Runner{authStore: authStore, cli: cli}
}

func (r *Runner) ID() string {
	return ProviderID
}

// ExecutionEpochLifecycle declares that every request carries the full epoch
// input and that no Claude Code conversation survives between requests.
func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode: provideriface.ExecutionEpochContextStatelessFullInput,
	}
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.run(ctx, req, nil)
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.run(ctx, req, onEvent)
}

type turnPlan struct {
	args     []string
	env      []string
	workDir  string
	stdin    []byte
	bridge   *mcpBridge
	restart  *restartSignal
	correlat *toolUseCorrelator
	// onRateLimits receives each rate_limit_event's rate_limit_info.
	onRateLimits func(json.RawMessage)
}

func (r *Runner) run(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.authStore == nil || r.cli == nil {
		return provideriface.Response{}, errors.New("claude code runner is not configured")
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return provideriface.Response{}, identity.ErrPrincipalRequired
	}
	if _, enabled, err := activeCLICredential(r.authStore, principal.AccountScopeID); err != nil {
		return provideriface.Response{}, err
	} else if !enabled {
		return provideriface.Response{}, errors.New("claude code is not enabled for this account yet (needs an active Claude Code CLI login credential)")
	}
	if strings.TrimSpace(req.Model) == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	path, err := r.cli.ResolvePath()
	if err != nil {
		return provideriface.Response{}, err
	}

	workDir, err := os.MkdirTemp("", "swarm-claudecode-")
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("create claude code turn directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	plan, err := r.planTurn(turnCtx, req, workDir)
	if err != nil {
		return provideriface.Response{}, err
	}
	if plan.bridge != nil {
		defer plan.bridge.Close()
	}
	if err := provideriface.CheckBillableDispatch(ctx); err != nil {
		return provideriface.Response{}, err
	}
	return executeTurn(turnCtx, path, plan, req.Model, onEvent)
}

func (r *Runner) planTurn(ctx context.Context, req provideriface.Request, workDir string) (turnPlan, error) {
	plan := turnPlan{
		workDir:      workDir,
		restart:      &restartSignal{},
		correlat:     newToolUseCorrelator(),
		onRateLimits: r.cli.RecordRateLimits,
	}
	staticInstructions, dynamicContext := provideriface.SplitStaticInstructionsAndDynamicContext(req.Instructions)

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--restricted",
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--permission-mode", "dontAsk",
		"--model", cliModel(req.Model, req.ContextWindow),
	}
	if staticInstructions != "" {
		promptPath := filepath.Join(workDir, "system-prompt.txt")
		if err := os.WriteFile(promptPath, []byte(staticInstructions), 0o600); err != nil {
			return turnPlan{}, fmt.Errorf("write claude code system prompt: %w", err)
		}
		args = append(args, "--system-prompt-file", promptPath)
	}

	toolsEnabled := len(req.Tools) > 0 && !strings.EqualFold(strings.TrimSpace(req.ToolChoice), "none")
	if toolsEnabled {
		bridge, err := newMCPBridge(ctx, req.Tools, req.ToolInvoker, plan.correlat, plan.restart.Trigger)
		if err != nil {
			return turnPlan{}, err
		}
		plan.bridge = bridge
		config, err := bridge.Config()
		if err != nil {
			bridge.Close()
			return turnPlan{}, err
		}
		configPath := filepath.Join(workDir, "mcp.json")
		if err := os.WriteFile(configPath, config, 0o600); err != nil {
			bridge.Close()
			return turnPlan{}, fmt.Errorf("write claude code tool bridge config: %w", err)
		}
		// Swarm's own permission gate runs inside the ToolInvoker; Claude Code
		// pre-approves only the bridge and denies everything else (dontAsk).
		args = append(args, "--mcp-config", configPath, "--allowedTools", "mcp__"+mcpServerName)
	}

	var extraEnv []string
	if toolsEnabled {
		// Without them the CLI ends a long Swarm tool call (bash runs up to
		// provideriface.MaxToolCallDuration) on its own, shorter timeouts.
		extraEnv = append(extraEnv, mcpToolTimeoutEnv()...)
	}
	effort, disableThinking := effortForThinking(req.Thinking)
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if disableThinking {
		extraEnv = append(extraEnv, "MAX_THINKING_TOKENS=0")
	}
	plan.args = args
	plan.env = r.cli.Env(extraEnv...)

	message, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": buildTranscriptPrompt(req.Input, dynamicContext)}},
		},
	})
	if err != nil {
		return turnPlan{}, fmt.Errorf("encode claude code turn input: %w", err)
	}
	plan.stdin = append(message, '\n')
	return plan, nil
}

// claudeCodeDefaultContextWindow is the window Claude Code gives a model that
// is not asked for its long-context variant.
const claudeCodeDefaultContextWindow = 200000

// cliModel names the model for --model. Some catalog models (Opus 4.6, Sonnet
// 4.6) run with a 200k window in Claude Code unless the 1M variant is named
// with the "[1m]" suffix, while Swarm plans compaction against the catalog
// window; asking for the suffix whenever that window is larger keeps the two
// equal. Models that are 1M by default accept the suffix unchanged.
func cliModel(model string, contextWindow int) string {
	model = strings.TrimSpace(model)
	if model == "" || contextWindow <= claudeCodeDefaultContextWindow || strings.Contains(model, "[") {
		return model
	}
	return model + "[1m]"
}

// catalogModelName drops a Claude Code variant suffix such as "[1m]" from a
// model name the CLI reports, so usage records name the catalog model.
func catalogModelName(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.Index(model, "["); i > 0 {
		model = model[:i]
	}
	return model
}

// effortForThinking maps Swarm thinking levels onto Claude Code --effort.
// "off" disables extended thinking instead of choosing an effort level.
func effortForThinking(thinking string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(thinking)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(thinking)), false
	case "ultra":
		return "max", false
	case "off":
		return "", true
	default:
		return "", false
	}
}

type restartSignal struct {
	mu        sync.Mutex
	requested bool
	interrupt func()
}

func (s *restartSignal) Trigger() {
	s.mu.Lock()
	s.requested = true
	interrupt := s.interrupt
	s.mu.Unlock()
	if interrupt != nil {
		interrupt()
	}
}

func (s *restartSignal) Requested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested
}

func (s *restartSignal) setInterrupt(interrupt func()) {
	s.mu.Lock()
	s.interrupt = interrupt
	requested := s.requested
	s.mu.Unlock()
	if requested && interrupt != nil {
		interrupt()
	}
}

func executeTurn(ctx context.Context, path string, plan turnPlan, model string, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	cmd := exec.CommandContext(ctx, path, plan.args...)
	cmd.Dir = plan.workDir
	cmd.Env = plan.env
	// SIGINT ends the turn cleanly; WaitDelay escalates to SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = processWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return provideriface.Response{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return provideriface.Response{}, err
	}
	stderr := &limitedBuffer{limit: maxCLIOutputBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return provideriface.Response{}, fmt.Errorf("start claude code: %w", err)
	}
	provideriface.ReportAttemptActivity(ctx, provideriface.AttemptActivityRequestSent)

	var interruptOnce sync.Once
	plan.restart.setInterrupt(func() {
		interruptOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
			}
		})
	})

	// The whole epoch input is one user message; closing stdin lets the CLI exit
	// as soon as that turn finishes. Write concurrently so a large transcript can
	// never deadlock against unread stdout.
	writeDone := make(chan error, 1)
	go func() {
		_, err := stdin.Write(plan.stdin)
		writeDone <- firstError(err, stdin.Close())
	}()

	state := newTurnState(model, plan.correlat)
	state.onRateLimits = plan.onRateLimits
	readErr := readStream(ctx, stdout, state, onEvent)
	waitErr := cmd.Wait()
	writeErr := <-writeDone

	if err := ctx.Err(); err != nil {
		return provideriface.Response{}, err
	}
	if plan.restart.Requested() {
		response := state.response()
		response.RestartTurn = true
		if strings.TrimSpace(response.StopReason) == "" {
			response.StopReason = "abort"
		}
		return response, nil
	}
	if readErr != nil {
		return provideriface.Response{}, fmt.Errorf("read claude code output: %w", readErr)
	}
	if state.result == nil {
		if writeErr != nil {
			return provideriface.Response{}, fmt.Errorf("send turn to claude code: %w%s", writeErr, errorDetail(stderr.String()))
		}
		return provideriface.Response{}, fmt.Errorf("claude code exited without a result (%v)%s", waitErr, errorDetail(stderr.String()))
	}
	if failure := state.resultFailure(); failure != "" {
		return provideriface.Response{}, errors.New(failure)
	}
	return state.response(), nil
}

func readStream(ctx context.Context, stdout io.Reader, state *turnState, onEvent func(provideriface.StreamEvent)) error {
	reader := bufio.NewReaderSize(stdout, 256*1024)
	for {
		line, err := readLine(reader)
		if len(line) > 0 {
			provideriface.ReportAttemptActivity(ctx, provideriface.AttemptActivityStreamEvent)
			state.handleLine(line, onEvent)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := reader.ReadLine()
		line = append(line, chunk...)
		if len(line) > maxStreamLineBytes {
			return nil, fmt.Errorf("stream line exceeds %d bytes", maxStreamLineBytes)
		}
		if err != nil {
			return line, err
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// turnState folds Claude Code stream-json output into a provider response.
type turnState struct {
	model       string
	correlation *toolUseCorrelator

	text          strings.Builder
	sawTextDelta  bool
	fallbackText  []string
	thinkingByKey map[string]string
	thinkingOrder []string
	blockTypes    map[int]string
	callIndex     int
	stopReason    string

	onRateLimits func(json.RawMessage)

	lastCallUsage map[string]any
	callUsages    []map[string]any
	result        *streamResult
}

type streamLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Model           string          `json:"model"`
	Event           json.RawMessage `json:"event"`
	Message         json.RawMessage `json:"message"`
	IsError         bool            `json:"is_error"`
	Result          string          `json:"result"`
	Usage           map[string]any  `json:"usage"`
	TotalCostUSD    float64         `json:"total_cost_usd"`
	NumTurns        int             `json:"num_turns"`
	Errors          json.RawMessage `json:"errors"`
	RateLimitInfo   json.RawMessage `json:"rate_limit_info"`
}

type streamResult struct {
	Subtype      string
	IsError      bool
	Result       string
	Usage        map[string]any
	TotalCostUSD float64
	NumTurns     int
	Errors       json.RawMessage
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Model string         `json:"model"`
		Usage map[string]any `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage map[string]any `json:"usage"`
}

type assistantMessage struct {
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}

func newTurnState(model string, correlation *toolUseCorrelator) *turnState {
	return &turnState{
		model:         strings.TrimSpace(model),
		correlation:   correlation,
		thinkingByKey: make(map[string]string, 4),
		blockTypes:    make(map[int]string, 4),
	}
}

func (s *turnState) handleLine(raw []byte, onEvent func(provideriface.StreamEvent)) {
	var line streamLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return
	}
	// Swarm removes the built-in Agent tool, but never fold a nested run's
	// output into the main response if one appears.
	if line.ParentToolUseID != nil && strings.TrimSpace(*line.ParentToolUseID) != "" {
		return
	}
	switch line.Type {
	case "system":
		if line.Subtype == "init" && strings.TrimSpace(line.Model) != "" {
			s.model = catalogModelName(line.Model)
		}
	case "stream_event":
		var event streamEvent
		if err := json.Unmarshal(line.Event, &event); err == nil {
			s.handleStreamEvent(event, onEvent)
		}
	case "assistant":
		var message assistantMessage
		if err := json.Unmarshal(line.Message, &message); err == nil {
			s.handleAssistantMessage(message)
		}
	case "rate_limit_event":
		if s.onRateLimits != nil {
			s.onRateLimits(line.RateLimitInfo)
		}
	case "result":
		s.result = &streamResult{
			Subtype:      strings.TrimSpace(line.Subtype),
			IsError:      line.IsError,
			Result:       line.Result,
			Usage:        line.Usage,
			TotalCostUSD: line.TotalCostUSD,
			NumTurns:     line.NumTurns,
			Errors:       line.Errors,
		}
	}
}

func (s *turnState) handleStreamEvent(event streamEvent, onEvent func(provideriface.StreamEvent)) {
	switch event.Type {
	case "message_start":
		s.callIndex++
		s.blockTypes = make(map[int]string, 4)
		if model := catalogModelName(event.Message.Model); model != "" {
			s.model = model
		}
		s.lastCallUsage = cloneJSONMap(event.Message.Usage)
		if s.lastCallUsage == nil {
			s.lastCallUsage = map[string]any{}
		}
		s.callUsages = append(s.callUsages, s.lastCallUsage)
	case "content_block_start":
		s.blockTypes[event.Index] = event.ContentBlock.Type
		if event.ContentBlock.Type == "text" && s.text.Len() > 0 {
			s.emitText("\n\n", onEvent)
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				s.sawTextDelta = true
				s.emitText(event.Delta.Text, onEvent)
			}
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				key := fmt.Sprintf("claudecode-thinking-%d-%d", s.callIndex, event.Index)
				if _, ok := s.thinkingByKey[key]; !ok {
					s.thinkingOrder = append(s.thinkingOrder, key)
				}
				next := s.thinkingByKey[key] + event.Delta.Thinking
				s.thinkingByKey[key] = next
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: next, DeltaMode: provideriface.StreamEventDeltaModeReplace, ReasoningKey: key})
				}
			}
		}
	case "message_delta":
		if reason := strings.TrimSpace(event.Delta.StopReason); reason != "" {
			s.stopReason = reason
		}
		if s.lastCallUsage != nil {
			for key, value := range event.Usage {
				if value != nil {
					s.lastCallUsage[key] = value
				}
			}
		}
	}
}

func (s *turnState) emitText(delta string, onEvent func(provideriface.StreamEvent)) {
	s.text.WriteString(delta)
	if onEvent != nil {
		onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta})
	}
}

func (s *turnState) handleAssistantMessage(message assistantMessage) {
	if model := catalogModelName(message.Model); model != "" {
		s.model = model
	}
	for _, block := range message.Content {
		switch block.Type {
		case "tool_use":
			s.correlation.Observe(block.ID, block.Name, block.Input)
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				s.fallbackText = append(s.fallbackText, text)
			}
		}
	}
}

func (s *turnState) resultFailure() string {
	if s.result == nil {
		return "claude code returned no result"
	}
	if !s.result.IsError && (s.result.Subtype == "" || s.result.Subtype == "success") {
		return ""
	}
	detail := strings.TrimSpace(s.result.Result)
	if detail == "" && len(s.result.Errors) > 0 {
		var messages []string
		if err := json.Unmarshal(s.result.Errors, &messages); err == nil {
			detail = strings.Join(messages, "; ")
		} else {
			detail = string(s.result.Errors)
		}
	}
	subtype := s.result.Subtype
	if subtype == "" {
		subtype = "error"
	}
	return fmt.Sprintf("claude code turn failed (%s)%s", subtype, errorDetail(detail))
}

func (s *turnState) response() provideriface.Response {
	text := s.text.String()
	if !s.sawTextDelta {
		text = strings.Join(s.fallbackText, "\n\n")
	}
	if strings.TrimSpace(text) == "" && s.result != nil {
		text = s.result.Result
	}
	thinking := make([]string, 0, len(s.thinkingOrder))
	for _, key := range s.thinkingOrder {
		if value := strings.TrimSpace(s.thinkingByKey[key]); value != "" {
			thinking = append(thinking, value)
		}
	}
	stopReason := s.stopReason
	if stopReason == "" && s.result != nil && s.resultFailure() == "" {
		stopReason = "end_turn"
	}
	return provideriface.Response{
		Model:            s.model,
		StopReason:       stopReason,
		Text:             strings.TrimSpace(text),
		ReasoningSummary: strings.Join(thinking, "\n\n"),
		Usage:            s.usage(),
	}
}

// usage reports the final model call of the turn, matching the one-call-per-step
// semantics other providers use for context occupancy. Aggregate turn usage and
// Claude Code's client-side cost estimate are kept only in the raw record; the
// estimate is not billing data.
func (s *turnState) usage() provideriface.TokenUsage {
	last := s.lastCallUsage
	if last == nil && s.result != nil {
		last = s.result.Usage
	}
	if last == nil {
		return provideriface.TokenUsage{}
	}
	input := jsonInt(last["input_tokens"])
	output := jsonInt(last["output_tokens"])
	cacheRead := jsonInt(last["cache_read_input_tokens"])
	cacheWrite := jsonInt(last["cache_creation_input_tokens"])
	raw := cloneJSONMap(last)
	if s.result != nil {
		raw["claude_code_turn_usage"] = cloneJSONMap(s.result.Usage)
		raw["claude_code_num_turns"] = s.result.NumTurns
		raw["claude_code_total_cost_usd_estimate"] = s.result.TotalCostUSD
	}
	history := make([]map[string]any, 0, len(s.callUsages))
	paths := make([]string, 0, len(s.callUsages))
	for _, call := range s.callUsages {
		history = append(history, cloneJSONMap(call))
		paths = append(paths, usageRawPath)
	}
	return provideriface.TokenUsage{
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		TotalTokens:      input + output + cacheRead + cacheWrite,
		Source:           usageSource,
		APIUsageRaw:      raw,
		APIUsageRawPath:  usageRawPath,
		APIUsageHistory:  history,
		APIUsagePaths:    paths,
	}
}

func jsonInt(value any) int64 {
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			return 0
		}
		return int64(typed)
	case int64:
		if typed < 0 {
			return 0
		}
		return typed
	case int:
		if typed < 0 {
			return 0
		}
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func errorDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	if len(detail) > maxErrorDetailLength {
		detail = detail[:maxErrorDetailLength] + "…"
	}
	return ": " + detail
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
