package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"swarm/packages/swarmd/internal/imagegen"
)

func (s *Server) SetImageGenerationService(service *imagegen.Service) {
	if s == nil {
		return
	}
	s.imageGen = service
}

func (s *Server) handleImageGenerationProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.imageGen == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image generation service is not configured"))
		return
	}
	caps, err := s.imageGen.Capabilities(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "providers": caps.Providers})
}

func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.imageGen == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image generation service is not configured"))
		return
	}
	var req imagegen.GenerateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stream := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") || strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("stream")), "true")
	if stream {
		s.handleImageGenerationsStream(w, r, req)
		return
	}
	result, err := s.imageGen.Generate(r.Context(), req)
	if err != nil {
		body := map[string]any{"ok": false, "error": err.Error(), "code": "400"}
		if providerResponse, ok := imagegen.ProviderResponseFromError(err); ok {
			body["provider_response"] = providerResponse
		}
		writeJSON(w, http.StatusBadRequest, body)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleImageGenerationsStream(w http.ResponseWriter, r *http.Request, req imagegen.GenerateRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("image generation streaming is not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	send := func(event string, payload any) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			encoded = []byte(`{"ok":false,"error":"encode stream event"}`)
		}
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flusher.Flush()
	}
	req.OnEvent = func(event imagegen.GenerateStreamEvent) {
		send(event.Type, map[string]any{"ok": true, "event": event})
	}
	result, err := s.imageGen.Generate(r.Context(), req)
	if err != nil {
		body := map[string]any{"ok": false, "error": err.Error(), "code": "400"}
		if providerResponse, ok := imagegen.ProviderResponseFromError(err); ok {
			body["provider_response"] = providerResponse
		}
		send("error", body)
		return
	}
	send("completed", map[string]any{"ok": true, "result": result})
}

func (s *Server) handleImageAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	if s.imageGen == nil {
		writeError(w, http.StatusInternalServerError, errors.New("image generation service is not configured"))
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	assetID := strings.TrimSpace(r.URL.Query().Get("asset_id"))
	assetPath, asset, err := s.imageGen.ResolveAssetPath(threadID, assetID)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	file, err := os.Open(assetPath)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	contentType := "image/png"
	if strings.EqualFold(asset.Extension, "jpg") || strings.EqualFold(asset.Extension, "jpeg") {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
