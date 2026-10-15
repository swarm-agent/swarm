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

// Purpose: prepareLandWorktree and Queue.Process own the landing failure route.
// Container death must retry only once and become land_failed without worker
// send-back; an actual nonzero check must retain the checks-failed send-back.
// A scripted executor is the narrowest layer proving these queue postconditions.
func TestPrepareLandSandboxDeath(t *testing.T) {
	for _, mode := range []string{"recover", "dies twice", "check fails"} {
		t.Run(mode, func(t *testing.T) {
			head := strings.Repeat("a", 40)
			q := publish.NewQueue(&landMemoryStore{})
			entry := publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: "/project", Branch: "agent/job", Head: head, BaseBranch: "dev"}}
			if _, err := q.Enqueue(entry); err != nil {
				t.Fatal(err)
			}
			calls, landed, backs := 0, false, 0
			err := q.Process(context.Background(), entry.Account, entry.Request.Folder, publish.QueueHooks{
				Prepare: func(ctx context.Context, e *publish.Entry) error {
					return prepareLandWorktree(ctx, publish.Rule{Folder: "/project", Mode: "land"}, e, "/project/job", []string{"check"}, 1,
						func(_ context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
							res := &provider.ExecResult{}
							switch strings.Join(req.Command, " ") {
							case "git branch --show-current":
								res.Stdout = "agent/job\n"
							case "git rev-parse HEAD":
								res.Stdout = head + "\n"
							case "bash -lc check":
								calls++
								if mode == "dies twice" || (mode == "recover" && calls == 1) {
									return nil, &sandbox.ErrSandboxDied{OOMKilled: true, Memory: "6g"}
								}
								if mode == "check fails" {
									res.ExitCode = 1
								}
							}
							return res, nil
						}, func(context.Context, publish.Rule, string) (publish.Base, error) {
							return publish.Base{Commit: strings.Repeat("b", 40), Branch: "dev"}, nil
						})
				},
				Land: func(context.Context, publish.Entry) (publish.LandResult, error) {
					landed = true
					return publish.LandResult{Commit: head}, nil
				},
				SendBack: func(context.Context, publish.Entry) error { backs++; return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := q.Get(entry.Account, "job")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "recover":
				if calls != 2 || !landed || got.State != "landed" {
					t.Fatalf("%d %+v", calls, got)
				}
			case "dies twice":
				if calls != 2 || landed || backs != 0 || got.SendBacks != 0 || got.State != "land_failed" || !strings.Contains(got.Reason, "6g") {
					t.Fatalf("%d %+v", calls, got)
				}
			case "check fails":
				if calls != 1 || landed || backs != 1 || got.State != "sent_back" {
					t.Fatalf("%d %+v", calls, got)
				}
			}
		})
	}
}

// Purpose: prepareLandWorktreeWithExecutors must send only code-owned Git to
// daemon admission; arbitrary folder checks must retain agent throttling. This
// boundary test detects accidentally granting trusted execution to check text.
func TestPrepareLandSeparatesTrustedCommands(t *testing.T) {
	head := strings.Repeat("a", 40)
	entry := publish.Entry{Request: publish.Request{Branch: "agent/job", BaseBranch: "dev"}}
	checks := 0
	err := prepareLandWorktreeWithExecutors(context.Background(), publish.Rule{Mode: "land"}, &entry, "/project", []string{"git status --porcelain"}, 1,
		func(_ context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			out := ""
			switch strings.Join(req.Command, " ") {
			case "git branch --show-current":
				out = "agent/job\n"
			case "git rev-parse HEAD":
				out = head + "\n"
			}
			if req.Command[0] != "git" {
				t.Fatalf("user check reached trusted executor: %v", req.Command)
			}
			return &provider.ExecResult{Stdout: out}, nil
		}, func(_ context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			checks++
			if strings.Join(req.Command, " ") != "bash -lc git status --porcelain" {
				t.Fatalf("unexpected agent command: %v", req.Command)
			}
			return &provider.ExecResult{}, nil
		}, func(context.Context, publish.Rule, string) (publish.Base, error) {
			return publish.Base{Commit: head, Branch: "dev"}, nil
		})
	if err != nil || checks != 1 || entry.Request.Head != head {
		t.Fatalf("checks=%d entry=%+v err=%v", checks, entry, err)
	}
}
