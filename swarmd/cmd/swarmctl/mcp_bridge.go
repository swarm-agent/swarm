package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// swarmctl mcp is a stdio MCP bridge. A local AI client (Claude Code, Claude
// Desktop, Codex CLI) starts it as a subprocess and speaks MCP over stdin and
// stdout: newline-delimited JSON-RPC, the MCP stdio transport. Every message
// is forwarded unchanged to the daemon's own Swarm Control endpoint (/mcp,
// swarmd/internal/api/control_mcp.go), so the tools, levels, scopes and
// results are exactly those of an AI key; the bridge implements no tool.
//
// Trust boundary. The bridge reaches the daemon only through its private
// owner socket (<data root>/local-transport/api.sock: directory 0700, socket
// 0600, owned by the OS user that runs swarmd; the kernel enforces this).
// Anyone who can run the bridge as that user can already open the socket and
// has owner access to the whole local API, so the bridge grants nothing new.
// It refuses to start as root or when the socket belongs to another user, so
// a wrong user gets an explanation rather than a bare permission error.
//
// The AI never holds owner authority. At start the bridge asks the daemon for
// an AI key at the chosen level (full by default) and sends every MCP message
// with it, so the daemon applies the AI-key gate (nothing but /mcp), the
// Swarm Control level and the route scopes exactly as for a pasted swk_ key;
// /mcp still refuses token-less socket requests.
//
// No stored key. The key exists only in this process's memory: it is never
// printed, logged or written to disk. It lives 30 minutes and is renewed
// while the client runs; every key the bridge minted is deleted (not merely
// revoked) when the bridge exits. A bridge killed without warning leaves a
// key nobody holds, which expires on its own within its lifetime.
const (
	mcpBridgeKeyName     = "AI client on this machine (swarm mcp)"
	mcpBridgeKeyLifetime = 30 * time.Minute
	// Renew while more than a provider key link's lifetime (10 minutes)
	// remains, so a link the AI handed out survives renewal: the key page
	// re-checks the key that asked when the person submits.
	mcpBridgeRenewBefore = 15 * time.Minute
	// Matches the daemon's /mcp request limit.
	mcpBridgeMaxMessage  = 1 << 20
	mcpBridgeMaxReply    = 16 << 20
	mcpBridgeMaxInflight = 16
	mcpBridgeBaseURL     = "http://swarm-local-transport"
)

var errMCPBridgeRefused = errors.New("Swarm refused this bridge's AI key (revoked or expired); restart your AI client to reconnect")

const mcpBridgeHelp = `swarmctl mcp: connect a local AI client to this machine's Swarm over stdio.

No key to make or paste. Run as the user that runs Swarm, the bridge gets a
short-lived AI key over Swarm's private owner socket, keeps it only in memory,
renews it while the client runs and deletes it on exit. Every MCP message goes
to Swarm Control (/mcp) unchanged. stdout carries only MCP; messages go to
stderr.

Usage:
  swarm mcp [--access full|write|read]                     (native install)
  swarmctl mcp [--access full|write|read] [--socket PATH]

  --access  full (default): every Swarm Control tool, including approving
            tool calls, models, workers and provider key requests.
            write: read tools plus start sessions, send messages, stop runs.
            read: read-only tools.
  --socket  Swarm's owner socket (default: $SWARMD_LOCAL_TRANSPORT_SOCKET or
            <data root>/local-transport/api.sock).

Connect your AI client:
  Claude Code:
    claude mcp add swarm -- swarm mcp
  Claude Desktop (claude_desktop_config.json; use the full path from
  "command -v swarm" if the app cannot find swarm):
    {"mcpServers": {"swarm": {"command": "swarm", "args": ["mcp"]}}}
  Codex CLI (~/.codex/config.toml):
    [mcp_servers.swarm]
    command = "swarm"
    args = ["mcp"]
  Headless container, from its host:
    claude mcp add swarm -- docker exec -i swarm-headless swarmctl mcp
`

