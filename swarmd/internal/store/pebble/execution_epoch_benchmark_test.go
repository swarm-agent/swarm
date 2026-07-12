package pebblestore

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble"
)

func BenchmarkBeginExecutionEpochHistoryIndependence(b *testing.B) {
	for _, history := range []int{0, 1_000, 100_000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			store := openV3SessionEventTestStore(b)
			sessions := NewSessionStore(store)
			const sessionID = "epoch-benchmark-session"
			if err := sessions.CreateSession(SessionSnapshot{ID: sessionID, UserID: "user", AccountScopeID: "account", WorkspacePath: "/workspace", WorkspaceName: "workspace"}); err != nil {
				b.Fatalf("create session: %v", err)
			}
			seedExecutionEpochBenchmarkHistory(b, store, sessionID, history)

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
			b.ReportMetric(float64(history), "history_records")
			b.ReportMetric(float64(after.PointReads-before.PointReads)/float64(b.N), "epoch_point_reads/op")
			b.ReportMetric(float64(after.IteratorReads-before.IteratorReads)/float64(b.N), "epoch_iterators/op")
			b.ReportMetric(float64(after.BatchCommits-before.BatchCommits)/float64(b.N), "epoch_batch_commits/op")
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
			key := KeyMessage(sessionID, uint64(i+1))
			if err := batch.Set([]byte(key), []byte(`{"role":"user","content":"historical"}`), nil); err != nil {
				batch.Close()
				tb.Fatalf("seed history: %v", err)
			}
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			batch.Close()
			tb.Fatalf("commit history: %v", err)
		}
		batch.Close()
	}
}
