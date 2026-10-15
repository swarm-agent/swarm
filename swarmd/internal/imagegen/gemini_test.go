package imagegen

import (
	"context"

	"sync"
)

type blockingGeminiImageClient struct {
	ready chan struct{}
	start chan struct{}

	mu          sync.Mutex
	active      int
	maxActive   int
	callCount   int
	outputIndex []int
}

func (c *blockingGeminiImageClient) GenerateImage(_ context.Context, req GeminiImageGenerationRequest) (GeminiImageGenerationResult, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.callCount++
	c.outputIndex = append(c.outputIndex, req.OutputIndex)
	if c.callCount == cap(c.ready) {
		close(c.ready)
	}
	c.mu.Unlock()

	<-c.start

	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return GeminiImageGenerationResult{ProviderResponse: map[string]any{"slot": req.OutputIndex}}, nil
}
