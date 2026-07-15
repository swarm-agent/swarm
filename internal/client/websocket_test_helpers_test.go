package client

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
)

func hijackLifecycleTestWebsocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	accept := wsAcceptForKey(r.Header.Get("Sec-WebSocket-Key"))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

func readClientLifecycleTestFrame(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	payloadLength := int(head[1] & 0x7F)
	if payloadLength == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		payloadLength = int(ext[0])<<8 | int(ext[1])
	} else if payloadLength == 127 {
		return 0, nil, http.ErrNotSupported
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeServerLifecycleTestFrame(t *testing.T, conn io.Writer, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal server frame: %v", err)
	}
	header := []byte{0x80 | wsOpcodeText}
	if len(raw) <= 125 {
		header = append(header, byte(len(raw)))
	} else if len(raw) <= 65535 {
		header = append(header, 126, byte(len(raw)>>8), byte(len(raw)))
	} else {
		t.Fatalf("test frame too large")
	}
	if _, err := conn.Write(append(header, raw...)); err != nil {
		t.Fatalf("write server frame: %v", err)
	}
}
