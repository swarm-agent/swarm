package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/publish"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: SetEnvironmentProvider must accept the Cloud Build provider at the
// startup boundary. This direct test reproduces rejection without a live daemon.
func TestCloudBuildEnvironmentSelection(t *testing.T) {
	if err := (&Server{}).SetEnvironmentProvider("gcp-cloudbuild"); err != nil {
		t.Fatal(err)
	}
}

type cloudStorageFake struct {
	objects map[string][]byte
	result  string
	deleted int
	failPut bool
}

func (f *cloudStorageFake) Put(_ context.Context, _, name string, r io.Reader) error {
	if f.failPut {
		return fmt.Errorf("upload failed")
	}
	b, err := io.ReadAll(r)
	f.objects[name] = b
	return err
}
func (f *cloudStorageFake) Get(context.Context, string, string) ([]byte, error) {
	if f.result == "" {
		return nil, fmt.Errorf("missing result")
	}
	return []byte(f.result), nil
}
func (f *cloudStorageFake) DeletePrefix(_ context.Context, _, prefix string) error {
	f.deleted++
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			delete(f.objects, k)
		}
	}
	return nil
}

type cloudBuildFake struct {
	build  environment.Build
	status string
	fail   bool
}

func (f *cloudBuildFake) Create(_ context.Context, b environment.Build) (environment.Build, error) {
	f.build = b
	if f.fail {
		return environment.Build{}, fmt.Errorf("create failed")
	}
	return environment.Build{ID: "test-build"}, nil
}
func (f *cloudBuildFake) Get(context.Context, string) (environment.Build, error) {
	return environment.Build{ID: "test-build", Status: f.status}, nil
}

// Purpose: real archive creation and the landing check boundary must map two
// Cloud Build results, send failed checks back, and reserve auto fallback for
// infrastructure errors. Fake remote clients keep this narrow integration test
// hermetic; it does not claim to execute Google's service or the generated script.
func TestCloudBuildEnvironmentLanding(t *testing.T) {
	for _, mode := range []string{"success", "check_failure", "missing", "auto_missing", "upload_failure", "create_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			source, head := runnerSource(t)
			storage := &cloudStorageFake{objects: map[string][]byte{}, result: `{"checks":[{"exit_code":0,"duration_ms":1,"output":"first"},{"exit_code":0,"duration_ms":2,"output":"second"}]}`}
			builds := &cloudBuildFake{status: "SUCCESS"}
			switch mode {
			case "check_failure":
				storage.result = `{"checks":[{"exit_code":0,"duration_ms":1,"output":"first"},{"exit_code":7,"duration_ms":2,"output":"FAIL second"}]}`
			case "missing", "auto_missing":
				storage.result = ""
			case "upload_failure":
				storage.failPut = true
			case "create_failure":
				builds.fail = true
			case "cancelled":
				builds.status = "CANCELLED"
			}
			cloud := environment.CloudBuild{Store: db, Storage: storage, Builds: builds, Config: environment.CloudBuildConfig{Project: "test-project", Bucket: "test-bucket", TimeoutMinutes: 1}}
			var p environment.Provider = cloud
			local := &recordingLandProvider{}
			if mode == "auto_missing" {
				p = environment.Auto{Remote: cloud, Local: landExecutorProvider{local.Exec}}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entry := &publish.Entry{Account: "account", Request: publish.Request{Branch: "job"}}
			git := func(_ context.Context, r environment.ExecRequest) (*environment.ExecResult, error) {
				out := ""
				if r.Command[1] == "branch" {
					out = "job"
				}
				if r.Command[1] == "rev-parse" {
					out = head
				}
				return &environment.ExecResult{Stdout: out}, nil
			}
			err = prepareLandWorktreeWithExecutors(ctx, publish.Rule{}, entry, source, []string{"first", "second"}, 1, git, p, environment.Spec{Checkout: source, Definition: environment.Definition{BaseImage: "test-image"}}, func(context.Context, publish.Rule, string) (publish.Base, error) {
				return publish.Base{Commit: head, Branch: "dev"}, nil
			})
			switch mode {
			case "success", "auto_missing":
				if err != nil {
					t.Fatal(err)
				}
				if len(entry.Checks) != 2 {
					t.Fatalf("checks: %+v", entry.Checks)
				}
			case "check_failure":
				var send *publish.SendBackError
				if !errors.As(err, &send) {
					t.Fatalf("expected send-back: %v", err)
				}
			default:
				var failure *environment.Failure
				if !errors.As(err, &failure) {
					t.Fatalf("expected environment failure: %v", err)
				}
			}
			if storage.deleted == 0 || len(storage.objects) != 0 {
				t.Fatalf("prefix not cleaned: %+v", storage)
			}
			if mode == "success" && (entry.Checks[0].ExitCode != 0 || entry.Checks[1].ExitCode != 0 || entry.Checks[0].Output != "first" || entry.Checks[1].Output != "second") {
				t.Fatalf("incorrect mapped checks: %+v", entry.Checks)
			}
			if mode == "check_failure" && (len(entry.Checks) != 2 || entry.Checks[1].ExitCode != 7 || !strings.Contains(entry.Checks[1].Output, "FAIL second")) {
				t.Fatalf("incorrect failed check: %+v", entry.Checks)
			}
			// Purpose: the landing projection must preserve remote per-check timings,
			// rather than charging polling time or discarding result.json durations.
			if mode == "success" {
				data, _ := json.Marshal(entry.Checks)
				var timings []struct {
					DurationMS int64 `json:"duration_ms"`
				}
				_ = json.Unmarshal(data, &timings)
				if timings[0].DurationMS != 1 || timings[1].DurationMS != 2 {
					t.Fatalf("lost durations: %s", data)
				}
			}
			if mode == "success" && entry.Environment != "cloudbuild:test-build" {
				t.Fatalf("environment=%q", entry.Environment)
			}
			if mode == "auto_missing" && entry.Environment != "local" {
				t.Fatalf("fallback=%q", entry.Environment)
			}
			if len(builds.build.Steps) > 0 {
				script := builds.build.Steps[0].Args[1]
				for _, s := range []string{"sha256 mismatch", "for command in request['checks']", "transfer('result.json'", "transfer('output.log'"} {
					if !strings.Contains(script, s) {
						t.Errorf("missing script contract: %s", s)
					}
				}
			}
		})
	}
}
