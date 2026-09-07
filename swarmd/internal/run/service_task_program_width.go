package run

// A stage's declaration is a conservative admitted ceiling, not proof that all
// its jobs are ready. Scheduler dependency checks still choose each actual cohort.
func taskProgramReservationWidth(program *taskProgramSpec) int {
	if program == nil {
		return 0
	}
	widths := make(map[string]int)
	maximum := 0
	for _, job := range program.Jobs {
		widths[job.StageID]++
		if widths[job.StageID] > maximum {
			maximum = widths[job.StageID]
		}
	}
	return maximum
}
