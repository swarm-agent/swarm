package pebblestore

import "sort"

type RunnerSummary struct {
	Count   int            `json:"count"`
	Items   []RunnerStatus `json:"items"`
	ByState map[string]int `json:"by_state"`
	Pending int            `json:"pending"`
	Claimed int            `json:"claimed"`
}

func (s *Store) RunnerSummary(account string) (RunnerSummary, error) {
	result := RunnerSummary{Items: []RunnerStatus{}, ByState: map[string]int{"idle": 0, "busy": 0, "unhealthy": 0}}
	runnerStateMu.Lock()
	defer runnerStateMu.Unlock()
	var st runnerState
	if _, err := s.GetJSON("runners:v1:"+runnerHash(account), &st); err != nil {
		return result, err
	}
	for _, status := range st.Statuses {
		result.Items = append(result.Items, status)
		result.ByState[status.State]++
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Name < result.Items[j].Name })
	result.Count = len(result.Items)
	for _, l := range st.Landings {
		switch l.State {
		case "pending", "lease_expired":
			result.Pending++
		case "claimed":
			result.Claimed++
		}
	}
	return result, nil
}

func (s *Store) RemoveRunnerStatus(account, name string) error {
	return s.mutateRunners(account, func(st *runnerState) error { delete(st.Statuses, name); return nil })
}