func cmdMCP(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runMCPBridge(ctx, args, os.Stdin, os.Stdout, os.Stderr, os.Geteuid(), os.Lstat)
}

func runMCPBridge(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, euid int, lstat func(string) (os.FileInfo, error)) error {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	access := flags.String("access", "full", "full, write or read")
	socket := flags.String("socket", "", "Swarm's owner socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, mcpBridgeHelp)
			return err
		}
		return errors.New("invalid mcp flags; use --help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected mcp arguments; use --help")
	}
	level := strings.ToLower(strings.TrimSpace(*access))
	if level != "full" && level != "write" && level != "read" {
		return errors.New("--access must be full, write or read")
	}
	path, err := resolveOwnerSocket(*socket)
	if err != nil {
		return err
	}
	if err := checkMCPOwnerSocket(path, euid, lstat); err != nil {
		return err
	}
	bridge := &mcpBridge{
		client: mcpBridgeClient(path),
		socket: path,
		access: level,
		now:    time.Now,
		out:    stdout,
		log:    stderr,
	}
	defer bridge.shutdown()
	key, err := bridge.acquire(ctx)
	if err != nil {
		return err
	}
	bridge.release(key)
	fmt.Fprintf(stderr, "swarm mcp: connected to Swarm with %s access; speaking MCP on stdio\n", level)
	return bridge.serve(ctx, stdin)
}

// checkMCPOwnerSocket admits only the non-root OS user that owns Swarm's
// owner socket: the same user the kernel already lets open it.
func checkMCPOwnerSocket(path string, euid int, lstat func(string) (os.FileInfo, error)) error {
	if euid == 0 {
		return errors.New("refusing to connect an AI client as root; run it as the user that runs Swarm")
	}
	info, err := lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("Swarm is not running here (no owner socket at %s); start it with `swarm start`, then restart your AI client", path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("this user (uid %d) may not use Swarm's owner socket at %s; run your AI client as the user that runs Swarm", euid, path)
	case err != nil:
		return fmt.Errorf("cannot inspect Swarm's owner socket at %s", path)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not Swarm's owner socket", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine the owner of %s", path)
	}
	if int(stat.Uid) != euid {
		return fmt.Errorf("this user (uid %d) is not Swarm's owner: its owner socket belongs to uid %d; run your AI client as that user", euid, stat.Uid)
	}
	return nil
}

func mcpBridgeClient(socket string) *http.Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	// No overall timeout: Swarm Control bounds its own waits (wait_seconds).
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type mcpBridgeKey struct {
	secret   string
	id       string
	renewAt  time.Time
	expires  time.Time
	inflight int
}

type mcpBridge struct {
	client *http.Client
	socket string
	access string
	now    func() time.Time
	log    io.Writer

	mu      sync.Mutex
	current *mcpBridgeKey
	retired []*mcpBridgeKey

	outMu sync.Mutex
	out   io.Writer
}

// serve reads one JSON-RPC message per line and answers each request on its
// own line. Requests run concurrently (bounded); Swarm Control is stateless
// per message, so replies may arrive in any order, as JSON-RPC allows. It
// returns nil at end of input once in-flight requests are answered, or when
// ctx is cancelled (the client stopped the bridge).
func (b *mcpBridge) serve(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	type line struct {
		data    []byte
		tooLong bool
	}
	lines := make(chan line)
	readDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(in, 64<<10)
		for {
			data, tooLong, err := readMCPLine(reader, mcpBridgeMaxMessage)
			if tooLong || len(bytes.TrimSpace(data)) > 0 {
				select {
				case lines <- line{data: data, tooLong: tooLong}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				readDone <- err
				return
			}
		}
	}()
	var wg sync.WaitGroup
	slots := make(chan struct{}, mcpBridgeMaxInflight)
	finish := func(readErr error) error {
		wg.Wait()
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("reading MCP input: %w", readErr)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return finish(nil)
		case err := <-readDone:
			return finish(err)
		case next := <-lines:
			if next.tooLong {
				b.reply(cancel, mcpBridgeError(nil, -32600, "message exceeds 1 MiB"))
				continue
			}
			message := bytes.TrimSpace(next.data)
			if !json.Valid(message) {
				b.reply(cancel, mcpBridgeError(nil, -32700, "parse error"))
				continue
			}
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return finish(nil)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				b.handle(ctx, message, cancel)
			}()
		}
	}
}

