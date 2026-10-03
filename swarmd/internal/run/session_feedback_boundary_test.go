package run

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/tool"
)

// Embedding the existing hermetic runner preserves its capability declarations.
// The callback runs inside the actual provider attempt, not between simulated runs.
type feedbackBoundaryRunner struct {
	*googleOverflowTestRunner
	step func(context.Context, provideriface.Request) (provideriface.Response, error)
}

func (r *feedbackBoundaryRunner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.step(ctx, req)
}

func (r *feedbackBoundaryRunner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.step(ctx, req)
}

// Purpose: RunTurnWithOptions must inject durable notes queued during an active
// provider step only at a subsequent eligible boundary and publish identity-bound
// receipts only after success. The real run loop, V3 store and tool dispatch with
// a deterministic provider adapter are the narrowest hermetic proof of delivery,
// failed/interrupted attempts, no-further-step behavior and read_messages visibility.
// This is not a live provider benchmark or evidence of semantic incorporation.
func TestFeedbackProviderStepBoundary(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "interrupted", "terminal"} {
		t.Run(outcome, func(t *testing.T) {
			svc, sessions, _, base, sessionID := newGoogleOverflowTestFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p := identity.Principal{Type: "user", UserID: "user-1", AccountScopeID: "account-1"}
			snapshot, _, err := sessions.GetSession(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			scope := tool.WorkspaceScope{Principal: p, SessionID: "sender", PrimaryPath: snapshot.WorkspacePath}
			invoke := func(args map[string]any) (map[string]any, error) {
				raw, err := json.Marshal(args)
				if err != nil {
					return nil, err
				}
				output, err := svc.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, scope, tool.Call{Name: "manage-sessions", Arguments: string(raw)})
				if err != nil {
					return nil, err
				}
				var result map[string]any
				err = json.Unmarshal([]byte(output), &result)
				return result, err
			}
			const marker = "feedback-boundary-acknowledgement"
			var mu sync.Mutex
			calls := 0
			messageID := ""
			received := false
			runner := &feedbackBoundaryRunner{googleOverflowTestRunner: base}
			runner.step = func(stepCtx context.Context, req provideriface.Request) (provideriface.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				input, err := json.Marshal(req.Input)
				if err != nil {
					return provideriface.Response{}, err
				}
				if calls == 1 {
					if strings.Contains(string(input), marker) {
						return provideriface.Response{}, errors.New("note present before queueing")
					}
					for retry := 0; retry < 2; retry++ {
						out, err := invoke(map[string]any{"action": "send_message", "session_id": sessionID, "prompt": marker, "trigger": false, "client_request_id": "stable-feedback-key"})
						if err != nil {
							return provideriface.Response{}, err
						}
						id, _ := out["message_id"].(string)
						if id == "" || (messageID != "" && id != messageID) || out["delivery_status"] != "unconfirmed" || out["incorporation_status"] != "unconfirmed" {
							return provideriface.Response{}, errors.New("queue falsely acknowledged delivery or changed retry identity")
						}
						messageID = id
					}
					if outcome == "terminal" {
						return provideriface.Response{Text: "Finished without another step."}, nil
					}
					return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "list", Name: "list", Arguments: `{"path":".","max_entries":1}`}}}, nil
				}
				if calls != 2 || strings.Count(string(input), marker) != 1 {
					return provideriface.Response{}, errors.New("next step did not receive exactly one feedback note")
				}
				messages, err := sessions.ListSessionMessages(sessionID, 0, 100)
				if err != nil {
					return provideriface.Response{}, err
				}
				for _, m := range messages {
					if m.Metadata["source"] == "feedback_delivery" {
						return provideriface.Response{}, errors.New("receipt published before provider success")
					}
				}
				received = true
				if outcome == "failure" {
					return provideriface.Response{}, errors.New("injected feedback provider failure")
				}
				if outcome == "interrupted" {
					cancel()
					return provideriface.Response{}, context.Canceled
				}
				return provideriface.Response{Text: "Acknowledged " + marker}, nil
			}
			svc.providers.RegisterRunner(runner)
			result, runErr := svc.RunTurnWithOptions(ctx, sessionID, RunOptions{Prompt: "Inspect the workspace once.", Principal: p, ApplySessionMutation: sessions.ApplySessionMutation})
			mu.Lock()
			defer mu.Unlock()
			if outcome == "failure" || outcome == "interrupted" {
				if runErr == nil {
					t.Fatal("failed attempt succeeded")
				}
			} else if runErr != nil {
				t.Fatal(runErr)
			}
			wantCalls := 2
			if outcome == "terminal" {
				wantCalls = 1
			}
			if calls != wantCalls || messageID == "" || received != (outcome != "terminal") {
				t.Fatalf("provider calls=%d id=%q received=%v error=%v", calls, messageID, received, runErr)
			}
			messages, err := sessions.ListSessionMessages(sessionID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			notes, receipts := 0, 0
			for _, m := range messages {
				if m.ID == messageID {
					notes++
				}
				if m.Metadata["source"] == "feedback_delivery" {
					receipts++
					if m.Metadata["message_id"] != messageID || m.Metadata["delivery_status"] != "delivered" || m.Metadata["incorporation_status"] != "unconfirmed" || m.Metadata["run_id"] == "" {
						t.Fatalf("incorrect receipt: %+v", m)
					}
				}
			}
			wantReceipts := 0
			if outcome == "success" {
				wantReceipts = 1
			}
			if notes != 1 || receipts != wantReceipts {
				t.Fatalf("notes=%d receipts=%d, want 1/%d", notes, receipts, wantReceipts)
			}
			if outcome == "success" {
				if !strings.Contains(result.AssistantMessage.Content, marker) {
					t.Fatal("missing acknowledgement")
				}
				out, err := invoke(map[string]any{"action": "read_messages", "session_id": sessionID, "role": "system", "limit": 100})
				if err != nil {
					t.Fatal(err)
				}
				visible := 0
				for _, item := range out["messages"].([]any) {
					if receipt, ok := item.(map[string]any)["feedback_delivery"].(map[string]any); ok {
						visible++
						if receipt["message_id"] != messageID || receipt["incorporation_status"] != "unconfirmed" {
							t.Fatalf("read_messages lost identity: %+v", receipt)
						}
					}
				}
				if visible != 1 {
					t.Fatalf("visible receipts=%d", visible)
				}
			}
		})
	}
}
