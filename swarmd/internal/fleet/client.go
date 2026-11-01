package fleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// TokenHeader carries the home's owner session on the linked Swarm, as any
// client of Swarm sends it.
const TokenHeader = "X-Swarm-Token"

// Client calls one linked Swarm as its home.
type Client struct {
	address string
	token   string
	http    *http.Client
	// headerTimeout is the default bound on an answer's headers
	// (ResponseHeaderTimeout); a call's context can lengthen or lift it.
	headerTimeout time.Duration
}

// A machine that has gone away (its address now drops packets) must fail
// fast rather than hold the caller for the kernel's SYN retries (minutes).
// The header timeout bounds only the wait for the answer's headers, so
// streamed answers and upgraded WebSockets stay open once they have begun.
// It is applied per call (see WithHeldWait and WithoutHeaderBound), not on
// the shared transport, because some calls ask the machine to hold its
// answer for longer.
const (
	DialTimeout           = 5 * time.Second
	TLSHandshakeTimeout   = 5 * time.Second
	ResponseHeaderTimeout = 30 * time.Second
)

// NewClient is a client for the Swarm at address whose key is fingerprint,
// sending token (empty before the link exists).
func NewClient(cert tls.Certificate, address, fingerprint, token string) *Client {
	dialer := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
	return &Client{
		address:       address,
		token:         token,
		headerTimeout: ResponseHeaderTimeout,
		http: &http.Client{Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSClientConfig:     ClientTLS(cert, fingerprint),
			TLSHandshakeTimeout: TLSHandshakeTimeout,
			ForceAttemptHTTP2:   false,
		}},
	}
}

type headerBoundKey struct{}

// WithHeldWait marks a call in which the machine may hold its answer for up
// to wait (a wait_seconds the caller forwards): the header bound becomes
// wait plus ResponseHeaderTimeout as a margin.
func WithHeldWait(ctx context.Context, wait time.Duration) context.Context {
	if wait <= 0 {
		return ctx
	}
	return context.WithValue(ctx, headerBoundKey{}, wait+ResponseHeaderTimeout)
}

// WithoutHeaderBound lifts the header bound for a call whose own context
// bounds it (a forwarded request, whose caller holds its own deadline and
// may ask the machine for any held wait). Connect and TLS bounds still apply.
func WithoutHeaderBound(ctx context.Context) context.Context {
	return context.WithValue(ctx, headerBoundKey{}, time.Duration(-1))
}

// headerBound is the call's bound on the answer's headers; zero or less
// means none.
func (c *Client) headerBound(ctx context.Context) time.Duration {
	if bound, ok := ctx.Value(headerBoundKey{}).(time.Duration); ok {
		return bound
	}
	return c.headerTimeout
}

// headerTimeoutError says the machine sent no answer in time.
type headerTimeoutError struct {
	bound time.Duration
}

func (e *headerTimeoutError) Error() string {
	return fmt.Sprintf("timeout awaiting response headers: the machine sent no answer within %s", e.bound)
}

// cancelOnClose ends a call's header deadline context with its body.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// Error is a Swarm's refusal.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the machine answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("the machine answered HTTP %d: %s", e.Status, e.Message)
}

// Do sends one request to path (with its query) on the Swarm and returns the
// answer as it comes; the caller closes its body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	bound := c.headerBound(ctx)
	callCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(callCtx, method, "https://"+c.address+path, body)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if c.token != "" {
		req.Header.Set(TokenHeader, c.token)
	}
	if bound <= 0 {
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	// The deadline covers only the wait for headers: it is stopped once
	// they arrive, so a streamed body stays open until the caller closes it.
	timer := time.AfterFunc(bound, cancel)
	resp, err := c.http.Do(req)
	stopped := timer.Stop()
	if err != nil {
		cancel()
		if !stopped && ctx.Err() == nil {
			return nil, &headerTimeoutError{bound: bound}
		}
		return nil, err
	}
	if !stopped {
		// The deadline fired as the headers arrived; the body is cut.
		resp.Body.Close()
		cancel()
		return nil, &headerTimeoutError{bound: bound}
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// Upgrade opens a WebSocket using the same pinned TLS transport and link
// session as Do. Redirects are never followed. The caller closes the stream.
func (c *Client) Upgrade(ctx context.Context, path string, header http.Header) (*http.Response, io.ReadWriteCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(10*time.Second, cancel)
	defer timer.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.address+path, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header = header.Clone()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", TokenHeader} {
		req.Header.Del(key)
	}
	req.Header.Set(TokenHeader, c.token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := c.http.Transport.RoundTrip(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		resp.Body.Close()
		cancel()
		return nil, nil, &Error{Status: resp.StatusCode}
	}
	stream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("the machine did not provide an upgraded connection")
	}
	return resp, &upgradedStream{ReadWriteCloser: stream, cancel: cancel}, nil
}

type upgradedStream struct {
	io.ReadWriteCloser
	cancel context.CancelFunc
}

func (s *upgradedStream) Close() error {
	s.cancel()
	return s.ReadWriteCloser.Close()
}

// JSON sends one request and decodes the JSON answer into out (when not nil).
func (c *Client) JSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	header := http.Header{"Accept": {"application/json"}}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(ctx, method, path, reader, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &Error{Status: resp.StatusCode, Message: RefusalMessage(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// RefusalMessage is the reason in a Swarm's error answer.
func RefusalMessage(data []byte) string {
	var refusal struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &refusal)
	message := refusal.Message
	if text, ok := refusal.Error.(string); ok && text != "" {
		message = text
	} else if object, ok := refusal.Error.(map[string]any); ok {
		message, _ = object["message"].(string)
	}
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	return message
}
