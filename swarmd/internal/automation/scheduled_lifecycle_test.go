package automation

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: accepted policy bytes must reach repeated TickAt/Dispatch calls with
// exact due timestamps and immutable plan pins. Real approval/definition/receipt
// storage and production scheduling are the narrowest deterministic boundary;
// a recording runtime replaces providers, so these are not actual start timings.
// Edits must stop new old-policy admissions and require fresh approval, while
// already admitted records retain their original revision rather than repinning.
func TestScheduledLifecycleAcceptedRevisionAndEdits(t *testing.T) {
	for _, schedule := range []store.AutomationSchedulePolicy{
		{Kind: "cron", Expression: "0 18 * * *", Timezone: "UTC"},
		{Kind: "cron", Expression: "0 */6 * * *", Timezone: "UTC"},
		{Kind: "interval", IntervalSeconds: 3600},
	} {
		t.Run(schedule.Kind+schedule.Expression, func(t *testing.T) {
			ctx := context.Background()
			_, _, ownership, plans, user, scope, d := fixture(t)
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
			actual := user
			approval, err := NewPolicyApproval(db, plans, ownership, ApprovalIdentity{Current: func(context.Context) (Principal, error) { return actual, nil }, ExplicitUser: func(context.Context) (Principal, error) {
				if actual.Role != "user" {
					return Principal{}, ErrDenied
				}
				return actual, nil
			}}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			svc, err := New(db, plans, approval, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			d.Schedule = schedule
			d.Enabled = false
			d.Authorization = store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: now.Add(72 * time.Hour).UnixMilli()}
			head, _, err := svc.SaveDefinition(ctx, user, scope, "scheduled", "create", 0, d)
			if err != nil {
				t.Fatal(err)
			}
			enable := func(r store.AutomationRecord) store.AutomationRecord {
				t.Helper()
				actual = user
				digest, _ := ApprovalPolicyDigest(*r.Definition)
				g, err := approval.ApproveUser(ctx, ApprovalRequest{Scope: scope, AutomationID: r.ID, DefinitionRevision: r.Revision, PolicySHA256: digest})
				if err != nil {
					t.Fatal(err)
				}
				next := *r.Definition
				next.Enabled = true
				next.Authorization.Mode = "approved_policy"
				next.Authorization.ApprovalReference = g.ID
				out, _, err := svc.SaveDefinition(ctx, user, scope, r.ID, fmt.Sprint("enable-", r.Revision), r.Revision, next)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			head = enable(head)
			runtime := &scheduledRecordingRuntime{plans: head.Definition.Plans}
			exec, err := NewExecutionService(svc, runtime, triggerAuthorityFake{})
			if err != nil {
				t.Fatal(err)
			}
			slots, err := scheduleSlots(ctx, head, now.UnixMilli(), now.Add(24*time.Hour).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			want := 24
			if schedule.Kind == "cron" {
				want = 4
				if schedule.Expression == "0 18 * * *" {
					want = 1
				}
			}
			if len(slots) != want {
				t.Fatalf("slots %d want %d", len(slots), want)
			}
			for i, slot := range slots {
				expected := head.WrittenAt + int64(i)*3600000
				if schedule.Kind == "cron" {
					expected = head.WrittenAt + int64(i)*6*3600000
					if want == 1 {
						expected = head.WrittenAt + 18*3600000
					}
				}
				if slot.ScheduledAt != expected {
					t.Fatalf("due %d want %d", slot.ScheduledAt, expected)
				}
				now = time.UnixMilli(slot.ScheduledAt).Add(20 * time.Second)
				actual = user
				actual.Role = "system"
				for retry := 0; retry < 2; retry++ {
					if err := exec.TickAt(ctx, actual, scope, head.ID, head.Revision, now); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := exec.RecoverPage(ctx, actual, scope, head.ID, ""); err != nil {
					t.Fatal(err)
				}
			}
			if len(runtime.records) != want {
				t.Fatalf("dispatch count %d want %d", len(runtime.records), want)
			}
			for i, r := range runtime.records {
				if r.Occurrence.DefinitionRevision != head.Revision || r.Occurrence.ScheduledAt != slots[i].ScheduledAt {
					t.Fatal("dispatch repinned or drifted")
				}
			}
			old := head
			actual = user
			now = now.Add(time.Minute)
			changed := *head.Definition
			changed.Schedule = store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 7200}
			head, _, err = svc.SaveDefinition(ctx, user, scope, head.ID, "edit", head.Revision, changed)
			if err != nil {
				t.Fatal(err)
			}
			if head.Definition.Enabled || head.Definition.Authorization.ApprovalReference != "" {
				t.Fatal("edit reused approval")
			}
			actual.Role = "system"
			if err := exec.Tick(ctx, actual, scope, head.ID, old.Revision); err == nil {
				t.Fatal("stale revision admitted")
			}
			if err := exec.Tick(ctx, actual, scope, head.ID, head.Revision); err == nil {
				t.Fatal("unapproved edit admitted")
			}
			rows, _, err := db.SearchAutomationRecords(store.AutomationSearch{Scope: scope, AutomationID: head.ID, Kind: "occurrence", Limit: 50})
			if err != nil || len(rows) != want {
				t.Fatal("rejected edit changed occurrences", err)
			}
			for _, r := range rows {
				if r.Occurrence.DefinitionRevision != old.Revision {
					t.Fatal("history repinned")
				}
			}
			head = enable(head)
			now = time.UnixMilli(head.WrittenAt).Add(2 * time.Hour)
			actual.Role = "system"
			if err := exec.Tick(ctx, actual, scope, head.ID, head.Revision); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.RecoverPage(ctx, actual, scope, head.ID, ""); err != nil {
				t.Fatal(err)
			}
			if len(runtime.records) != want+1 || runtime.records[want].Occurrence.DefinitionRevision != head.Revision {
				t.Fatal("reapproved dispatch missing")
			}
		})
	}
}

type scheduledRecordingRuntime struct {
	records []store.AutomationRecord
	plans   []store.AutomationPlanBinding
}

func (r *scheduledRecordingRuntime) Ensure(_ context.Context, _ Principal, def, occ store.AutomationRecord) (string, error) {
	if def.Revision != occ.Occurrence.DefinitionRevision || !reflect.DeepEqual(def.Definition.Plans, r.plans) || def.Definition.Plans[0].Plan.DocumentSHA256 == "" {
		return "", ErrDenied
	}
	r.records = append(r.records, occ)
	return "execution-" + occ.ID, nil
}
