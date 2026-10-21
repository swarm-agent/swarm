package pebblestore

func cleanupSignalChanged(old, next *WorkerRunCleanup) bool {
	if next == nil || !next.FailureReported {
		return false
	}
	return old == nil || !old.FailureReported || (old.State != "done" && next.State == "done")
}
