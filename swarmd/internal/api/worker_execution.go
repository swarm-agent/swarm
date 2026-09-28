package api

import (
	"errors"
	runruntime "swarm/packages/swarmd/internal/run"
)

var errWorkerExecutionUnavailable = errors.New("worker execution service unavailable")

// Resolve the daemon-composed authority; never replace it with HTTP-local writes.
func (s *Server) workerExecutionService() (*runruntime.WorkerExecutionService, error) {
	provider, ok := s.runner.(interface {
		WorkerExecutionService() *runruntime.WorkerExecutionService
	})
	if !ok || provider.WorkerExecutionService() == nil {
		return nil, errWorkerExecutionUnavailable
	}
	return provider.WorkerExecutionService(), nil
}
