package api

import (
	"errors"

	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

// workerRunView adds the read-time review projection to a worker run. The
// stored run status keeps its execution meaning; review states whether the
// run's project task still awaits review/integration.
type workerRunView struct {
	pebblestore.WorkerRunRecord
	Review *runruntime.WorkerRunReview `json:"review,omitempty"`
}

func (s *Server) workerRunViews(runs []pebblestore.WorkerRunRecord) ([]workerRunView, error) {
	views := make([]workerRunView, 0, len(runs))
	execution, execErr := s.workerExecutionService()
	for _, run := range runs {
		view := workerRunView{WorkerRunRecord: run}
		if execErr == nil && run.Status == "succeeded" {
			review, err := execution.RunReview(run)
			if err != nil {
				return nil, err
			}
			view.Review = review
		}
		views = append(views, view)
	}
	return views, nil
}
