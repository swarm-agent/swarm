package api

import (
	"net/http/httptest"
)

// activityBlockedWriter blocks only the initial connection flush, allowing a
// deterministic slow-client test without sleeps, kernel buffer-size assumptions,
// or a simulated performance measurement.
type activityBlockedWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (w *activityBlockedWriter) Flush() {
	if !w.once {
		w.once = true
		close(w.entered)
		<-w.release
	}
	w.ResponseRecorder.Flush()
}
