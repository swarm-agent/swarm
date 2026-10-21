package api

import (
	"context"

	"swarm/packages/swarmd/internal/videogen"
)

// Encoder-fixture provider; deliberately not live generation evidence.
type storyReadyGenerator struct {
	data      []byte
	prompts   []string
	preflight *videogen.Service
}

func (g *storyReadyGenerator) GenerateManagedVideo(_ context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	g.prompts = append(g.prompts, req.Prompt)
	return videogen.ManagedVideoResult{Bytes: g.data, MediaType: "video/mp4", Model: req.Model, Provider: "google", AspectRatio: req.AspectRatio, Resolution: req.Resolution}, nil
}

func (g *storyReadyGenerator) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	return g.preflight.PreflightVideoOperation(ctx, req)
}
