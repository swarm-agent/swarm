package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

const (
	handshakeGUID  = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	opcodeText     = 0x1
	opcodeClose    = 0x8
	opcodePing     = 0x9
	opcodePong     = 0xA
	maxFrameLength = 1 << 20 // 1 MiB per frame guardrail
)

var ErrUpgradeRequired = errors.New("websocket upgrade required")

type Conn struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !isWebsocketUpgrade(r) {
		return nil, ErrUpgradeRequired
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing websocket key")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("http server does not support hijacking")
	}

	netConn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack websocket connection: %w", err)
	}

	accept := computeAccept(key)
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n" +
		"\r\n"
	if _, err := rw.WriteString(response); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("write websocket handshake response: %w", err)
	}
	if err := rw.Flush(); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("flush websocket handshake response: %w", err)
	}

	return &Conn{
		conn:   netConn,
		reader: rw.Reader,
		writer: rw.Writer,
	}, nil
}

func (c *Conn) ReadText() ([]byte, error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opcodeText:
			return payload, nil
		case opcodeClose:
			_ = c.WriteClose()
			return nil, io.EOF
		case opcodePing:
			if err := c.writeFrame(opcodePong, payload); err != nil {
				return nil, err
			}
		case opcodePong:
			continue
		default:
			continue
		}
	}
}

func (c *Conn) WriteText(payload []byte) error {
	return c.writeFrame(opcodeText, payload)
}

func (c *Conn) WriteClose() error {
	return c.writeFrame(opcodeClose, nil)
}

func (c *Conn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		closeErr = c.conn.Close()
	})
	return closeErr
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Conn) readFrame() (byte, []byte, error) {
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
	if !masked {
		return 0, nil, errors.New("client frame is not masked")
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

	if payloadLength > maxFrameLength {
		return 0, nil, fmt.Errorf("websocket frame too large: %d", payloadLength)
	}

	maskKey := make([]byte, 4)
	if _, err := io.ReadFull(c.reader, maskKey); err != nil {
		return 0, nil, err
	}

	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= maskKey[i%4]
	}
	return opcode, payload, nil
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	head := []byte{0x80 | opcode}
	payloadLength := len(payload)
	switch {
	case payloadLength <= 125:
		head = append(head, byte(payloadLength))
	case payloadLength <= 65535:
		head = append(head, 126, byte(payloadLength>>8), byte(payloadLength))
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(payloadLength))
		head = append(head, 127)
		head = append(head, ext...)
	}

	if _, err := c.writer.Write(head); err != nil {
		return err
	}
	if payloadLength > 0 {
		if _, err := c.writer.Write(payload); err != nil {
			return err
		}
	}
	return c.writer.Flush()
}

func isWebsocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	connection := strings.ToLower(r.Header.Get("Connection"))
	return strings.Contains(connection, "upgrade")
}

func computeAccept(key string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(key + handshakeGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
