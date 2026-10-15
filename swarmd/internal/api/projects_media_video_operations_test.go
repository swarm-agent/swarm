package api

import (
	"context"

	"errors"
	"fmt"

	"sync"

	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/videogen"
)

// mockPreflightVideoService implements managedVideoService and videoPreflightService for testing.
type mockPreflightVideoService struct {
	mu           sync.Mutex
	lastReq      videogen.ManagedVideoRequest
	lastPfReq    videogen.VideoPreflightRequest
	failGen      bool
	failGenErr   error
	genResult    videogen.ManagedVideoResult
	preflightErr error
	uiSettings   *uisettings.Service
}

func (m *mockPreflightVideoService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastReq = req
	if m.failGen {
		if m.failGenErr != nil {
			return videogen.ManagedVideoResult{}, m.failGenErr
		}
		return videogen.ManagedVideoResult{}, fmt.Errorf("mock upstream generation error")
	}
	res := m.genResult
	if len(res.Bytes) == 0 {
		res.Bytes = []byte("fake-video-mp4-payload")
		res.MediaType = "video/mp4"
	}
	if res.Model == "" {
		res.Model = req.Model
	}
	if res.Provider == "" {
		res.Provider = "google"
	}
	if res.Provenance == nil {
		res.Provenance = &pebblestore.VideoProvenance{
			AccountScopeID: req.Principal.AccountScopeID,
			Provider:       res.Provider,
			Model:          res.Model,
			Operation:      req.Operation,
			CreatedAt:      time.Now().UnixMilli(),
		}
	}
	return res, nil
}

func (m *mockPreflightVideoService) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPfReq = req
	if m.preflightErr != nil {
		return nil, m.preflightErr
	}

	// Check UI settings iteration model if configured
	if m.uiSettings != nil && req.Principal.AccountScopeID != "" {
		ui, err := m.uiSettings.GetForAccount(req.Principal.AccountScopeID)
		if err == nil {
			if req.Operation == pebblestore.VideoOperationEdit && req.ExplicitModel == "" && ui.Tools.Video.IterationModel == "" {
				return nil, errors.New("no default video iteration model configured for account; select a model or configure one in Settings")
			}
		}
	}

	model := req.ExplicitModel
	if model == "" {
		if req.Operation == pebblestore.VideoOperationEdit {
			model = "gemini-omni-video"
		} else if req.Operation == pebblestore.VideoOperationExtend {
			isOmni := (req.Source != nil && req.Source.InteractionID != "") || (req.SourceProvenance != nil && videogen.IsOmniModel(req.SourceProvenance.Model))
			if isOmni {
				model = "gemini-omni-video"
			} else {
				model = "veo-3.1-generate-preview"
			}
		} else {
			model = "veo-3.1-generate-preview"
		}
	}

	if req.Operation == pebblestore.VideoOperationEdit && videogen.IsVeoModel(model) {
		return nil, errors.New("Veo models do not support video editing; use Gemini Omni for video editing or extend for Veo continuation")
	}

	dur := req.DurationSeconds
	if videogen.IsOmniModel(model) {
		dur = 0
	} else if dur <= 0 {
		dur = 8
	}
	ar := req.AspectRatio
	if ar == "" {
		ar = "16:9"
	}
	res := req.Resolution
	if res == "" {
		res = "720p"
	}

	return &videogen.VideoPreflightResult{
		Operation:        req.Operation,
		ResolvedModel:    model,
		ResolvedProvider: "google",
		DurationSeconds:  dur,
		AspectRatio:      ar,
		Resolution:       res,
	}, nil
}
