package api

import (
	"context"
	"errors"

	"swarm/packages/swarmd/internal/videogen"
)

type storyFailGenerator struct{ prompts []string }

func (g *storyFailGenerator) GenerateManagedVideo(_ context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	g.prompts = append(g.prompts, req.Prompt)
	if len(g.prompts) == 2 {
		return videogen.ManagedVideoResult{}, errors.New("provider failure")
	}
	return videogen.ManagedVideoResult{Bytes: []byte("fixture"), MediaType: "video/mp4"}, nil
}
