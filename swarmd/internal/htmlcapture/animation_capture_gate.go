package htmlcapture

import "context"

type animationCaptureGateKey struct{}

// One gate per browser job, inherited by its worker tabs and frame contexts.
// Independent jobs retain their existing bounded renderer capacity/isolation.
func withAnimationCaptureGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, animationCaptureGateKey{}, make(chan struct{}, 1))
}

func acquireAnimationCapture(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate, ok := ctx.Value(animationCaptureGateKey{}).(chan struct{})
	if !ok {
		return func() {}, nil
	} // standalone single-page capture
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
