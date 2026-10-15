package run

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeTaskContextUsageAuthority struct {
	summary    pebblestore.SessionUsageSummary
	turn       pebblestore.SessionTurnUsageSnapshot
	lineage    pebblestore.DelegatedChildLineageRecord
	generation pebblestore.DelegatedChildGenerationRecord
}

func (f *fakeTaskContextUsageAuthority) GetUsageSummary(string) (pebblestore.SessionUsageSummary, bool, error) {
	return f.summary, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetTurnUsage(string, string) (pebblestore.SessionTurnUsageSnapshot, bool, error) {
	return f.turn, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetDelegatedChildLineage(string, string) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	return f.lineage, true, nil
}
func (f *fakeTaskContextUsageAuthority) GetDelegatedChildGeneration(string, string, int) (pebblestore.DelegatedChildGenerationRecord, bool, error) {
	return f.generation, true, nil
}
func (f *fakeTaskContextUsageAuthority) UpdateDelegatedChildRun(input pebblestore.UpdateDelegatedChildRunInput) (pebblestore.DelegatedChildLineageRecord, bool, error) {
	f.lineage.CurrentRunID = input.RunID
	f.generation.RunID = input.RunID
	return f.lineage, true, nil
}
