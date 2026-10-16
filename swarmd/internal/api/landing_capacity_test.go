package api

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/publish"
)

// Purpose: admitLanding must cap checks across folders, without losing drain
// leases or permits on cancellation. The admission boundary is the narrowest
// layer that can prove this independently of Git and check execution.
func TestLandingCPUAdmission(t *testing.T) {
	s := &Server{perm: landPrincipalCapacity{manager: executioncapacity.NewManager(executioncapacity.ManagerConfig{})}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < max(1, runtime.NumCPU()/4); i++ {
		release, err := s.admitLanding(ctx, publish.Entry{Account: "account", Request: publish.Request{JobID: fmt.Sprint(i), Folder: fmt.Sprint(i)}})
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	blocked, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	release, err := s.admitLanding(blocked, publish.Entry{Account: "other", Request: publish.Request{JobID: "blocked", Folder: "other"}})
	if err == nil {
		release()
		t.Fatal("landing exceeded box-wide CPU cap")
	}
	releases[0]()
	releases = releases[1:]
	release, err = s.admitLanding(ctx, publish.Entry{Account: "other", Request: publish.Request{JobID: "next", Folder: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	releases = append(releases, release)
}

// Purpose: startup overrides must control admitLanding, and failed drain
// admission must return the CPU permit. Exercise the actual lease boundary
// without check subprocesses or a durable queue fixture.
func TestLandingCPUOverrideAndDrain(t *testing.T) {
	manager := executioncapacity.NewManager(executioncapacity.ManagerConfig{})
	s := &Server{perm: landPrincipalCapacity{manager: manager}}
	if err := s.SetLandingMaxConcurrent(-1); err == nil {
		t.Fatal("negative cap accepted")
	}
	if err := s.SetLandingMaxConcurrent(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entry := publish.Entry{Account: "account", Request: publish.Request{JobID: "one", Folder: "one"}}
	release, err := s.admitLanding(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if cap(s.landingSlots) != 1 {
		t.Fatal("override ignored")
	}
	if err := s.SetLandingMaxConcurrent(2); err == nil {
		t.Fatal("live cap mutation accepted")
	}
	release()
	release()
	if len(s.landingSlots) != 0 {
		t.Fatal("release leaked permit")
	}
	if _, err := manager.BeginDrain(time.Hour); err != nil {
		t.Fatal(err)
	}
	if release, err := s.admitLanding(ctx, entry); err == nil {
		release()
		t.Fatal("drain admitted landing")
	}
	if len(s.landingSlots) != 0 {
		t.Fatal("failed admission leaked permit")
	}
}
