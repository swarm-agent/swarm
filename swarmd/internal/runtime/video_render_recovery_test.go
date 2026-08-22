package runtime

import (
	"context"
	"testing"
)

func TestStartVideoRenderRecoveryAcceptsNilService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startVideoRenderRecovery(ctx, nil)
}
