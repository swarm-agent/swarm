package pebblestore

import (
	"sync/atomic"
	"time"
)

// PlanRuntimeTelemetry exposes aggregate amplification evidence without labels
// containing session, plan, checkpoint, or subtask identifiers.
type PlanRuntimeTelemetry struct {
	Mutations              uint64
	EventBytes             uint64
	ProjectionBytes        uint64
	OutboxBytes            uint64
	ResultBytes            uint64
	LogicalBytes           uint64
	KeysPerCommitTotal     uint64
	ChangedTargets         uint64
	CommitDuration         time.Duration
	MutationDuration       time.Duration
	Conflicts              uint64
	Replays                uint64
	Recoveries             uint64
	RecoveriesWithSnapshot uint64
	RecoveryTailEvents     uint64
	RecoveryTailBytes      uint64
	RecoverySnapshotAge    time.Duration
	RecoveryDuration       time.Duration
}

type planRuntimeMutationObservation struct {
	eventBytes      int
	projectionBytes int
	outboxBytes     int
	resultBytes     int
	logicalBytes    int
	keys            int
	targets         int
	commitDuration  time.Duration
	totalDuration   time.Duration
}

var planRuntimeTelemetry struct {
	mutations, eventBytes, projectionBytes, outboxBytes atomic.Uint64
	resultBytes, logicalBytes, keys, targets            atomic.Uint64
	commitNanos, mutationNanos                          atomic.Uint64
	conflicts, replays                                  atomic.Uint64
	recoveries, recoveriesWithSnapshot                  atomic.Uint64
	tailEvents, tailBytes, snapshotAgeNanos             atomic.Uint64
	recoveryNanos                                       atomic.Uint64
}

func SnapshotPlanRuntimeTelemetry() PlanRuntimeTelemetry {
	return PlanRuntimeTelemetry{
		Mutations:              planRuntimeTelemetry.mutations.Load(),
		EventBytes:             planRuntimeTelemetry.eventBytes.Load(),
		ProjectionBytes:        planRuntimeTelemetry.projectionBytes.Load(),
		OutboxBytes:            planRuntimeTelemetry.outboxBytes.Load(),
		ResultBytes:            planRuntimeTelemetry.resultBytes.Load(),
		LogicalBytes:           planRuntimeTelemetry.logicalBytes.Load(),
		KeysPerCommitTotal:     planRuntimeTelemetry.keys.Load(),
		ChangedTargets:         planRuntimeTelemetry.targets.Load(),
		CommitDuration:         time.Duration(planRuntimeTelemetry.commitNanos.Load()),
		MutationDuration:       time.Duration(planRuntimeTelemetry.mutationNanos.Load()),
		Conflicts:              planRuntimeTelemetry.conflicts.Load(),
		Replays:                planRuntimeTelemetry.replays.Load(),
		Recoveries:             planRuntimeTelemetry.recoveries.Load(),
		RecoveriesWithSnapshot: planRuntimeTelemetry.recoveriesWithSnapshot.Load(),
		RecoveryTailEvents:     planRuntimeTelemetry.tailEvents.Load(),
		RecoveryTailBytes:      planRuntimeTelemetry.tailBytes.Load(),
		RecoverySnapshotAge:    time.Duration(planRuntimeTelemetry.snapshotAgeNanos.Load()),
		RecoveryDuration:       time.Duration(planRuntimeTelemetry.recoveryNanos.Load()),
	}
}

func observePlanRuntimeMutation(value planRuntimeMutationObservation) {
	planRuntimeTelemetry.mutations.Add(1)
	addPositive(&planRuntimeTelemetry.eventBytes, value.eventBytes)
	addPositive(&planRuntimeTelemetry.projectionBytes, value.projectionBytes)
	addPositive(&planRuntimeTelemetry.outboxBytes, value.outboxBytes)
	addPositive(&planRuntimeTelemetry.resultBytes, value.resultBytes)
	addPositive(&planRuntimeTelemetry.logicalBytes, value.logicalBytes)
	addPositive(&planRuntimeTelemetry.keys, value.keys)
	addPositive(&planRuntimeTelemetry.targets, value.targets)
	planRuntimeTelemetry.commitNanos.Add(uint64(value.commitDuration))
	planRuntimeTelemetry.mutationNanos.Add(uint64(value.totalDuration))
}

func observePlanRuntimeConflict() { planRuntimeTelemetry.conflicts.Add(1) }
func observePlanRuntimeReplay()   { planRuntimeTelemetry.replays.Add(1) }

func observePlanRuntimeRecovery(hasSnapshot bool, tailEvents, tailBytes int, snapshotAge, duration time.Duration) {
	planRuntimeTelemetry.recoveries.Add(1)
	if hasSnapshot {
		planRuntimeTelemetry.recoveriesWithSnapshot.Add(1)
		if snapshotAge > 0 {
			planRuntimeTelemetry.snapshotAgeNanos.Add(uint64(snapshotAge))
		}
	}
	addPositive(&planRuntimeTelemetry.tailEvents, tailEvents)
	addPositive(&planRuntimeTelemetry.tailBytes, tailBytes)
	planRuntimeTelemetry.recoveryNanos.Add(uint64(duration))
}

func addPositive(target *atomic.Uint64, value int) {
	if value > 0 {
		target.Add(uint64(value))
	}
}
