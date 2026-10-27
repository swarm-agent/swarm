package fleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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
}

// NewClient is a client for the Swarm at address whose key is fingerprint,
// sending token (empty before the link exists).
func NewClient(cert tls.Certificate, address, fingerprint, token string) *Client {
	return &Client{
		address: address,
		token:   token,
		http: &http.Client{Transport: &http.Transport{
			TLSClientConfig:   ClientTLS(cert, fingerprint),
			ForceAttemptHTTP2: false,
		}},
	}
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
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.address+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if c.token != "" {
		req.Header.Set(TokenHeader, c.token)
	}
	return c.http.Do(req)
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
