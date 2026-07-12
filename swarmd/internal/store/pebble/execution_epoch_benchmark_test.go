package pebblestore

import (
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
			// Every acceptance performs one active-index point read. The indexed
			// path decodes it every time; the legacy path misses only on its first
			// acceptance and then uses the newly written active index.
			wantPointReads := uint64(b.N)
			wantDecodes := uint64(b.N)
			if !indexed {
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
