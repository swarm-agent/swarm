package pebblestore

import (
	"context"
	"errors"
	"time"
)

// PebbleStorageMetrics is a payload-safe numerical view of Pebble's current
// physical state. It deliberately excludes paths, keys, values, and options.
type PebbleStorageMetrics struct {
	DiskSpaceBytes          uint64 `json:"disk_space_bytes"`
	LiveSSTableBytes        uint64 `json:"live_sstable_bytes"`
	CompactionDebtBytes     uint64 `json:"compaction_debt_bytes"`
	CompactionsInProgress   int64  `json:"compactions_in_progress"`
	CompactionBytesInFlight int64  `json:"compaction_bytes_in_flight"`
	ObsoleteTableCount      int64  `json:"obsolete_table_count"`
	ObsoleteTableBytes      uint64 `json:"obsolete_table_bytes"`
	ZombieTableCount        int64  `json:"zombie_table_count"`
	ZombieTableBytes        uint64 `json:"zombie_table_bytes"`
	ZombieMemtableCount     int64  `json:"zombie_memtable_count"`
	ZombieMemtableBytes     uint64 `json:"zombie_memtable_bytes"`
	ObsoleteWALCount        int64  `json:"obsolete_wal_count"`
	ObsoleteWALBytes        uint64 `json:"obsolete_wal_bytes"`
}

type SessionStorageMaintenanceSnapshot struct {
	Namespaces                SessionStorageMeasurement `json:"namespaces"`
	Pebble                    PebbleStorageMetrics      `json:"pebble"`
	OldestRetainedEndpointSeq uint64                    `json:"oldest_retained_endpoint_seq,omitempty"`
}

type SessionStorageRetentionConfiguration struct {
	RealtimeReplayRetentionSeconds       int64  `json:"realtime_replay_retention_seconds"`
	CompletedIdempotencyRetentionSeconds int64  `json:"completed_idempotency_retention_seconds"`
	RealtimeMinimumRecords               uint64 `json:"realtime_minimum_records"`
	BatchRecords                         int    `json:"batch_records"`
	RealtimeCutoffUnixMs                 int64  `json:"realtime_cutoff_unix_ms"`
	CompletedIdempotencyCutoffUnixMs     int64  `json:"completed_idempotency_cutoff_unix_ms"`
}

type SessionStorageMaintenanceRequest struct {
	Apply                      bool
	Now                        time.Time
	RetentionPolicy            V3SessionRetentionPolicy
	RunSearchMigration         bool
	SearchMigrationMaxSessions int
}

type SessionStorageMaintenanceReport struct {
	Mode                 string                                  `json:"mode"`
	Retention            SessionStorageRetentionConfiguration    `json:"retention"`
	CandidateCleanup     V3SessionMaintenanceResult              `json:"candidate_cleanup"`
	AppliedCleanup       *V3SessionMaintenanceResult             `json:"applied_cleanup,omitempty"`
	SearchMigrationState *V3SessionSearchMigrationState          `json:"search_migration_state,omitempty"`
	SearchMigration      *V3SessionSearchMigrationResult         `json:"search_migration,omitempty"`
	Before               SessionStorageMaintenanceSnapshot       `json:"before"`
	After                SessionStorageMaintenanceSnapshot       `json:"after"`
	Exclusions           []LegacySessionNamespaceCleanupDecision `json:"exclusions"`
	PhysicalCompaction   struct {
		Performed                      bool `json:"performed"`
		RequiresExplicitOperatorAction bool `json:"requires_explicit_operator_action"`
	} `json:"physical_compaction"`
}

