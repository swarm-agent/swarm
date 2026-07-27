package pebblestore

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble"
)

func BenchmarkBeginExecutionEpochHistoryIndependence(b *testing.B) {
	benchmarkBeginExecutionEpochHistoryIndependence(b, false)
}

func BenchmarkBeginExecutionEpochIndexedHistoryIndependence(b *testing.B) {
	benchmarkBeginExecutionEpochHistoryIndependence(b, true)
}

func benchmarkBeginExecutionEpochHistoryIndependence(b *testing.B, indexed bool) {
	for _, history := range []int{0, 1_000, 100_000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			store := openV3SessionEventTestStore(b)
			sessions := NewSessionStore(store)
			const sessionID = "epoch-benchmark-session"
			if err := sessions.CreateSession(SessionSnapshot{ID: sessionID, UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
				b.Fatalf("create session: %v", err)
			}
			seedExecutionEpochBenchmarkHistory(b, store, sessionID, history)
			if indexed {
				initial := NewInitialExecutionEpoch(sessionID, "user", "account", 1, 1)
				if err := store.PutJSON(KeyExecutionEpoch(initial.SessionID, initial.EpochID), initial); err != nil {
					b.Fatalf("seed initial epoch: %v", err)
				}
				if err := store.PutJSON(KeyExecutionEpochActive(initial.SessionID), initial); err != nil {
					b.Fatalf("seed active epoch: %v", err)
				}
			}

			before := SnapshotExecutionEpochTelemetry()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := sessions.BeginExecutionEpoch(BeginExecutionEpochInput{SessionID: sessionID, UserID: "user", AccountScopeID: "account", ClientRequestID: fmt.Sprintf("boundary-%d", i), PayloadHash: fmt.Sprintf("hash-%d", i), Reason: "benchmark"})
				if err != nil {
					b.Fatalf("begin epoch: %v", err)
				}
				if result.Epoch.Ordinal != uint64(i+2) {
					b.Fatalf("epoch ordinal = %d, want %d", result.Epoch.Ordinal, i+2)
				}
			}
			b.StopTimer()
			after := SnapshotExecutionEpochTelemetry()
			pointReads := after.PointReads - before.PointReads
			decodes := after.DecodeCalls - before.DecodeCalls
			iterators := after.IteratorReads - before.IteratorReads
			commits := after.BatchCommits - before.BatchCommits
			if iterators != 0 {
				b.Fatalf("epoch acceptance used %d iterators, want none", iterators)
			}
			// Every acceptance performs bounded active/id/ordinal point reads.
			// Legacy adoption performs one additional latest-index miss once; the
			// counts remain independent of transcript history size.
			wantPointReads := uint64(2 * b.N)
			wantDecodes := uint64(b.N)
			if !indexed {
				wantPointReads++
				wantDecodes--
			}
			if pointReads != wantPointReads || decodes != wantDecodes || commits != uint64(b.N) {
				b.Fatalf("non-fixed epoch operations: point_reads=%d decodes=%d commits=%d n=%d indexed=%v", pointReads, decodes, commits, b.N, indexed)
			}
			b.ReportMetric(float64(history), "history_records")
			b.ReportMetric(float64(pointReads)/float64(b.N), "epoch_point_reads/op")
			b.ReportMetric(float64(decodes)/float64(b.N), "epoch_decodes/op")
			b.ReportMetric(float64(iterators)/float64(b.N), "epoch_iterators/op")
			b.ReportMetric(float64(commits)/float64(b.N), "epoch_batch_commits/op")
		})
	}
}

