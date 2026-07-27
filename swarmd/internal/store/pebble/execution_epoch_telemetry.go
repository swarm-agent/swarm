package pebblestore

import (
	"sync/atomic"
	"time"
)

// ExecutionEpochTelemetry is process-local aggregate evidence for the bounded
// execution-epoch path. Durations are cumulative so callers can derive rates
// without placing session identifiers or transcript content in metrics.
type ExecutionEpochTelemetry struct {
	BoundaryCalls        uint64
	BoundaryDuration     time.Duration
	QueueWaits           uint64
	QueueWaitDuration    time.Duration
	RecoveryCalls        uint64
	RecoveryDuration     time.Duration
	PointReads           uint64
	PointReadDuration    time.Duration
	DecodeCalls          uint64
	DecodeDuration       time.Duration
	IteratorReads        uint64
	IteratorRecords      uint64
	IteratorDuration     time.Duration
	RangeLoads           uint64
	RangeLoadRecords     uint64
	RangeLoadDuration    time.Duration
	EncodeCalls          uint64
	EncodeDuration       time.Duration
	BatchCommits         uint64
	BatchCommitDuration  time.Duration
	SocketDials          uint64
	SocketDialDuration   time.Duration
	SocketReuses         uint64
	SocketResets         uint64
	ProviderSends        uint64
	ProviderSendDuration time.Duration
	FirstEvents          uint64
	FirstEventDuration   time.Duration
}

var executionEpochTelemetry struct {
	boundaryCalls, boundaryNanos                  atomic.Uint64
	queueWaits, queueWaitNanos                    atomic.Uint64
	recoveryCalls, recoveryNanos                  atomic.Uint64
	pointReads, pointReadNanos                    atomic.Uint64
	decodeCalls, decodeNanos                      atomic.Uint64
	iteratorReads, iteratorRecords, iteratorNanos atomic.Uint64
	encodeCalls, encodeNanos                      atomic.Uint64
	batchCommits, batchCommitNanos                atomic.Uint64
	socketDials, socketDialNanos                  atomic.Uint64
	socketReuses, socketResets                    atomic.Uint64
	providerSends, providerSendNanos              atomic.Uint64
	firstEvents, firstEventNanos                  atomic.Uint64
}

func SnapshotExecutionEpochTelemetry() ExecutionEpochTelemetry {
	return ExecutionEpochTelemetry{
		BoundaryCalls: executionEpochTelemetry.boundaryCalls.Load(), BoundaryDuration: time.Duration(executionEpochTelemetry.boundaryNanos.Load()),
		QueueWaits: executionEpochTelemetry.queueWaits.Load(), QueueWaitDuration: time.Duration(executionEpochTelemetry.queueWaitNanos.Load()),
		RecoveryCalls: executionEpochTelemetry.recoveryCalls.Load(), RecoveryDuration: time.Duration(executionEpochTelemetry.recoveryNanos.Load()),
		PointReads: executionEpochTelemetry.pointReads.Load(), PointReadDuration: time.Duration(executionEpochTelemetry.pointReadNanos.Load()),
		DecodeCalls: executionEpochTelemetry.decodeCalls.Load(), DecodeDuration: time.Duration(executionEpochTelemetry.decodeNanos.Load()),
		IteratorReads: executionEpochTelemetry.iteratorReads.Load(), IteratorRecords: executionEpochTelemetry.iteratorRecords.Load(), IteratorDuration: time.Duration(executionEpochTelemetry.iteratorNanos.Load()),
		RangeLoads: executionEpochTelemetry.iteratorReads.Load(), RangeLoadRecords: executionEpochTelemetry.iteratorRecords.Load(), RangeLoadDuration: time.Duration(executionEpochTelemetry.iteratorNanos.Load()),
		EncodeCalls: executionEpochTelemetry.encodeCalls.Load(), EncodeDuration: time.Duration(executionEpochTelemetry.encodeNanos.Load()),
		BatchCommits: executionEpochTelemetry.batchCommits.Load(), BatchCommitDuration: time.Duration(executionEpochTelemetry.batchCommitNanos.Load()),
		SocketDials: executionEpochTelemetry.socketDials.Load(), SocketDialDuration: time.Duration(executionEpochTelemetry.socketDialNanos.Load()),
		SocketReuses: executionEpochTelemetry.socketReuses.Load(), SocketResets: executionEpochTelemetry.socketResets.Load(),
		ProviderSends: executionEpochTelemetry.providerSends.Load(), ProviderSendDuration: time.Duration(executionEpochTelemetry.providerSendNanos.Load()),
		FirstEvents: executionEpochTelemetry.firstEvents.Load(), FirstEventDuration: time.Duration(executionEpochTelemetry.firstEventNanos.Load()),
	}
}

