package environment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type timeoutBuilds struct{ build Build }

func (b *timeoutBuilds) Create(_ context.Context, v Build) (Build, error) {
	b.build = v
	return Build{ID: "test-build"}, nil
}
func (b *timeoutBuilds) Get(context.Context, string) (Build, error) {
	return Build{Status: "SUCCESS"}, nil
}

// Purpose: CloudBuild.Acquire must translate the installer's zero timeout into
// the folder timeout plus five minutes, capped at 24h, without rejecting a valid
// landing. Real payload creation and captured remote requests are the narrowest
// hermetic boundary; this is not a live Cloud Build qualification.
func TestCloudBuildAcquireTimeout(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	git := func(args ...string) string {
		t.Helper()
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = checkout
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("add", "file")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
	head := git("rev-parse", "HEAD")
	for _, tc := range []struct {
		name    string
		minutes int
		check   time.Duration
		want    string
	}{
		{"default", 0, 7 * time.Minute, "720s"}, {"cap", 0, 24 * time.Hour, "86400s"}, {"explicit", 10, time.Minute, "600s"},
		{"explicit cap", 1441, time.Minute, "86400s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &checkFake{objects: map[string]bool{}}
			builds := &timeoutBuilds{}
			p := CloudBuild{Store: db, Storage: storage, Builds: builds, Config: CloudBuildConfig{Project: "project", Bucket: "bucket", TimeoutMinutes: tc.minutes}}
			e, err := p.Acquire(ctx, Spec{Account: "account", Checkout: checkout, Commit: head, CheckTimeout: tc.check, Definition: Definition{BaseImage: "image"}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Release()
			want, err := time.ParseDuration(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.(*cloudEnvironment).timeout; got != want {
				t.Fatalf("unresolved wait timeout: %v", got)
			}
			if builds.build.Timeout != tc.want {
				t.Fatalf("timeout = %s, want %s", builds.build.Timeout, tc.want)
			}
		})
	}
}

// Purpose: Acquire must identify the invalid prerequisite before creating any
// remote objects. Direct provider calls isolate field diagnostics and side effects.
func TestCloudBuildAcquireMissingFields(t *testing.T) {
	for _, field := range []string{"cloudbuild_bucket", "cloudbuild_project", "storage client", "builds client", "store"} {
		t.Run(field, func(t *testing.T) {
			storage := &checkFake{objects: map[string]bool{}}
			builds := &timeoutBuilds{}
			p := CloudBuild{Store: &pebblestore.Store{}, Storage: storage, Builds: builds, Config: CloudBuildConfig{Project: "project", Bucket: "bucket", TimeoutMinutes: 1}}
			switch field {
			case "cloudbuild_bucket":
				p.Config.Bucket = ""
			case "cloudbuild_project":
				p.Config.Project = ""
			case "storage client":
				p.Storage = nil
			case "builds client":
				p.Builds = nil
			case "store":
				p.Store = nil
			}
			e, err := p.Acquire(context.Background(), Spec{CheckTimeout: time.Minute, Definition: Definition{BaseImage: "image"}})
			if err == nil || !strings.Contains(err.Error(), field) || e != nil {
				t.Fatalf("expected %s error, got %v", field, err)
			}
			if len(storage.objects) != 0 || builds.build.Timeout != "" {
				t.Fatal("invalid config caused remote side effects")
			}
		})
	}
}