func seedExecutionEpochBenchmarkHistory(tb testing.TB, store *Store, sessionID string, count int) {
	tb.Helper()
	const chunkSize = 2_000
	for start := 0; start < count; start += chunkSize {
		end := start + chunkSize
		if end > count {
			end = count
		}
		batch := store.NewBatch()
		for i := start; i < end; i++ {
			seq := uint64(i + 1)
			message := MessageSnapshot{ID: fmt.Sprintf("history-%d", seq), SessionID: sessionID, GlobalSeq: seq, Role: "user", Content: "historical", CreatedAt: int64(seq)}
			messageRaw, err := json.Marshal(message)
			if err != nil {
				batch.Close()
				tb.Fatalf("marshal history message: %v", err)
			}
			event := V3SessionEvent{ID: fmt.Sprintf("history-event-%d", seq), SessionID: sessionID, Seq: seq, EventType: "session.message.appended", Payload: messageRaw, TsUnixMs: int64(seq)}
			eventRaw, err := json.Marshal(event)
			if err != nil {
				batch.Close()
				tb.Fatalf("marshal history event: %v", err)
			}
			if err := batch.Set([]byte(KeyV3SessionMessage(sessionID, seq)), messageRaw, nil); err != nil {
				batch.Close()
				tb.Fatalf("seed history message: %v", err)
			}
			if err := batch.Set([]byte(KeyV3SessionEvent(sessionID, seq)), eventRaw, nil); err != nil {
				batch.Close()
				tb.Fatalf("seed history event: %v", err)
			}
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			batch.Close()
			tb.Fatalf("commit history: %v", err)
		}
		batch.Close()
	}
	if err := store.PutBytes(KeyV3SessionSequence(sessionID), uint64ToBytes(uint64(count))); err != nil {
		tb.Fatalf("seed root sequence: %v", err)
	}
	projection := V3SessionProjection{SessionID: sessionID, LastEventSeq: uint64(count), ProjectionHighWatermarkSeq: uint64(count), UpdatedAt: int64(count)}
	if err := store.PutJSON(KeyV3SessionProjection(sessionID), projection); err != nil {
		tb.Fatalf("seed root projection: %v", err)
	}
}

func BenchmarkListExecutionEpochMessagesNamedRange(b *testing.B) {
	for _, history := range []int{0, 1_000, 100_000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			store := openV3SessionEventTestStore(b)
			sessions := NewSessionStore(store)
			const sessionID = "epoch-range-benchmark-session"
			if err := sessions.CreateSession(SessionSnapshot{ID: sessionID, UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
				b.Fatalf("create session: %v", err)
			}
			seedExecutionEpochBenchmarkHistory(b, store, sessionID, history)
			const epochSize = 8
			first := uint64(history + 1)
			for offset := 0; offset < epochSize; offset++ {
				seq := first + uint64(offset)
				message := MessageSnapshot{ID: fmt.Sprintf("epoch-%d", seq), SessionID: sessionID, GlobalSeq: seq, Role: "user", Content: "current epoch", CreatedAt: int64(seq)}
				if err := store.PutJSON(KeyV3SessionMessage(sessionID, seq), message); err != nil {
					b.Fatalf("seed epoch message: %v", err)
				}
			}
			epoch := ExecutionEpoch{EpochID: "named-epoch", SessionID: sessionID, Ordinal: 2, Status: ExecutionEpochStatusSealed, FirstRootSeq: first, LastRootSeq: first + epochSize - 1, CreatedAt: 1, UpdatedAt: 1, SealedAt: 1}
			if err := store.PutJSON(KeyExecutionEpoch(sessionID, epoch.EpochID), epoch); err != nil {
				b.Fatalf("seed epoch: %v", err)
			}

			before := SnapshotExecutionEpochTelemetry()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, messages, err := sessions.ListExecutionEpochMessages(sessionID, epoch.EpochID, 0)
				if err != nil || len(messages) != epochSize {
					b.Fatalf("load named epoch: messages=%d err=%v", len(messages), err)
				}
			}
			b.StopTimer()
			after := SnapshotExecutionEpochTelemetry()
			b.ReportMetric(float64(history), "root_history_records")
			b.ReportMetric(float64(epochSize), "named_epoch_records")
			b.ReportMetric(float64(after.PointReads-before.PointReads)/float64(b.N), "epoch_point_reads/op")
		})
	}
}
