package pebblestore

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
)

// V3PlanAcceptanceTelemetry is process-local, label-free evidence for work that
// can amplify native V3 plan acceptance. Counters are cumulative so focused
// tests and benchmarks can snapshot before and after one lifecycle operation.
type V3PlanAcceptanceTelemetry struct {
	MessageRowsScanned        uint64
	SearchPostingsRead        uint64
	SearchPostingsDeleted     uint64
	SearchPostingsSet         uint64
	SearchPostingLogicalBytes uint64
	SearchFullRebuilds        uint64
	SearchAllTokenRekeys      uint64
	LifecycleMutations        uint64
	PebbleCommits             uint64
	PebbleLogicalBytes        uint64
	PebbleCommitDuration      time.Duration
}

var v3PlanAcceptanceTelemetry struct {
	messageRowsScanned        atomic.Uint64
	searchPostingsRead        atomic.Uint64
	searchPostingsDeleted     atomic.Uint64
	searchPostingsSet         atomic.Uint64
	searchPostingLogicalBytes atomic.Uint64
	searchFullRebuilds        atomic.Uint64
	searchAllTokenRekeys      atomic.Uint64
	lifecycleMutations        atomic.Uint64
	pebbleCommits             atomic.Uint64
	pebbleLogicalBytes        atomic.Uint64
	pebbleCommitNanos         atomic.Uint64
}

func SnapshotV3PlanAcceptanceTelemetry() V3PlanAcceptanceTelemetry {
	return V3PlanAcceptanceTelemetry{
		MessageRowsScanned:        v3PlanAcceptanceTelemetry.messageRowsScanned.Load(),
		SearchPostingsRead:        v3PlanAcceptanceTelemetry.searchPostingsRead.Load(),
		SearchPostingsDeleted:     v3PlanAcceptanceTelemetry.searchPostingsDeleted.Load(),
		SearchPostingsSet:         v3PlanAcceptanceTelemetry.searchPostingsSet.Load(),
		SearchPostingLogicalBytes: v3PlanAcceptanceTelemetry.searchPostingLogicalBytes.Load(),
		SearchFullRebuilds:        v3PlanAcceptanceTelemetry.searchFullRebuilds.Load(),
		SearchAllTokenRekeys:      v3PlanAcceptanceTelemetry.searchAllTokenRekeys.Load(),
		LifecycleMutations:        v3PlanAcceptanceTelemetry.lifecycleMutations.Load(),
		PebbleCommits:             v3PlanAcceptanceTelemetry.pebbleCommits.Load(),
		PebbleLogicalBytes:        v3PlanAcceptanceTelemetry.pebbleLogicalBytes.Load(),
		PebbleCommitDuration:      time.Duration(v3PlanAcceptanceTelemetry.pebbleCommitNanos.Load()),
	}
}

func DeltaV3PlanAcceptanceTelemetry(after, before V3PlanAcceptanceTelemetry) V3PlanAcceptanceTelemetry {
	return V3PlanAcceptanceTelemetry{
		MessageRowsScanned:        after.MessageRowsScanned - before.MessageRowsScanned,
		SearchPostingsRead:        after.SearchPostingsRead - before.SearchPostingsRead,
		SearchPostingsDeleted:     after.SearchPostingsDeleted - before.SearchPostingsDeleted,
		SearchPostingsSet:         after.SearchPostingsSet - before.SearchPostingsSet,
		SearchPostingLogicalBytes: after.SearchPostingLogicalBytes - before.SearchPostingLogicalBytes,
		SearchFullRebuilds:        after.SearchFullRebuilds - before.SearchFullRebuilds,
		SearchAllTokenRekeys:      after.SearchAllTokenRekeys - before.SearchAllTokenRekeys,
		LifecycleMutations:        after.LifecycleMutations - before.LifecycleMutations,
		PebbleCommits:             after.PebbleCommits - before.PebbleCommits,
		PebbleLogicalBytes:        after.PebbleLogicalBytes - before.PebbleLogicalBytes,
		PebbleCommitDuration:      after.PebbleCommitDuration - before.PebbleCommitDuration,
	}
}

// ValidateV3PlanAcceptanceFixedPath is the post-fix regression threshold used by
// focused tests and benchmarks. The fixed acceptance path may do bounded point
// reads and writes, but it must never scan historical messages or rekey every
// existing token posting, and its sync-commit count must stay explicitly bounded.
func ValidateV3PlanAcceptanceFixedPath(metrics V3PlanAcceptanceTelemetry, maxCommits uint64) error {
	if metrics.MessageRowsScanned != 0 {
		return fmt.Errorf("plan acceptance scanned %d historical message rows, want zero", metrics.MessageRowsScanned)
	}
	if metrics.SearchFullRebuilds != 0 || metrics.SearchAllTokenRekeys != 0 {
		return fmt.Errorf("plan acceptance rebuilt search index %d times and rekeyed all tokens %d times, want zero", metrics.SearchFullRebuilds, metrics.SearchAllTokenRekeys)
	}
	if metrics.PebbleCommits > maxCommits {
		return fmt.Errorf("plan acceptance used %d Pebble commits, want at most %d", metrics.PebbleCommits, maxCommits)
	}
	return nil
}

// ObserveV3PlanLifecycleMutation records one canonical lifecycle service call.
// It intentionally carries no session, plan, user, or transcript labels.
func ObserveV3PlanLifecycleMutation() {
	v3PlanAcceptanceTelemetry.lifecycleMutations.Add(1)
}

func observeV3SearchMessageRowScanned() {
	v3PlanAcceptanceTelemetry.messageRowsScanned.Add(1)
}

func observeV3SearchPostingRead() {
	v3PlanAcceptanceTelemetry.searchPostingsRead.Add(1)
}

func observeV3SearchPostingDeleted(key string) {
	v3PlanAcceptanceTelemetry.searchPostingsDeleted.Add(1)
	v3PlanAcceptanceTelemetry.searchPostingLogicalBytes.Add(uint64(len(key)))
}

func observeV3SearchPostingSet(key string, value []byte) {
	v3PlanAcceptanceTelemetry.searchPostingsSet.Add(1)
	v3PlanAcceptanceTelemetry.searchPostingLogicalBytes.Add(uint64(len(key) + len(value)))
}

func observeV3SearchFullRebuild() {
	v3PlanAcceptanceTelemetry.searchFullRebuilds.Add(1)
}

func observeV3SearchAllTokenRekey() {
	v3PlanAcceptanceTelemetry.searchAllTokenRekeys.Add(1)
}

func (s *Store) commitV3PlanAcceptanceObserved(batch *pebble.Batch, options *pebble.WriteOptions) error {
	start := time.Now()
	logicalBytes := batch.Len()
	if err := batch.Commit(options); err != nil {
		return err
	}
	observeV3PlanAcceptanceCommit(logicalBytes, time.Since(start))
	return nil
}

func observeV3PlanAcceptanceCommit(logicalBytes int, duration time.Duration) {
	v3PlanAcceptanceTelemetry.pebbleCommits.Add(1)
	if logicalBytes > 0 {
		v3PlanAcceptanceTelemetry.pebbleLogicalBytes.Add(uint64(logicalBytes))
	}
	v3PlanAcceptanceTelemetry.pebbleCommitNanos.Add(uint64(duration))
}
