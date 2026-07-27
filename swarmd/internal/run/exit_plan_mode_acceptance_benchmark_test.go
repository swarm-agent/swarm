package run

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// BenchmarkExitPlanModeAcceptanceHistorySensitivity verifies the canonical
// acceptance boundary stays independent of transcript size. Its operation
// counters distinguish transcript scans, token-posting churn, payload bytes,
// and sync commits at 0, 1k, and large transcript sizes.
func BenchmarkExitPlanModeAcceptanceHistorySensitivity(b *testing.B) {
	for _, history := range []int{0, 1_000, 100_000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			runSvc, sessionSvc, store, sessionStore := newExitPlanModeAcceptanceBenchmarkService(b)
			const sessionID = "exit-plan-acceptance-benchmark"
			created, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
				SessionID: sessionID, Title: "Exit plan acceptance benchmark",
				WorkspacePath: b.TempDir(), WorkspaceName: "workspace",
				UserID: "user", AccountScopeID: "account",
				Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "benchmark-model", Thinking: "medium"},
			})
			if err != nil {
				b.Fatalf("create session: %v", err)
			}
			if _, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-acceptance", "Acceptance", "# Acceptance", "draft", "draft", true, sessionruntime.PlanSaveMetadata{Document: benchmarkExitPlanModeDocument()}); err != nil {
				b.Fatalf("seed plan: %v", err)
			}
			seedExitPlanModeAcceptanceHistory(b, store, sessionID, history)
			created.MessageCount = history
			created.LastMessageAt = int64(history)
			created.UpdatedAt = int64(history + 1)
			if err := sessionStore.UpdateSession(created); err != nil {
				b.Fatalf("index seeded history: %v", err)
			}

			args, err := json.Marshal(map[string]any{
				"plan_id": "plan-acceptance", "title": "Acceptance", "plan": "# Acceptance",
				"continue_automatically": true, "document": benchmarkExitPlanModeDocument(),
			})
			if err != nil {
				b.Fatal(err)
			}
			applyMutation := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
				return sessionSvc.ApplySessionMutation(input)
			}

			var last pebblestore.V3PlanAcceptanceTelemetry
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if _, _, err := sessionSvc.SetMode(sessionID, sessionruntime.ModePlan); err != nil {
					b.Fatalf("reset plan mode: %v", err)
				}
				before := pebblestore.SnapshotV3PlanAcceptanceTelemetry()
				b.StartTimer()
				if _, err := runSvc.executeExitPlanModeTool(sessionID, sessionruntime.ModePlan, pebblestore.AgentProfile{Name: "swarm"}, string(args), "", applyMutation); err != nil {
					b.Fatalf("accept exit_plan_mode: %v", err)
				}
				b.StopTimer()
				last = pebblestore.DeltaV3PlanAcceptanceTelemetry(pebblestore.SnapshotV3PlanAcceptanceTelemetry(), before)
			}
			b.ReportMetric(float64(history), "history_messages")
			b.ReportMetric(float64(last.MessageRowsScanned), "message_rows_scanned/op")
			b.ReportMetric(float64(last.SearchPostingsRead), "search_postings_read/op")
			b.ReportMetric(float64(last.SearchPostingsDeleted), "search_postings_deleted/op")
			b.ReportMetric(float64(last.SearchPostingsSet), "search_postings_set/op")
			b.ReportMetric(float64(last.SearchPostingLogicalBytes), "search_posting_bytes/op")
			b.ReportMetric(float64(last.SearchFullRebuilds), "search_full_rebuilds/op")
			b.ReportMetric(float64(last.SearchAllTokenRekeys), "search_all_token_rekeys/op")
			b.ReportMetric(float64(last.LifecycleMutations), "lifecycle_mutations/op")
			b.ReportMetric(float64(last.PebbleCommits), "pebble_sync_commits/op")
			b.ReportMetric(float64(last.PebbleLogicalBytes), "pebble_logical_bytes/op")
			b.ReportMetric(float64(last.PebbleCommitDuration.Nanoseconds()), "pebble_commit_ns/op")
			if last.LifecycleMutations != 1 {
				b.Fatalf("lifecycle mutations = %d, want 1", last.LifecycleMutations)
			}
			if last.PebbleCommits != 2 {
				b.Fatalf("Pebble sync commits = %d, want canonical acceptance batch plus outbox-head publication", last.PebbleCommits)
			}
			if err := pebblestore.ValidateV3PlanAcceptanceFixedPath(last, 2); err != nil {
				b.Fatalf("history=%d acceptance path is amplified: %v; metrics=%+v", history, err, last)
			}
			if last.SearchPostingsRead != 0 || last.SearchPostingsDeleted != 0 || last.SearchPostingsSet > 16 || last.SearchPostingLogicalBytes > 8*1024 {
				b.Fatalf("history=%d acceptance exceeded bounded metadata-posting work: metrics=%+v", history, last)
			}
		})
	}
}

func newExitPlanModeAcceptanceBenchmarkService(tb testing.TB) (*Service, *sessionruntime.Service, *pebblestore.Store, *pebblestore.SessionStore) {
	tb.Helper()
	store, err := pebblestore.Open(filepath.Join(tb.TempDir(), "exit-plan-acceptance.pebble"))
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	sessionStore := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		tb.Fatalf("open event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessionStore, events)
	return &Service{sessions: sessionSvc}, sessionSvc, store, sessionStore
}

func benchmarkExitPlanModeDocument() *pebblestore.SessionPlanDocument {
	return &pebblestore.SessionPlanDocument{
		ID: "plan-acceptance", Title: "Acceptance",
		Info:               pebblestore.SessionPlanInfo{Goal: "measure exit plan acceptance"},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Measure", Status: sessionruntime.PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}
}

func seedExitPlanModeAcceptanceHistory(tb testing.TB, store *pebblestore.Store, sessionID string, count int) {
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
			message := pebblestore.MessageSnapshot{ID: fmt.Sprintf("history-%d", seq), SessionID: sessionID, UserID: "user", AccountScopeID: "account", GlobalSeq: seq, Role: "user", Content: fmt.Sprintf("historical token-%06d shared", i), CreatedAt: int64(seq)}
			raw, err := json.Marshal(message)
			if err != nil {
				batch.Close()
				tb.Fatal(err)
			}
			if err := batch.Set([]byte(pebblestore.KeyV3SessionMessage(sessionID, seq)), raw, nil); err != nil {
				batch.Close()
				tb.Fatal(err)
			}
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			batch.Close()
			tb.Fatalf("seed history: %v", err)
		}
		batch.Close()
	}
}
