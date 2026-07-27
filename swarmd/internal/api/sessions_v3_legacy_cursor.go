package api

import (
	"fmt"
	"strconv"
	"strings"
)

func parseV3RealtimeEndpointCursorStrict(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if !strings.HasPrefix(raw, "cursor-") {
		return 0, fmt.Errorf("malformed endpoint_cursor %q", raw)
	}
	seq, err := strconv.ParseUint(strings.TrimPrefix(raw, "cursor-"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed endpoint_cursor %q", raw)
	}
	return seq, nil
}

func pebbleV3RealtimeOutboxCursor(seq uint64) string {
	if seq == 0 {
		return ""
	}
	return fmt.Sprintf("cursor-%d", seq)
}