func observeExecutionEpochBoundary(start time.Time) {
	executionEpochTelemetry.boundaryCalls.Add(1)
	executionEpochTelemetry.boundaryNanos.Add(uint64(time.Since(start)))
}

// ObserveExecutionEpochQueueWait records executor scheduling latency. It accepts
// only a timestamp so queue metrics cannot acquire session or transcript labels.
func ObserveExecutionEpochQueueWait(enqueuedAt time.Time) {
	if enqueuedAt.IsZero() {
		return
	}
	executionEpochTelemetry.queueWaits.Add(1)
	executionEpochTelemetry.queueWaitNanos.Add(uint64(time.Since(enqueuedAt)))
}

// ObserveExecutionEpochRecovery records one bounded provider-context recovery.
func ObserveExecutionEpochRecovery(start time.Time) {
	executionEpochTelemetry.recoveryCalls.Add(1)
	executionEpochTelemetry.recoveryNanos.Add(uint64(time.Since(start)))
}

func observeExecutionEpochPointRead(start time.Time) {
	executionEpochTelemetry.pointReads.Add(1)
	executionEpochTelemetry.pointReadNanos.Add(uint64(time.Since(start)))
}

func observeExecutionEpochDecode(start time.Time) {
	executionEpochTelemetry.decodeCalls.Add(1)
	executionEpochTelemetry.decodeNanos.Add(uint64(time.Since(start)))
}

// ObserveExecutionEpochIterator records one bounded current-epoch range read.
// decoded is the number of records decoded by the range operation.
func ObserveExecutionEpochIterator(start time.Time, decoded int) {
	executionEpochTelemetry.iteratorReads.Add(1)
	executionEpochTelemetry.iteratorNanos.Add(uint64(time.Since(start)))
	if decoded > 0 {
		executionEpochTelemetry.iteratorRecords.Add(uint64(decoded))
	}
}

func observeExecutionEpochEncode(start time.Time) {
	executionEpochTelemetry.encodeCalls.Add(1)
	executionEpochTelemetry.encodeNanos.Add(uint64(time.Since(start)))
}

func observeExecutionEpochBatchCommit(start time.Time) {
	executionEpochTelemetry.batchCommits.Add(1)
	executionEpochTelemetry.batchCommitNanos.Add(uint64(time.Since(start)))
}

// ObserveExecutionEpochSocketDial records a provider transport dial without
// attaching endpoint, credential, session, or epoch labels.
func ObserveExecutionEpochSocketDial(start time.Time) {
	executionEpochTelemetry.socketDials.Add(1)
	executionEpochTelemetry.socketDialNanos.Add(uint64(time.Since(start)))
}

// ObserveExecutionEpochSocketReuse records reuse of a compatible healthy socket.
func ObserveExecutionEpochSocketReuse() {
	executionEpochTelemetry.socketReuses.Add(1)
}

// ObserveExecutionEpochSocketReset records clearing or closing cached socket state.
func ObserveExecutionEpochSocketReset() {
	executionEpochTelemetry.socketResets.Add(1)
}

// ObserveExecutionEpochProviderSend records the provider loop latency after a
// bounded execution-epoch context has been constructed.
func ObserveExecutionEpochProviderSend(start time.Time) {
	executionEpochTelemetry.providerSends.Add(1)
	executionEpochTelemetry.providerSendNanos.Add(uint64(time.Since(start)))
}

// ObserveExecutionEpochFirstEvent records time to the first provider stream event.
func ObserveExecutionEpochFirstEvent(start time.Time) {
	executionEpochTelemetry.firstEvents.Add(1)
	executionEpochTelemetry.firstEventNanos.Add(uint64(time.Since(start)))
}