// RunSessionStorageMaintenance is the dedicated orchestration surface for
// previewing or applying one bounded logical-maintenance unit. It never invokes
// Pebble compaction. Dry-run uses the same retention scanner and staging logic
// as apply, but discards the batch and writes no progress state.
func (s *SessionStore) RunSessionStorageMaintenance(ctx context.Context, request SessionStorageMaintenanceRequest) (SessionStorageMaintenanceReport, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return SessionStorageMaintenanceReport{}, errors.New("session store is not configured")
	}
	if err := contextError(ctx); err != nil {
		return SessionStorageMaintenanceReport{}, err
	}
	policy := request.RetentionPolicy
	if policy == (V3SessionRetentionPolicy{}) {
		policy = DefaultV3SessionRetentionPolicy()
	}
	if err := policy.Validate(); err != nil {
		return SessionStorageMaintenanceReport{}, err
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	if request.SearchMigrationMaxSessions <= 0 {
		request.SearchMigrationMaxSessions = 10
	}

	report := SessionStorageMaintenanceReport{Mode: "dry_run", Exclusions: LegacySessionNamespaceCleanupAudit()}
	report.PhysicalCompaction.RequiresExplicitOperatorAction = true
	report.Retention = SessionStorageRetentionConfiguration{
		RealtimeReplayRetentionSeconds:       int64(policy.RealtimeReplayRetention / time.Second),
		CompletedIdempotencyRetentionSeconds: int64(policy.CompletedIdempotencyRetention / time.Second),
		RealtimeMinimumRecords:               policy.RealtimeMinimumRecords,
		BatchRecords:                         policy.BatchRecords,
		RealtimeCutoffUnixMs:                 now.Add(-policy.RealtimeReplayRetention).UnixMilli(),
		CompletedIdempotencyCutoffUnixMs:     now.Add(-policy.CompletedIdempotencyRetention).UnixMilli(),
	}
	var err error
	report.Before, err = s.sessionStorageMaintenanceSnapshot(ctx)
	if err != nil {
		return SessionStorageMaintenanceReport{}, err
	}
	state, ok, err := s.GetV3SessionSearchMigrationState()
	if err != nil {
		return SessionStorageMaintenanceReport{}, err
	}
	if ok {
		report.SearchMigrationState = &state
	}
	report.CandidateCleanup, err = s.PreviewV3SessionRetentionPass(ctx, now, policy)
	if err != nil {
		return SessionStorageMaintenanceReport{}, err
	}
	if !request.Apply {
		report.After = report.Before
		return report, nil
	}

	report.Mode = "apply"
	applied, err := s.RunV3SessionRetentionPass(ctx, now, policy)
	if err != nil {
		return report, err
	}
	report.AppliedCleanup = &applied
	if request.RunSearchMigration {
		migration, migrationErr := s.RunV3SessionSearchMigrationPass(ctx, now, request.SearchMigrationMaxSessions)
		if migrationErr != nil {
			return report, migrationErr
		}
		report.SearchMigration = &migration
		state, ok, stateErr := s.GetV3SessionSearchMigrationState()
		if stateErr != nil {
			return report, stateErr
		}
		if ok {
			report.SearchMigrationState = &state
		}
	}
	report.After, err = s.sessionStorageMaintenanceSnapshot(ctx)
	if err != nil {
		return report, err
	}
	return report, nil
}

func (s *SessionStore) sessionStorageMaintenanceSnapshot(ctx context.Context) (SessionStorageMaintenanceSnapshot, error) {
	measurement, err := s.store.MeasureSessionStorageNamespaces(ctx, nil)
	if err != nil {
		return SessionStorageMaintenanceSnapshot{}, err
	}
	metrics := s.store.db.Metrics()
	var liveSSTableBytes uint64
	for _, level := range metrics.Levels {
		if level.Size > 0 {
			liveSSTableBytes += uint64(level.Size)
		}
	}
	boundary, err := s.OldestRetainedV3RealtimeEndpointSeq()
	if err != nil {
		return SessionStorageMaintenanceSnapshot{}, err
	}
	return SessionStorageMaintenanceSnapshot{
		Namespaces:                measurement,
		OldestRetainedEndpointSeq: boundary,
		Pebble: PebbleStorageMetrics{
			DiskSpaceBytes:          metrics.DiskSpaceUsage(),
			LiveSSTableBytes:        liveSSTableBytes,
			CompactionDebtBytes:     metrics.Compact.EstimatedDebt,
			CompactionsInProgress:   metrics.Compact.NumInProgress,
			CompactionBytesInFlight: metrics.Compact.InProgressBytes,
			ObsoleteTableCount:      metrics.Table.ObsoleteCount,
			ObsoleteTableBytes:      metrics.Table.ObsoleteSize,
			ZombieTableCount:        metrics.Table.ZombieCount,
			ZombieTableBytes:        metrics.Table.ZombieSize,
			ZombieMemtableCount:     metrics.MemTable.ZombieCount,
			ZombieMemtableBytes:     metrics.MemTable.ZombieSize,
			ObsoleteWALCount:        metrics.WAL.ObsoleteFiles,
			ObsoleteWALBytes:        metrics.WAL.ObsoletePhysicalSize,
		},
	}, nil
}
