package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/tool/searchipc"
)

func TestSearchCoordinatorHelperProcess(t *testing.T) {
	if os.Getenv("SWARM_SEARCH_COORDINATOR_HELPER") != "1" {
		return
	}
	var root string
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req searchipc.Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		if root == "" {
			root = req.IndexRoot
		}
		if req.IndexRoot != root {
			_ = enc.Encode(searchipc.Response{RequestID: req.RequestID, HelperError: "root mismatch"})
			continue
		}
		if req.Queries[0] == "crash" {
			os.Exit(2)
		}
		if req.Queries[0] == "slow" {
			time.Sleep(150 * time.Millisecond)
		}
		_ = enc.Encode(searchipc.Response{ProtocolVersion: searchipc.ProtocolVersion, RequestID: req.RequestID, Completed: true, Diagnostics: searchipc.Diagnostics{ColdStartCount: 1}})
	}
}

func testSearchCoordinator(t *testing.T, maxRoots int) *SearchCoordinator {
	t.Helper()
	c := NewSearchCoordinator(maxRoots)
	c.resolve = func() (string, error) {
		return os.Executable()
	}
	c.helperArgs = []string{"-test.run=^TestSearchCoordinatorHelperProcess$"}
	old := os.Getenv("SWARM_SEARCH_COORDINATOR_HELPER")
	if err := os.Setenv("SWARM_SEARCH_COORDINATOR_HELPER", "1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = os.Setenv("SWARM_SEARCH_COORDINATOR_HELPER", old) })
	return c
}

func testSearchRequest(root, query string) searchipc.Request {
	return searchipc.Request{IndexRoot: root, TargetPath: root, Operation: "content", Queries: []string{query}, MaxResults: 10, TimeoutMillis: 1000}
}

func TestSearchCoordinatorReusesAndSerializesRoot(t *testing.T) {
	root := t.TempDir()
	c := testSearchCoordinator(t, 4)
	for i := 0; i < 20; i++ {
		if _, err := c.Execute(context.Background(), testSearchRequest(root, fmt.Sprintf("q-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	s := c.Snapshot()
	if s.ColdStarts != 1 || s.NativeExecutions != 20 || s.ResidentRoots != 1 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestSearchCoordinatorCoalescesIdenticalAndCancelsWaiter(t *testing.T) {
	root := t.TempDir()
	c := testSearchCoordinator(t, 4)
	var ready sync.WaitGroup
	ready.Add(2)
	var ok atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	contexts := []context.Context{context.Background(), ctx}
	for i := 0; i < 2; i++ {
		go func(callCtx context.Context) {
			ready.Done()
			if _, err := c.Execute(callCtx, testSearchRequest(root, "slow")); err == nil {
				ok.Add(1)
			}
		}(contexts[i])
	}
	ready.Wait()
	time.Sleep(250 * time.Millisecond)
	s := c.Snapshot()
	if s.NativeExecutions != 1 || s.CoalescedWaiters != 1 || ok.Load() != 1 {
		t.Fatalf("unexpected coalescing/cancellation: stats=%+v successes=%d", s, ok.Load())
	}
}

func TestSearchCoordinatorEvictsIdleAndRestartsCrash(t *testing.T) {
	c := testSearchCoordinator(t, 1)
	root1, root2 := t.TempDir(), t.TempDir()
	if _, err := c.Execute(context.Background(), testSearchRequest(root1, "one")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(context.Background(), testSearchRequest(root2, "two")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(context.Background(), testSearchRequest(root2, "crash")); err == nil {
		t.Fatal("expected crash error")
	}
	s := c.Snapshot()
	if s.Evictions != 1 || s.WorkerRestarts == 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestSearchCoordinatorParallelFirstWaveSharesOneRootWorker(t *testing.T) {
	root := t.TempDir()
	c := testSearchCoordinator(t, 4)
	const requests = 8
	start := make(chan struct{})
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		go func(i int) {
			<-start
			_, err := c.Execute(context.Background(), testSearchRequest(root, fmt.Sprintf("wave-%d", i)))
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < requests; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if s := c.Snapshot(); s.ColdStarts != 1 || s.NativeExecutions != requests || s.ResidentRoots != 1 {
		t.Fatalf("parallel first wave launched repeated workers: %+v", s)
	}
}

func TestSearchCoordinatorDifferentRootsProgressIndependently(t *testing.T) {
	c := testSearchCoordinator(t, 2)
	roots := []string{t.TempDir(), t.TempDir()}
	started := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for _, root := range roots {
		go func(root string) {
			defer wg.Done()
			if _, err := c.Execute(context.Background(), testSearchRequest(root, "slow")); err != nil {
				t.Errorf("execute %s: %v", root, err)
			}
		}(root)
	}
	wg.Wait()
	if elapsed := time.Since(started); elapsed > 260*time.Millisecond {
		t.Fatalf("different roots did not progress independently: %s", elapsed)
	}
	if s := c.Snapshot(); s.ColdStarts != 2 || s.NativeExecutions != 2 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestSearchCoordinatorCloseStopsHelper(t *testing.T) {
	root := t.TempDir()
	c := testSearchCoordinator(t, 1)
	if _, err := c.Execute(context.Background(), testSearchRequest(root, "one")); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	worker := c.workers[filepath.Clean(root)]
	c.mu.Unlock()
	if worker == nil || worker.cmd == nil {
		t.Fatal("helper was not started")
	}
	pid := worker.cmd.Process.Pid
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-0", fmt.Sprint(pid)).Run(); err == nil {
		t.Fatalf("helper %d remains alive", pid)
	}
	if s := c.Snapshot(); s.Shutdowns != 1 || s.ResidentRoots != 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}
