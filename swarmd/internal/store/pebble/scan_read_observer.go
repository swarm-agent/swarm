package pebblestore

import "sync/atomic"

// scanReadObserver, when installed by a test, sees every record a range scan
// reads (its key and value size). It lets tests assert the read budget of a
// code path, for example that a Companion turn never scans its whole
// conversation. It is nil in production.
var scanReadObserver atomic.Pointer[func(key string, valueBytes int)]

// ObserveScanReadsForTest installs fn as the scan read observer and returns a
// function restoring the previous one. fn runs on the scanning goroutine and
// must be safe for concurrent use.
func ObserveScanReadsForTest(fn func(key string, valueBytes int)) (restore func()) {
	var next *func(key string, valueBytes int)
	if fn != nil {
		next = &fn
	}
	previous := scanReadObserver.Swap(next)
	return func() { scanReadObserver.Store(previous) }
}

func observeScanRead(key string, valueBytes int) {
	if observe := scanReadObserver.Load(); observe != nil {
		(*observe)(key, valueBytes)
	}
}