// readMCPLine returns one newline-terminated message. A line longer than
// limit is consumed and reported as tooLong without being kept.
func readMCPLine(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(bytes.TrimRight(chunk, "\r\n")) > limit {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, tooLong, err
	}
}

func (b *mcpBridge) handle(ctx context.Context, message []byte, fail context.CancelCauseFunc) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	isObject := message[0] == '{'
	if isObject {
		_ = json.Unmarshal(message, &envelope)
		if envelope.Method == nil {
			// A response to a server request; Swarm Control sends none.
			return
		}
	}
	hasID := len(envelope.ID) > 0 && string(envelope.ID) != "null"
	reply, err := b.forward(ctx, message)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if hasID || !isObject {
			b.reply(fail, mcpBridgeError(envelope.ID, -32603, err.Error()))
		} else {
			fmt.Fprintf(b.log, "swarm mcp: notification not delivered: %v\n", err)
		}
		if errors.Is(err, errMCPBridgeRefused) {
			fail(err)
		}
		return
	}
	if len(reply) > 0 {
		b.reply(fail, reply)
	}
}

// forward sends one message to /mcp with the current AI key and returns the
// JSON-RPC reply compacted to one line, or nil for an accepted notification.
func (b *mcpBridge) forward(ctx context.Context, message []byte) ([]byte, error) {
	key, err := b.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer b.release(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpBridgeBaseURL+"/mcp", bytes.NewReader(message))
	if err != nil {
		return nil, errors.New("cannot build the request to Swarm")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key.secret)
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, b.unreachable(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpBridgeMaxReply+1))
	if err != nil {
		return nil, errors.New("Swarm's reply was interrupted")
	}
	if len(body) > mcpBridgeMaxReply {
		return nil, errors.New("Swarm's reply is too large")
	}
	switch {
	case resp.StatusCode == http.StatusAccepted:
		return nil, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errMCPBridgeRefused
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("Swarm rejected the message (HTTP %d): %s", resp.StatusCode, mcpBridgeDaemonMessage(body))
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, errors.New("Swarm returned an unreadable reply")
	}
	return compact.Bytes(), nil
}

// acquire returns the key to send a message with, renewing it once less than
// mcpBridgeRenewBefore remains. Replaced keys stay valid until they expire
// (in-flight requests and provider key links still use them) and are
// deleted once expired and idle, or when the bridge exits.
func (b *mcpBridge) acquire(ctx context.Context) (*mcpBridgeKey, error) {
	b.mu.Lock()
	now := b.now()
	if b.current == nil || !now.Before(b.current.renewAt) {
		key, err := b.mint(ctx)
		switch {
		case err == nil:
			if b.current != nil {
				b.retired = append(b.retired, b.current)
			}
			b.current = key
		case b.current == nil || !now.Before(b.current.expires):
			b.mu.Unlock()
			return nil, err
		default:
			fmt.Fprintf(b.log, "swarm mcp: could not renew the AI key yet: %v\n", err)
		}
	}
	key := b.current
	key.inflight++
	var stale []*mcpBridgeKey
	kept := b.retired[:0]
	for _, old := range b.retired {
		if old.inflight == 0 && !now.Before(old.expires) {
			stale = append(stale, old)
		} else {
			kept = append(kept, old)
		}
	}
	b.retired = kept
	b.mu.Unlock()
	for _, old := range stale {
		b.purge(old)
	}
	return key, nil
}

