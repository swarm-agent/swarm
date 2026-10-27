package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func fleetHeaderToken(header http.Header, key, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// The normal home authentication and Desktop boundary wrap fleet routes just
// as they wrap realtime. Only the machine's handshake headers cross back;
// neither side's cookies or credentials belong to this upgraded connection.
func (s *Server) forwardFleetWebSocket(w http.ResponseWriter, r *http.Request, client *fleet.Client, machine pebblestore.FleetMachine, path string, header http.Header) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("http server does not support hijacking"))
		return
	}
	resp, upstream, err := client.Upgrade(r.Context(), path, header)
	s.noteFleetContact(machine, err)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("could not upgrade "+machine.Name+": "+err.Error()))
		return
	}
	defer upstream.Close()
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	responseHeader := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}
	for _, key := range []string{"Sec-WebSocket-Accept", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
		if values := resp.Header.Values(key); len(values) > 0 {
			responseHeader[key] = values
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return
	}
	if err := responseHeader.Write(rw); err != nil {
		return
	}
	if _, err := rw.WriteString("\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Time{})
	toMachine, toClient := make(chan struct{}), make(chan struct{})
	go func() { defer close(toMachine); _, _ = io.Copy(upstream, rw) }()
	go func() { defer close(toClient); _, _ = io.Copy(conn, upstream) }()
	select {
	case <-toMachine:
	case <-toClient:
	case <-r.Context().Done():
	}
	_ = upstream.Close()
	_ = conn.Close()
	// Both copies are released by closing both connections.
	<-toMachine
	<-toClient
}
