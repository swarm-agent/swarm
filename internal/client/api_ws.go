package client

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsOpcodeText  = 0x1
	wsOpcodeClose = 0x8
	wsOpcodePing  = 0x9
	wsOpcodePong  = 0xA

	wsMaxFrameLength  = 1 << 20
	wsReadPollTimeout = 500 * time.Millisecond
)

type StreamEventEnvelope struct {
	GlobalSeq     uint64             `json:"global_seq"`
	Stream        string             `json:"stream"`
	EventType     string             `json:"event_type"`
	EntityID      string             `json:"entity_id"`
	Payload       json.RawMessage    `json:"payload"`
	TsUnixMs      int64              `json:"ts_unix_ms"`
	Pairing       SwarmPairingState  `json:"pairing"`
	TrustedPeers  []SwarmTrustedPeer `json:"trusted_peers"`
	CausationID   string             `json:"causation_id,omitempty"`
	CorrelationID string             `json:"correlation_id,omitempty"`
}

type wsOutboundMessage struct {
	Type          string               `json:"type"`
	OK            bool                 `json:"ok,omitempty"`
	Message       string               `json:"message,omitempty"`
	Channel       string               `json:"channel,omitempty"`
	ClientID      string               `json:"client_id,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	SentAtUnixMS  int64                `json:"sent_at_unix_ms,omitempty"`
	Event         *StreamEventEnvelope `json:"event,omitempty"`
	Dropped       bool                 `json:"dropped,omitempty"`
}

type wsClientConn struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	writeMu sync.Mutex
}

func (c *API) StreamEvents(ctx context.Context, lastSeen uint64, channels []string, onEvent func(StreamEventEnvelope)) error {
	if c == nil {
		return errors.New("api client is not configured")
	}
	normalized := make([]string, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		channel = strings.TrimSpace(channel)
		if channel == "" {
			continue
		}
		if _, ok := seen[channel]; ok {
			continue
		}
		seen[channel] = struct{}{}
		normalized = append(normalized, channel)
	}
	if len(normalized) == 0 {
		return errors.New("at least one websocket channel is required")
	}
	baseURL, _, socketPath := c.requestTarget()
	conn, err := dialDaemonWS(ctx, baseURL, c.Token(), socketPath, "/ws", "")
	if err != nil {
		return err
	}
	defer conn.Close()

	for _, channel := range normalized {
		subscribe := map[string]any{
			"type":    "subscribe",
			"channel": channel,
		}
		if lastSeen > 0 {
			subscribe["last_seen_seq"] = lastSeen
		}
		rawSubscribe, err := json.Marshal(subscribe)
		if err != nil {
			return err
		}
		if err := conn.WriteText(rawSubscribe); err != nil {
			return err
		}
	}

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil
			}
		}
		raw, err := conn.ReadText(ctx)
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return nil
			}
			return err
		}
		var message wsOutboundMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(message.Type)) != "event" || message.Event == nil {
			continue
		}
		if onEvent != nil {
			onEvent(*message.Event)
		}
	}
}

func (c *API) StreamSessionEvents(ctx context.Context, lastSeen uint64, onEvent func(StreamEventEnvelope)) error {
	return c.StreamEvents(ctx, lastSeen, []string{"session:*"}, onEvent)
}

func dialDaemonWS(ctx context.Context, baseURL, token, socketPath, wsPath, clientID string) (*wsClientConn, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("base url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}

	wsScheme := "ws"
	defaultPort := "80"
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "https":
		wsScheme = "wss"
		defaultPort = "443"
	case "http", "":
		wsScheme = "ws"
	}

	hostName := strings.TrimSpace(parsed.Hostname())
	if hostName == "" {
		return nil, errors.New("daemon host is required")
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		port = defaultPort
	}
	host := net.JoinHostPort(hostName, port)

	wsURL := &url.URL{
		Scheme: wsScheme,
		Host:   host,
		Path:   normalizeDaemonWSPath(wsPath),
	}
	if clientID = strings.TrimSpace(clientID); clientID != "" {
		q := wsURL.Query()
		q.Set("client_id", clientID)
		wsURL.RawQuery = q.Encode()
	}

	netConn, err := dialWSConn(ctx, wsURL, socketPath)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(netConn)
	writer := bufio.NewWriter(netConn)

	key, err := randomWSKey()
	if err != nil {
		_ = netConn.Close()
		return nil, err
	}
	hostHeader := wsURL.Host
	requestPath := wsURL.RequestURI()

	var reqBuilder strings.Builder
	reqBuilder.WriteString("GET " + requestPath + " HTTP/1.1\r\n")
	reqBuilder.WriteString("Host: " + hostHeader + "\r\n")
	reqBuilder.WriteString("Upgrade: websocket\r\n")
	reqBuilder.WriteString("Connection: Upgrade\r\n")
	reqBuilder.WriteString("Sec-WebSocket-Version: 13\r\n")
	reqBuilder.WriteString("Sec-WebSocket-Key: " + key + "\r\n")
	if authToken := strings.TrimSpace(token); authToken != "" {
		reqBuilder.WriteString("X-Swarm-Token: " + authToken + "\r\n")
	}
	reqBuilder.WriteString("\r\n")

	if _, err := writer.WriteString(reqBuilder.String()); err != nil {
		_ = netConn.Close()
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		_ = netConn.Close()
		return nil, err
	}

	req := &http.Request{Method: http.MethodGet, URL: wsURL, Host: hostHeader}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = netConn.Close()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		_ = netConn.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	accept := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept"))
	if accept != wsAcceptForKey(key) {
		_ = netConn.Close()
		return nil, errors.New("invalid websocket accept key")
	}
	_ = netConn.SetDeadline(time.Time{})

	return &wsClientConn{
		conn:   netConn,
		reader: reader,
		writer: writer,
	}, nil
}

func dialWSConn(ctx context.Context, wsURL *url.URL, socketPath string) (net.Conn, error) {
	host := strings.TrimSpace(wsURL.Host)
	if host == "" {
		return nil, errors.New("websocket host is required")
	}
	dialer := &net.Dialer{}
	if strings.TrimSpace(socketPath) != "" {
		return dialer.DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	}
	switch strings.ToLower(strings.TrimSpace(wsURL.Scheme)) {
	case "wss":
		serverName := host
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			serverName = parsedHost
		}
		tlsDialer := tls.Dialer{
			NetDialer: dialer,
			Config: &tls.Config{
				ServerName: serverName,
			},
		}
		return tlsDialer.DialContext(ctx, "tcp", host)
	default:
		return dialer.DialContext(ctx, "tcp", host)
	}
}

func randomWSKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func wsAcceptForKey(key string) string {
	hash := sha1.New()
	_, _ = hash.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func normalizeDaemonWSPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/ws"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func (c *wsClientConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.WriteClose()
	return c.conn.Close()
}

func (c *wsClientConn) ReadText(ctx context.Context) ([]byte, error) {
	for {
		if c == nil || c.conn == nil {
			return nil, io.EOF
		}
		deadline := time.Now().Add(wsReadPollTimeout)
		if ctx != nil {
			if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
		}
		_ = c.conn.SetReadDeadline(deadline)
		opcode, payload, err := c.readFrame()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx != nil && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			return nil, err
		}
		switch opcode {
		case wsOpcodeText:
			return payload, nil
		case wsOpcodeClose:
			return nil, io.EOF
		case wsOpcodePing:
			if err := c.writeFrame(wsOpcodePong, payload); err != nil {
				return nil, err
			}
		case wsOpcodePong:
			continue
		default:
			continue
		}
	}
}

func (c *wsClientConn) WriteText(payload []byte) error {
	return c.writeFrame(wsOpcodeText, payload)
}

func (c *wsClientConn) WriteClose() error {
	return c.writeFrame(wsOpcodeClose, nil)
}

func (c *wsClientConn) readFrame() (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, head); err != nil {
		return 0, nil, err
	}
	fin := head[0]&0x80 != 0
	if !fin {
		return 0, nil, errors.New("fragmented websocket frames are unsupported")
	}

	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	if masked {
		return 0, nil, errors.New("unexpected masked websocket server frame")
	}

	payloadLength := uint64(head[1] & 0x7F)
	switch payloadLength {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.reader, ext); err != nil {
			return 0, nil, err
		}
		payloadLength = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.reader, ext); err != nil {
			return 0, nil, err
		}
		payloadLength = binary.BigEndian.Uint64(ext)
	}
	if payloadLength > wsMaxFrameLength {
		return 0, nil, fmt.Errorf("websocket frame too large: %d", payloadLength)
	}

	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func (c *wsClientConn) writeFrame(opcode byte, payload []byte) error {
	if c == nil || c.conn == nil {
		return io.EOF
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	head := []byte{0x80 | opcode}
	payloadLength := len(payload)
	switch {
	case payloadLength <= 125:
		head = append(head, byte(payloadLength)|0x80)
	case payloadLength <= 65535:
		head = append(head, 126|0x80, byte(payloadLength>>8), byte(payloadLength))
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(payloadLength))
		head = append(head, 127|0x80)
		head = append(head, ext...)
	}

	maskKey := make([]byte, 4)
	if _, err := rand.Read(maskKey); err != nil {
		return err
	}
	maskedPayload := append([]byte(nil), payload...)
	for i := range maskedPayload {
		maskedPayload[i] ^= maskKey[i%4]
	}

	if _, err := c.writer.Write(head); err != nil {
		return err
	}
	if _, err := c.writer.Write(maskKey); err != nil {
		return err
	}
	if len(maskedPayload) > 0 {
		if _, err := c.writer.Write(maskedPayload); err != nil {
			return err
		}
	}
	return c.writer.Flush()
}
