package api

import (
	"context"

	"swarm/packages/swarmd/internal/videogen"
)

type admissionVideoRecorder struct {
	fakeTestVideoGenService
}

func (r *admissionVideoRecorder) PreflightVideoOperation(ctx context.Context, req videogen.VideoPreflightRequest) (*videogen.VideoPreflightResult, error) {
	return (&mockPreflightVideoService{}).PreflightVideoOperation(ctx, req)
}
