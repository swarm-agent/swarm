package api

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
)

// Purpose: prepareLandWorktree -> Queue.Process -> Land must preserve supervised
// Git evidence through preparation, checks and JSON persistence. The supervisor
// fake exercises the actual ExecResult construction/redaction, not trimmed output;
// malformed final evidence must fail before Land invokes a privileged command.
func TestPrepareLandQueueSupervisedOutput(t *testing.T) {
	head, base := "a1"+strings.Repeat("b", 38), strings.Repeat("c", 40)
	for _, output := range []string{head + "\n", "progress\r\n \t" + head + "\r\n\n", head + "\nnot-a-commit\n"} {
		q := publish.NewQueue(&landMemoryStore{})
		entry := publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: "/project", Branch: "agent/job", Head: strings.Repeat("d", 40), BaseBranch: "dev"}}
		if _, err := q.Enqueue(entry); err != nil {
			t.Fatal(err)
		}
		landCalls, checkCalls := 0, 0
		err := q.Process(context.Background(), entry.Account, entry.Request.Folder, publish.QueueHooks{
			Prepare: func(ctx context.Context, e *publish.Entry) error {
				return prepareLandWorktree(ctx, publish.Rule{Folder: "/project", Mode: "land"}, e, "/project/job", []string{"check"}, 1,
					func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
						raw := ""
						switch strings.Join(req.Command, " ") {
						case "git branch --show-current":
							raw = "agent/job\n"
						case "git rev-parse HEAD":
							raw = output
						case "bash -lc check":
							checkCalls++
							raw = "[critical/fast] PASS\n"
						}
						return provider.ExecSupervised(ctx, &landOutputTransport{raw}, "box", provider.ExecRequest{Command: req.Command, Env: map[string]string{"GIT_CONFIG_COUNT": "1"}, Timeout: time.Second, OnProgress: func(p provider.ExecProgress) {
							if req.OnOutput != nil {
								req.OnOutput(p.Data)
							}
						}}, time.Second)
					}, func(context.Context, publish.Rule, string) (publish.Base, error) {
						return publish.Base{Commit: base, Branch: "dev"}, nil
					})
			},
			Land: func(ctx context.Context, e publish.Entry) (publish.LandResult, error) {
				landCalls++
				if e.Request.Head != head || e.Request.BaseCommit != base || e.Request.BaseBranch != "dev" || len(e.Checks) != 1 {
					t.Fatalf("prepared entry lost: %+v", e)
				}
				// No trusted command in this hermetic fixture: reaching this refusal proves
				// Land accepted all evidence and mode without running a privileged command.
				_, err := publish.Land(ctx, publish.Rule{Mode: "land", Command: "/missing-command"}, e.Request)
				if err == nil || err.Error() != "land command is not a trusted executable" {
					t.Fatalf("Land evidence refused: %v", err)
				}
				return publish.LandResult{Commit: head}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := q.Get(entry.Account, "job")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "not-a-commit") {
			if landCalls != 0 || checkCalls != 0 || got.State != "land_failed" {
				t.Fatalf("invalid evidence accepted: %+v", got)
			}
		} else if landCalls != 1 || checkCalls != 1 || got.State != "landed" || got.Request.Head != head || got.Commit != head || len(got.Checks) != 1 {
			t.Fatalf("landing failed: %+v", got)
		}
	}
}

type landMemoryStore struct{ data []byte }

func (s *landMemoryStore) GetJSON(_ string, v any) (bool, error) {
	if len(s.data) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(s.data, v)
}
func (s *landMemoryStore) PutJSON(_ string, v any) error {
	var err error
	s.data, err = json.Marshal(v)
	return err
}

type landOutputTransport struct{ out string }

func (f *landOutputTransport) RunExec(_ context.Context, _ io.Reader, stdout, _ io.Writer, _ ...string) error {
	_, err := io.WriteString(stdout, f.out)
	return err
}
func (f *landOutputTransport) RunExecCombined(context.Context, ...string) ([]byte, error) {
	return nil, nil
}
