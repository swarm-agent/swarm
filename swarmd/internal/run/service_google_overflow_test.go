package run

import (
	"context"
	"errors"

	"sync"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type googleOverflowTestRunner struct {
	t         *testing.T
	mu        sync.Mutex
	calls     int
	requests  []provideriface.Request
	responses []provideriface.Response
	errs      []error
}

func (r *googleOverflowTestRunner) ID() string {
	return "google-fake"
}

func (r *googleOverflowTestRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	return provideriface.MediaAdapterDeclaration{}, errors.New("not supported")
}

func (r *googleOverflowTestRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.CreateResponseStreaming(ctx, req, nil)
}

func (r *googleOverflowTestRunner) CreateResponseStreaming(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.requests = append(r.requests, req)
	idx := r.calls - 1
	var err error
	if idx < len(r.errs) && r.errs[idx] != nil {
		err = r.errs[idx]
	}
	var resp provideriface.Response
	if idx < len(r.responses) {
		resp = r.responses[idx]
		if resp.Model == "" {
			resp.Model = req.Model
		}
	} else {
		resp = provideriface.Response{Model: req.Model, Text: "default ok"}
	}
	if r.t != nil {
		r.t.Logf("call #%d model=%s inputLen=%d err=%v respText=%q", r.calls, req.Model, len(req.Input), err, resp.Text)
	}
	if err != nil {
		return provideriface.Response{}, err
	}
	return resp, nil
}
