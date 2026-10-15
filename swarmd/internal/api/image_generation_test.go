package api

import (
	"bytes"

	"net/http"

	"sync"
	"sync/atomic"

	"time"
)

type concurrentDetectResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	mu         sync.Mutex
	status     int
	writing    atomic.Int32
	concurrent atomic.Bool
}

func (w *concurrentDetectResponseWriter) Header() http.Header {
	return w.header
}

func (w *concurrentDetectResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *concurrentDetectResponseWriter) Write(data []byte) (int, error) {
	if !w.writing.CompareAndSwap(0, 1) {
		w.concurrent.Store(true)
	}
	defer w.writing.Store(0)
	time.Sleep(time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *concurrentDetectResponseWriter) Flush() {}

func (w *concurrentDetectResponseWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *concurrentDetectResponseWriter) Body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}
