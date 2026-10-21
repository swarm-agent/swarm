package pebblestore

// SetWorkerRunObserver installs fn, called once each time a worker run is
// stored with a new final status (succeeded, failed or cancelled), after the
// write commits and outside the worker lock. It must not block for long.
func (s *Store) SetWorkerRunObserver(fn func(worker WorkerRecord, run WorkerRunRecord)) {
	if s != nil {
		s.workerRunObserver = fn
	}
}

// SetDesignJobEndObserver installs fn, called once for each design job the
// restart reconcile marks failed, after the write commits. Jobs that end while
// the daemon runs are reported by their executor.
func (s *Store) SetDesignJobEndObserver(fn func(task ProjectTaskRecord)) {
	if s != nil {
		s.designJobEndObserver = fn
	}
}
