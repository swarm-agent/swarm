package api

import (
	"context"

	"errors"

	"sync"

	"swarm/packages/swarmd/internal/videogen"
)

type fakeTestVideoGenService struct {
	mu          sync.Mutex
	shouldFail  bool
	failOnIndex int
	callCount   int
	err         error
	lastRequest videogen.ManagedVideoRequest
	result      videogen.ManagedVideoResult
}

func (f *fakeTestVideoGenService) GenerateManagedVideo(ctx context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.lastRequest = req
	if f.shouldFail || (f.failOnIndex > 0 && f.callCount == f.failOnIndex) {
		if f.err != nil {
			return videogen.ManagedVideoResult{}, f.err
		}
		return videogen.ManagedVideoResult{}, errors.New("videogen provider upstream error: service unavailable")
	}
	return f.result, nil
}
