package run

import "testing"

// Requirement: initial single-Finder stage must not permanently serialize later
// disjoint Coders. Threat: silently losing approved stage parallelism. This pure
// manifest helper is the narrowest capacity-input assertion; permission tests
// separately enforce account capacity and max_concurrency. It grants no readiness.
func TestTaskProgramReservationUsesWidestStage(t *testing.T) {
	p := &taskProgramSpec{Jobs: []taskProgramJob{{StageID: "research"}, {StageID: "code"}, {StageID: "code"}, {StageID: "design"}}}
	if got := taskProgramReservationWidth(p); got != 2 {
		t.Fatalf("width=%d; later parallel stage lost", got)
	}
	if taskProgramReservationWidth(nil) != 0 || taskProgramReservationWidth(&taskProgramSpec{}) != 0 {
		t.Fatal("empty program manufactured capacity")
	}
	if len(p.Jobs) != 4 || p.Jobs[0].StageID != "research" {
		t.Fatal("width computation changed definition")
	}
}
