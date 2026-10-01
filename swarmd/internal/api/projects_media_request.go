package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// This is the whole JSON envelope limit, not a per-image or model limit.
// Allow multiple high-resolution images plus base64's 4/3 encoding overhead.
const defaultProjectMediaRequestBytes int64 = 256 << 20
const projectMediaRequestLimitEnv = "SWARM_PROJECT_MEDIA_MAX_REQUEST_BYTES"

func readProjectMediaRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limit := defaultProjectMediaRequestBytes
	if value := strings.TrimSpace(os.Getenv(projectMediaRequestLimitEnv)); value != "" {
		configured, err := strconv.ParseInt(value, 10, 64)
		if err != nil || configured <= 0 {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("%s must be a positive byte count", projectMediaRequestLimitEnv))
			return nil, false
		}
		limit = configured
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("media task request exceeds %d bytes; increase %s to allow larger uploads (including base64 overhead)", limit, projectMediaRequestLimitEnv))
		} else {
			writeError(w, http.StatusBadRequest, errors.New("cannot read request body"))
		}
		return nil, false
	}
	return body, true
}
