package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func parseAfterSeqAndLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (uint64, int, bool) {
	afterSeq := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
			return 0, 0, false
		}
		afterSeq = parsed
	}
	limit, ok := parseRequestPositiveLimit(w, r, defaultLimit)
	return afterSeq, limit, ok
}

func parseRequestPositiveLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func parseSessionsV2PositiveLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	return parseRequestPositiveLimit(w, r, defaultLimit)
}
