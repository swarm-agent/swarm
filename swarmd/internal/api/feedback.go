package api

import (
	"errors"
	"log"
	"net/http"
	"time"

	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

var errPublicAPIUnavailable = errors.New("public API client is unavailable")

func (s *Server) reportActivationBestEffort(event string) {
	if s == nil || s.publicAPI == nil {
		return
	}
	go func() {
		if err := s.publicAPI.ReportActivation(s.runCtx, event); err != nil && s.runCtx.Err() == nil {
			log.Printf("warning: anonymous activation report remains pending event=%q: %v", event, err)
		}
	}()
}

type desktopFeedbackRequest struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	FormTime int64  `json:"form_time"`
}

func (s *Server) handleDesktopFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.publicAPI == nil {
		writeError(w, http.StatusServiceUnavailable, errPublicAPIUnavailable)
		return
	}
	var request desktopFeedbackRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.FormTime <= 0 {
		request.FormTime = time.Now().UnixMilli()
	}
	if err := s.publicAPI.SubmitFeedback(r.Context(), swarmruntime.FeedbackInput{
		Category: request.Category,
		Message:  request.Message,
		FormTime: request.FormTime,
	}); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Thanks — your feedback was sent."})
}
