package pebblestore

// RunnerLandingWake is an accelerator only. Subscribe before attempting a
// durable claim so creation between claim and wait cannot be missed.
func (s *Store) RunnerLandingWake() <-chan struct{} {
	s.runnerWakeMu.Lock()
	defer s.runnerWakeMu.Unlock()
	if s.runnerWake == nil {
		s.runnerWake = make(chan struct{})
	}
	return s.runnerWake
}
func (s *Store) wakeRunnerLandings() {
	s.runnerWakeMu.Lock()
	defer s.runnerWakeMu.Unlock()
	if s.runnerWake != nil {
		close(s.runnerWake)
	}
	s.runnerWake = make(chan struct{})
}