func (b *mcpBridge) release(key *mcpBridgeKey) {
	b.mu.Lock()
	key.inflight--
	b.mu.Unlock()
}

// mint asks the daemon, over the owner socket and as its owner, for an AI
// key at the bridge's level. The key is returned to this process only.
func (b *mcpBridge) mint(ctx context.Context) (*mcpBridgeKey, error) {
	payload, err := json.Marshal(map[string]any{
		"name":               mcpBridgeKeyName,
		"ai_access":          b.access,
		"expires_in_seconds": int64(mcpBridgeKeyLifetime / time.Second),
	})
	if err != nil {
		return nil, errors.New("cannot encode the AI key request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpBridgeBaseURL+"/v3/auth/tokens", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("cannot build the AI key request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, b.unreachable(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Swarm refused to give this user an AI key (HTTP %d): %s", resp.StatusCode, mcpBridgeDaemonMessage(body))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("Swarm could not issue an AI key (HTTP %d): %s", resp.StatusCode, mcpBridgeDaemonMessage(body))
	}
	var result struct {
		Token  string `json:"token"`
		Record struct {
			ID        string `json:"id"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"record"`
	}
	if err := json.Unmarshal(body, &result); err != nil || !strings.HasPrefix(result.Token, "swk_") || strings.TrimSpace(result.Record.ID) == "" {
		return nil, errors.New("Swarm returned an invalid AI key response")
	}
	expires := b.now().Add(mcpBridgeKeyLifetime)
	if result.Record.ExpiresAt > 0 {
		expires = time.UnixMilli(result.Record.ExpiresAt)
	}
	return &mcpBridgeKey{secret: result.Token, id: result.Record.ID, renewAt: expires.Add(-mcpBridgeRenewBefore), expires: expires}, nil
}

// purge deletes a key record the bridge minted. It runs after the client
// may have gone, so it uses its own short deadline.
func (b *mcpBridge) purge(key *mcpBridgeKey) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, mcpBridgeBaseURL+"/v3/auth/tokens/"+url.PathEscape(key.id)+"?purge=true", nil)
	if err == nil {
		var resp *http.Response
		if resp, err = b.client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(b.log, "swarm mcp: could not delete an AI key this bridge created; it expires by itself at %s\n", key.expires.Format(time.RFC3339))
	}
}

// shutdown deletes every key the bridge minted.
func (b *mcpBridge) shutdown() {
	b.mu.Lock()
	keys := b.retired
	if b.current != nil {
		keys = append(keys, b.current)
	}
	b.current, b.retired = nil, nil
	b.mu.Unlock()
	for _, key := range keys {
		b.purge(key)
	}
}

func (b *mcpBridge) reply(fail context.CancelCauseFunc, line []byte) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	if _, err := b.out.Write(append(line, '\n')); err != nil {
		fail(fmt.Errorf("writing MCP output: %w", err))
	}
}

func (b *mcpBridge) unreachable(err error) error {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("Swarm is not running (nothing answers on its owner socket %s); start it with `swarm start`", b.socket)
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return fmt.Errorf("this user may not use Swarm's owner socket at %s; run your AI client as the user that runs Swarm", b.socket)
	default:
		return fmt.Errorf("cannot reach Swarm on its owner socket %s", b.socket)
	}
}

func mcpBridgeError(id json.RawMessage, code int, message string) []byte {
	var responseID any
	if len(id) > 0 {
		responseID = id
	}
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": responseID, "error": map[string]any{"code": code, "message": message}})
	return encoded
}

// mcpBridgeDaemonMessage keeps only the daemon's short error text.
func mcpBridgeDaemonMessage(body []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &decoded)
	message := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(decoded.Error))
	if message == "" {
		return "no reason given"
	}
	if runes := []rune(message); len(runes) > 300 {
		message = string(runes[:300]) + "…"
	}
	return message
}
