// Package longsessiondiag records bounded, metadata-only evidence for diagnosing
// memory growth and lag during long daemon sessions. It deliberately exposes no
// network listener and never records request, session, tool, or workspace data.
package longsessiondiag

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	runtimemetrics "runtime/metrics"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

const (
	DefaultSampleInterval     = 30 * time.Second
	DefaultProfileInterval    = 15 * time.Minute
	DefaultCPUProfileInterval = 30 * time.Minute
	DefaultCPUProfileDuration = 10 * time.Second
	DefaultDiskBudgetBytes    = int64(512 << 20)
	DefaultMaxProfileBytes    = int64(32 << 20)
)

type Options struct {
	Enabled            bool
	LogsRoot           string
	DatabasePath       string
	SampleInterval     time.Duration
	ProfileInterval    time.Duration
	CPUProfileInterval time.Duration
	CPUProfileDuration time.Duration
	DiskBudgetBytes    int64
	MaxProfileBytes    int64
	Now                func() time.Time
}

type Recorder struct {
	mu                      sync.Mutex
	opts                    Options
	dir                     string
	started                 time.Time
	hashSalt                [16]byte
	baseline                *Sample
	latest                  *Sample
	profiles                []ProfileArtifact
	snapshotProviders       map[string]SnapshotProvider
	latency                 map[string]*latencyAggregate
	lastErr                 error
	cancel                  context.CancelFunc
	wg                      sync.WaitGroup
	closed                  bool
	lastDesktopSampleAt     time.Time
	desktopBaseline         *DesktopSample
	desktopLatest           *DesktopSample
	oldMutexProfileFraction int
}

type Manifest struct {
	SchemaVersion     int        `json:"schema_version"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	SampleIntervalMS  int64      `json:"sample_interval_ms"`
	ProfileIntervalMS int64      `json:"profile_interval_ms"`
	DiskBudgetBytes   int64      `json:"disk_budget_bytes"`
	ContentPolicy     string     `json:"content_policy"`
}

type Sample struct {
	Timestamp             time.Time                  `json:"timestamp"`
	GoHeapAllocBytes      uint64                     `json:"go_heap_alloc_bytes"`
	GoHeapInuseBytes      uint64                     `json:"go_heap_inuse_bytes"`
	GoHeapObjects         uint64                     `json:"go_heap_objects"`
	GoTotalAllocBytes     uint64                     `json:"go_total_alloc_bytes"`
	GoSysBytes            uint64                     `json:"go_sys_bytes"`
	GoNumGC               uint32                     `json:"go_num_gc"`
	Goroutines            int                        `json:"goroutines"`
	ProcessRSSBytes       uint64                     `json:"process_rss_bytes,omitempty"`
	ProcessHighWaterBytes uint64                     `json:"process_high_water_bytes,omitempty"`
	DatabaseBytes         int64                      `json:"database_bytes,omitempty"`
	DatabaseFiles         int64                      `json:"database_files,omitempty"`
	RuntimeMetrics        map[string]float64         `json:"runtime_metrics,omitempty"`
	Subsystems            map[string]any             `json:"subsystems,omitempty"`
	Latency               map[string]LatencySnapshot `json:"latency,omitempty"`
}

// SnapshotProvider returns a bounded metadata-only snapshot. Providers must not
// return prompts, messages, headers, credentials, URLs, or paths.
type SnapshotProvider func() map[string]any

type LatencySnapshot struct {
	Count      uint64 `json:"count"`
	TotalMS    int64  `json:"total_ms"`
	MaxMS      int64  `json:"max_ms"`
	ErrorCount uint64 `json:"error_count,omitempty"`
}

type latencyAggregate struct {
	count, errors uint64
	total, max    time.Duration
}

var (
	ErrInvalidDesktopSample     = errors.New("invalid desktop diagnostic sample")
	ErrDesktopSampleTooFrequent = errors.New("desktop diagnostic sample received too frequently")
)

const desktopSampleMinimumInterval = 5 * time.Second

// DesktopSample is a bounded metadata-only browser snapshot. Session hashes are
// created in the browser for ranking only and cannot be joined to daemon IDs.
type DesktopSample struct {
	TimestampMS                          int64                  `json:"timestamp_ms"`
	JSHeapAvailable                      bool                   `json:"js_heap_available"`
	JSHeapUsedBytes                      uint64                 `json:"js_heap_used_bytes,omitempty"`
	JSHeapPeakUsedBytes                  uint64                 `json:"js_heap_peak_used_bytes,omitempty"`
	JSHeapTotalBytes                     uint64                 `json:"js_heap_total_bytes,omitempty"`
	JSHeapLimitBytes                     uint64                 `json:"js_heap_limit_bytes,omitempty"`
	EventLoopDriftMS                     float64                `json:"event_loop_drift_ms,omitempty"`
	LongTaskCount                        uint64                 `json:"long_task_count,omitempty"`
	LongTaskDurationMS                   float64                `json:"long_task_duration_ms,omitempty"`
	LongAnimationFrameCount              uint64                 `json:"long_animation_frame_count,omitempty"`
	LongAnimationFrameDurationMS         float64                `json:"long_animation_frame_duration_ms,omitempty"`
	LongAnimationFrameBlockingDurationMS float64                `json:"long_animation_frame_blocking_duration_ms,omitempty"`
	DOMNodes                             uint64                 `json:"dom_nodes,omitempty"`
	CacheMutationCount                   uint64                 `json:"cache_mutation_count,omitempty"`
	CacheMutationDurationMS              float64                `json:"cache_mutation_duration_ms,omitempty"`
	CacheMutationMaxDurationMS           float64                `json:"cache_mutation_max_duration_ms,omitempty"`
	CacheActionCounts                    map[string]uint64      `json:"cache_action_counts,omitempty"`
	CacheActionDurationMS                map[string]float64     `json:"cache_action_duration_ms,omitempty"`
	CacheActionMaxDurationMS             map[string]float64     `json:"cache_action_max_duration_ms,omitempty"`
	DiagnosticsSampleDurationMS          float64                `json:"diagnostics_sample_duration_ms,omitempty"`
	QueryCacheEntries                    uint64                 `json:"query_cache_entries,omitempty"`
	QueryCacheEstimatedBytes             uint64                 `json:"query_cache_estimated_bytes,omitempty"`
	V3CacheEstimatedBytes                uint64                 `json:"v3_cache_estimated_bytes,omitempty"`
	V3Sections                           map[string]uint64      `json:"v3_sections,omitempty"`
	LargestSessions                      []DesktopSessionSample `json:"largest_sessions,omitempty"`
}

type DesktopSessionSample struct {
	SessionHash    string `json:"session_hash"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
	Messages       uint64 `json:"messages,omitempty"`
	Events         uint64 `json:"events,omitempty"`
}

type OperationObservation struct {
	Timestamp   time.Time        `json:"timestamp"`
	Operation   string           `json:"operation"`
	SessionHash string           `json:"session_hash,omitempty"`
	DurationMS  int64            `json:"duration_ms"`
	Failed      bool             `json:"failed,omitempty"`
	Dimensions  map[string]int64 `json:"dimensions,omitempty"`
	Flags       map[string]bool  `json:"flags,omitempty"`
}

type ProfileArtifact struct {
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	Artifact  string    `json:"artifact"`
	Bytes     int64     `json:"bytes"`
}

type Finding struct {
	Rank              int      `json:"rank"`
	Signal            string   `json:"signal"`
	Assessment        string   `json:"assessment"`
	Baseline          float64  `json:"baseline"`
	Current           float64  `json:"current"`
	Delta             float64  `json:"delta"`
	PercentChange     *float64 `json:"percent_change,omitempty"`
	EvidenceArtifacts []string `json:"evidence_artifacts,omitempty"`
}

type Findings struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	BaselineAt    time.Time         `json:"baseline_at"`
	CurrentAt     time.Time         `json:"current_at"`
	Findings      []Finding         `json:"findings"`
	Profiles      []ProfileArtifact `json:"profiles"`
	Caveat        string            `json:"caveat"`
}

var metricNames = []string{
	"/cpu/classes/gc/mark/assist:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/scavenge/total:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
	"/gc/heap/allocs:bytes",
	"/gc/heap/frees:bytes",
	"/gc/heap/live:bytes",
	"/gc/heap/objects:objects",
	"/gc/pauses:seconds",
	"/memory/classes/heap/stacks:bytes",
	"/memory/classes/metadata/other:bytes",
	"/memory/classes/total:bytes",
	"/sched/gomaxprocs:threads",
	"/sched/goroutines:goroutines",
	"/sched/latencies:seconds",
}

func Start(opts Options) (*Recorder, error) {
	if !opts.Enabled {
		return nil, nil
	}
	applyDefaults(&opts)
	logsRoot := strings.TrimSpace(opts.LogsRoot)
	if logsRoot == "" {
		var err error
		logsRoot, err = storagecontract.ResolveRoot(storagecontract.RootLogs, storagecontract.Options{})
		if err != nil {
			return nil, fmt.Errorf("resolve long-session diagnostics logs root: %w", err)
		}
	}
	base, err := storagecontract.Join(logsRoot, "long-session-diagnostics")
	if err != nil {
		return nil, fmt.Errorf("resolve long-session diagnostics directory: %w", err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("create long-session diagnostics directory: %w", err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return nil, fmt.Errorf("secure long-session diagnostics directory: %w", err)
	}
	dir, err := createRunDir(base, opts.Now())
	if err != nil {
		return nil, err
	}

	r := &Recorder{opts: opts, dir: dir, started: opts.Now(), snapshotProviders: make(map[string]SnapshotProvider), latency: make(map[string]*latencyAggregate)}
	if _, err := rand.Read(r.hashSalt[:]); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("initialize diagnostic identifier salt: %w", err)
	}
	r.oldMutexProfileFraction = runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1_000_000)
	manifest := r.manifest(nil)
	if err := r.writeJSON("manifest.json", manifest); err != nil {
		runtime.SetMutexProfileFraction(r.oldMutexProfileFraction)
		runtime.SetBlockProfileRate(0)
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("initialize long-session diagnostics manifest: %w", err)
	}
	if err := r.captureSample(); err != nil {
		runtime.SetMutexProfileFraction(r.oldMutexProfileFraction)
		runtime.SetBlockProfileRate(0)
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("initialize long-session diagnostics sample: %w", err)
	}
	if err := r.captureProfiles(); err != nil {
		runtime.SetMutexProfileFraction(r.oldMutexProfileFraction)
		runtime.SetBlockProfileRate(0)
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("initialize long-session diagnostics profiles: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.run(ctx)
	return r, nil
}

func (r *Recorder) Directory() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// HashIdentifier returns a run-local stable pseudonym suitable for correlating
// records without retaining the underlying identifier.
func (r *Recorder) HashIdentifier(value string) string {
	if r == nil || strings.TrimSpace(value) == "" {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write(r.hashSalt[:])
	_, _ = io.WriteString(h, strings.TrimSpace(value))
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// RegisterSnapshotProvider adds a bounded named subsystem source. Names are
// fixed by code so samples cannot gain unbounded labels.
func (r *Recorder) RegisterSnapshotProvider(name string, provider SnapshotProvider) {
	if r == nil || provider == nil || !validMetricName(name) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.snapshotProviders[name] = provider
	}
}

// ObserveLatency accumulates a fixed-label operation timing. It intentionally
// stores no request or response data.
func (r *Recorder) ObserveLatency(name string, duration time.Duration, failed bool) {
	if r == nil || !validMetricName(name) || duration < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	agg := r.latency[name]
	if agg == nil {
		agg = &latencyAggregate{}
		r.latency[name] = agg
	}
	agg.count++
	agg.total += duration
	if duration > agg.max {
		agg.max = duration
	}
	if failed {
		agg.errors++
	}
}

// ObserveOperation writes one bounded metadata-only operation record and
// updates its aggregate. Invalid labels are dropped rather than serialized.
func (r *Recorder) ObserveOperation(name, sessionID string, duration time.Duration, failed bool, dimensions map[string]int64, flags map[string]bool) {
	if r == nil || !validMetricName(name) || duration < 0 {
		return
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	r.ObserveLatency(name, duration, failed)
	observation := OperationObservation{Timestamp: r.opts.Now(), Operation: name, SessionHash: r.HashIdentifier(sessionID), DurationMS: duration.Milliseconds(), Failed: failed, Dimensions: boundedIntFields(dimensions), Flags: boundedBoolFields(flags)}
	payload, err := json.Marshal(observation)
	if err != nil {
		r.recordError(err)
		return
	}
	if err := r.appendFile("operations.jsonl", append(payload, '\n')); err != nil {
		r.recordError(err)
	}
}

func boundedIntFields(input map[string]int64) map[string]int64 {
	out := make(map[string]int64)
	for key, value := range input {
		if len(out) >= 32 {
			break
		}
		if validMetricName(key) {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundedBoolFields(input map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for key, value := range input {
		if len(out) >= 32 {
			break
		}
		if validMetricName(key) {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validMetricName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' && ch != '.' && ch != '-' {
			return false
		}
	}
	return true
}

// RecordDesktopSample validates and appends one browser snapshot without
// accepting arbitrary labels or conversation payloads.
func (r *Recorder) RecordDesktopSample(sample DesktopSample) error {
	if r == nil {
		return ErrInvalidDesktopSample
	}
	if err := validateDesktopSample(&sample); err != nil {
		return err
	}
	now := r.opts.Now()
	if sample.TimestampMS == 0 {
		sample.TimestampMS = now.UnixMilli()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("long-session diagnostics recorder closed")
	}
	if !r.lastDesktopSampleAt.IsZero() && now.Sub(r.lastDesktopSampleAt) < desktopSampleMinimumInterval {
		r.mu.Unlock()
		return ErrDesktopSampleTooFrequent
	}
	r.lastDesktopSampleAt = now
	r.mu.Unlock()
	payload, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	if err := r.appendFile("desktop-samples.jsonl", append(payload, '\n')); err != nil {
		return err
	}
	r.mu.Lock()
	if r.desktopBaseline == nil {
		copy := sample
		r.desktopBaseline = &copy
	}
	copy := sample
	r.desktopLatest = &copy
	r.mu.Unlock()
	return r.writeFindings()
}

func validateDesktopSample(sample *DesktopSample) error {
	if sample == nil || len(sample.V3Sections) > 64 || len(sample.CacheActionCounts) > 64 || len(sample.CacheActionDurationMS) > 64 || len(sample.CacheActionMaxDurationMS) > 64 || len(sample.LargestSessions) > 10 {
		return ErrInvalidDesktopSample
	}
	if sample.EventLoopDriftMS < 0 || sample.LongTaskDurationMS < 0 || sample.LongAnimationFrameDurationMS < 0 || sample.LongAnimationFrameBlockingDurationMS < 0 || sample.CacheMutationDurationMS < 0 || sample.CacheMutationMaxDurationMS < 0 || sample.DiagnosticsSampleDurationMS < 0 {
		return ErrInvalidDesktopSample
	}
	for _, labels := range []any{sample.V3Sections, sample.CacheActionCounts, sample.CacheActionDurationMS, sample.CacheActionMaxDurationMS} {
		switch values := labels.(type) {
		case map[string]uint64:
			for name := range values {
				if !validMetricName(name) {
					return ErrInvalidDesktopSample
				}
			}
		case map[string]float64:
			for name, value := range values {
				if !validMetricName(name) || value < 0 {
					return ErrInvalidDesktopSample
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(sample.LargestSessions))
	for _, session := range sample.LargestSessions {
		if len(session.SessionHash) != 16 {
			return ErrInvalidDesktopSample
		}
		for _, ch := range session.SessionHash {
			if !((ch >= 'a' && ch <= 'f') || (ch >= '0' && ch <= '9')) {
				return ErrInvalidDesktopSample
			}
		}
		if _, exists := seen[session.SessionHash]; exists {
			return ErrInvalidDesktopSample
		}
		seen[session.SessionHash] = struct{}{}
	}
	return nil
}

func (r *Recorder) LastError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		err := r.lastErr
		r.mu.Unlock()
		return err
	}
	r.closed = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
	runtime.SetMutexProfileFraction(r.oldMutexProfileFraction)
	runtime.SetBlockProfileRate(0)
	if err := r.captureSample(); err != nil {
		r.recordError(err)
	}
	ended := r.opts.Now()
	if err := r.writeJSON("manifest.json", r.manifest(&ended)); err != nil {
		r.recordError(err)
	}
	return r.LastError()
}

func (r *Recorder) run(ctx context.Context) {
	defer r.wg.Done()
	sampleTicker := time.NewTicker(r.opts.SampleInterval)
	profileTicker := time.NewTicker(r.opts.ProfileInterval)
	cpuTicker := time.NewTicker(r.opts.CPUProfileInterval)
	defer sampleTicker.Stop()
	defer profileTicker.Stop()
	defer cpuTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTicker.C:
			if err := r.captureSample(); err != nil {
				r.recordError(err)
				return
			}
		case <-profileTicker.C:
			if err := r.captureProfiles(); err != nil {
				r.recordError(err)
				return
			}
		case <-cpuTicker.C:
			if err := r.captureCPUProfile(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.recordError(err)
				return
			}
		}
	}
}

func applyDefaults(opts *Options) {
	if opts.SampleInterval <= 0 {
		opts.SampleInterval = DefaultSampleInterval
	}
	if opts.ProfileInterval <= 0 {
		opts.ProfileInterval = DefaultProfileInterval
	}
	if opts.CPUProfileInterval <= 0 {
		opts.CPUProfileInterval = DefaultCPUProfileInterval
	}
	if opts.CPUProfileDuration <= 0 {
		opts.CPUProfileDuration = DefaultCPUProfileDuration
	}
	if opts.DiskBudgetBytes <= 0 {
		opts.DiskBudgetBytes = DefaultDiskBudgetBytes
	}
	if opts.MaxProfileBytes <= 0 {
		opts.MaxProfileBytes = DefaultMaxProfileBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
}

func createRunDir(base string, now time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("create diagnostic run identifier: %w", err)
	}
	name := "run-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:])
	dir := filepath.Join(base, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create long-session diagnostics run directory: %w", err)
	}
	return dir, nil
}

func (r *Recorder) manifest(ended *time.Time) Manifest {
	return Manifest{SchemaVersion: 1, StartedAt: r.started, EndedAt: ended,
		SampleIntervalMS: r.opts.SampleInterval.Milliseconds(), ProfileIntervalMS: r.opts.ProfileInterval.Milliseconds(),
		DiskBudgetBytes: r.opts.DiskBudgetBytes,
		ContentPolicy:   "metadata-only; no prompts, messages, tool output, credentials, headers, session identifiers, or workspace paths"}
}

func (r *Recorder) captureSample() error {
	sample := collectSample(r.opts.Now(), r.opts.DatabasePath)
	providers, latency := r.snapshotSources()
	if len(providers) > 0 {
		sample.Subsystems = make(map[string]any, len(providers))
		for name, provider := range providers {
			func() {
				defer func() { _ = recover() }()
				if snapshot := provider(); len(snapshot) > 0 {
					sample.Subsystems[name] = snapshot
				}
			}()
		}
	}
	sample.Latency = latency
	payload, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := r.appendFile("samples.jsonl", payload); err != nil {
		return err
	}
	r.mu.Lock()
	if r.baseline == nil {
		copy := sample
		r.baseline = &copy
	}
	copy := sample
	r.latest = &copy
	r.mu.Unlock()
	return r.writeFindings()
}

func (r *Recorder) snapshotSources() (map[string]SnapshotProvider, map[string]LatencySnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	providers := make(map[string]SnapshotProvider, len(r.snapshotProviders))
	for name, provider := range r.snapshotProviders {
		providers[name] = provider
	}
	latency := make(map[string]LatencySnapshot, len(r.latency))
	for name, agg := range r.latency {
		if agg == nil {
			continue
		}
		latency[name] = LatencySnapshot{Count: agg.count, TotalMS: agg.total.Milliseconds(), MaxMS: agg.max.Milliseconds(), ErrorCount: agg.errors}
	}
	return providers, latency
}

func collectSample(now time.Time, databasePath string) Sample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss, hwm := readProcessMemory()
	dbBytes, dbFiles := directoryStats(databasePath)
	return Sample{Timestamp: now, GoHeapAllocBytes: ms.HeapAlloc, GoHeapInuseBytes: ms.HeapInuse,
		GoHeapObjects: ms.HeapObjects, GoTotalAllocBytes: ms.TotalAlloc, GoSysBytes: ms.Sys, GoNumGC: ms.NumGC,
		Goroutines: runtime.NumGoroutine(), ProcessRSSBytes: rss, ProcessHighWaterBytes: hwm,
		DatabaseBytes: dbBytes, DatabaseFiles: dbFiles, RuntimeMetrics: readRuntimeMetrics()}
}

func readRuntimeMetrics() map[string]float64 {
	samples := make([]runtimemetrics.Sample, len(metricNames))
	for i, name := range metricNames {
		samples[i].Name = name
	}
	runtimemetrics.Read(samples)
	out := make(map[string]float64, len(samples))
	for _, sample := range samples {
		switch sample.Value.Kind() {
		case runtimemetrics.KindUint64:
			out[sample.Name] = float64(sample.Value.Uint64())
		case runtimemetrics.KindFloat64:
			out[sample.Name] = sample.Value.Float64()
		case runtimemetrics.KindFloat64Histogram:
			h := sample.Value.Float64Histogram()
			var count uint64
			for _, n := range h.Counts {
				count += n
			}
			out[sample.Name+"#count"] = float64(count)
		}
	}
	return out
}

func readProcessMemory() (rss, highWater uint64) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "VmRSS":
			rss = value * 1024
		case "VmHWM":
			highWater = value * 1024
		}
	}
	return rss, highWater
}

func directoryStats(path string) (bytes, files int64) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, 0
	}
	root := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		root = filepath.Dir(path)
	}
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return bytes, files
}

func (r *Recorder) captureProfiles() error {
	for _, name := range []string{"heap", "allocs", "goroutine", "block", "mutex"} {
		profile := pprof.Lookup(name)
		if profile == nil {
			continue
		}
		buf := newCappedBuffer(r.profileLimit())
		if err := profile.WriteTo(buf, 0); err != nil {
			return fmt.Errorf("capture %s profile: %w", name, err)
		}
		if err := r.storeProfile(name, buf.Bytes()); err != nil {
			return err
		}
	}
	return r.writeFindings()
}

func (r *Recorder) captureCPUProfile(ctx context.Context) error {
	buf := newCappedBuffer(r.profileLimit())
	if err := pprof.StartCPUProfile(buf); err != nil {
		return fmt.Errorf("start cpu profile: %w", err)
	}
	timer := time.NewTimer(r.opts.CPUProfileDuration)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
		pprof.StopCPUProfile()
		return ctx.Err()
	case <-timer.C:
		pprof.StopCPUProfile()
	}
	if buf.Err() != nil {
		return fmt.Errorf("capture cpu profile: %w", buf.Err())
	}
	if err := r.storeProfile("cpu", buf.Bytes()); err != nil {
		return err
	}
	return r.writeFindings()
}

func (r *Recorder) profileLimit() int64 {
	limit := r.opts.MaxProfileBytes
	if limit > r.opts.DiskBudgetBytes/4 {
		limit = r.opts.DiskBudgetBytes / 4
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

func (r *Recorder) storeProfile(kind string, payload []byte) error {
	stamp := r.opts.Now()
	name := "profile-" + stamp.UTC().Format("20060102T150405.000000000Z") + "-" + kind + ".pprof"
	if err := r.writeFile(name, payload); err != nil {
		return fmt.Errorf("store %s profile: %w", kind, err)
	}
	r.mu.Lock()
	r.profiles = append(r.profiles, ProfileArtifact{Timestamp: stamp, Kind: kind, Artifact: name, Bytes: int64(len(payload))})
	if len(r.profiles) > 64 {
		r.profiles = append([]ProfileArtifact(nil), r.profiles[len(r.profiles)-64:]...)
	}
	r.mu.Unlock()
	return nil
}

func (r *Recorder) writeFindings() error {
	r.mu.Lock()
	if r.baseline == nil || r.latest == nil {
		r.mu.Unlock()
		return nil
	}
	baseline, current := *r.baseline, *r.latest
	profiles := append([]ProfileArtifact(nil), r.profiles...)
	var desktopBaseline, desktopCurrent *DesktopSample
	if r.desktopBaseline != nil && r.desktopLatest != nil {
		baselineCopy, currentCopy := *r.desktopBaseline, *r.desktopLatest
		desktopBaseline, desktopCurrent = &baselineCopy, &currentCopy
	}
	r.mu.Unlock()
	artifacts := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		artifacts = append(artifacts, profile.Artifact)
	}
	findings := []Finding{
		growthFinding("daemon.go_live_heap_growth", float64(baseline.GoHeapAllocBytes), float64(current.GoHeapAllocBytes), artifacts),
		growthFinding("daemon.process_rss_growth", float64(baseline.ProcessRSSBytes), float64(current.ProcessRSSBytes), artifacts),
		growthFinding("daemon.goroutine_growth", float64(baseline.Goroutines), float64(current.Goroutines), artifacts),
		growthFinding("storage.pebble_growth", float64(baseline.DatabaseBytes), float64(current.DatabaseBytes), artifacts),
	}
	findings = append(findings, subsystemFindings(baseline, current, artifacts)...)
	if desktopBaseline != nil && desktopCurrent != nil {
		findings = append(findings,
			growthFinding("desktop.js_heap_growth", float64(desktopBaseline.JSHeapUsedBytes), float64(desktopCurrent.JSHeapUsedBytes), []string{"desktop-samples.jsonl"}),
			growthFinding("desktop.v3_cache_growth", float64(desktopBaseline.V3CacheEstimatedBytes), float64(desktopCurrent.V3CacheEstimatedBytes), []string{"desktop-samples.jsonl"}),
			growthFinding("desktop.query_cache_growth", float64(desktopBaseline.QueryCacheEstimatedBytes), float64(desktopCurrent.QueryCacheEstimatedBytes), []string{"desktop-samples.jsonl"}),
			growthFinding("desktop.dom_growth", float64(desktopBaseline.DOMNodes), float64(desktopCurrent.DOMNodes), []string{"desktop-samples.jsonl"}),
			growthFinding("desktop.event_loop_drift", desktopBaseline.EventLoopDriftMS, desktopCurrent.EventLoopDriftMS, []string{"desktop-samples.jsonl"}),
		)
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Delta > findings[j].Delta })
	for i := range findings {
		findings[i].Rank = i + 1
	}
	return r.writeJSON("latest-findings.json", Findings{SchemaVersion: 1, GeneratedAt: r.opts.Now(), BaselineAt: baseline.Timestamp,
		CurrentAt: current.Timestamp, Findings: findings, Profiles: profiles,
		Caveat: "Ranked correlations are evidence for investigation, not proof of causation."})
}

func subsystemFindings(baseline, current Sample, artifacts []string) []Finding {
	paths := []struct {
		signal string
		path   []string
	}{
		{"codex.retained_request_growth", []string{"codex", "request_properties_bytes"}},
		{"codex.retained_output_growth", []string{"codex", "last_output_bytes"}},
		{"realtime.queue_growth", []string{"api", "realtime_outbox", "pending_records"}},
		{"realtime.live_patch_bytes_growth", []string{"api", "live_patch", "pending_bytes"}},
		{"provider.api_latency_growth", []string{"latency", "codex.api_total", "max_ms"}},
		{"provider.first_event_latency_growth", []string{"latency", "v3.provider_first_event", "max_ms"}},
	}
	out := make([]Finding, 0, len(paths))
	for _, item := range paths {
		before, beforeOK := numericSamplePath(baseline, item.path)
		after, afterOK := numericSamplePath(current, item.path)
		if beforeOK || afterOK {
			out = append(out, growthFinding(item.signal, before, after, artifacts))
		}
	}
	return out
}

func numericSamplePath(sample Sample, path []string) (float64, bool) {
	var value any
	if len(path) > 0 && path[0] == "latency" {
		if len(path) != 3 {
			return 0, false
		}
		snapshot, ok := sample.Latency[path[1]]
		if !ok {
			return 0, false
		}
		switch path[2] {
		case "total_ms":
			return float64(snapshot.TotalMS), true
		case "max_ms":
			return float64(snapshot.MaxMS), true
		}
		return 0, false
	}
	value = sample.Subsystems
	for _, part := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return 0, false
		}
		value, ok = object[part]
		if !ok {
			return 0, false
		}
	}
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint64:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

func growthFinding(signal string, baseline, current float64, artifacts []string) Finding {
	delta := current - baseline
	assessment := "stable_or_lower"
	if delta > 0 {
		assessment = "increased"
	}
	finding := Finding{Signal: signal, Assessment: assessment, Baseline: baseline, Current: current, Delta: delta, EvidenceArtifacts: artifacts}
	if baseline > 0 {
		percent := delta / baseline * 100
		finding.PercentChange = &percent
	}
	return finding
}

func (r *Recorder) writeJSON(name string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return r.writeFile(name, payload)
}

func (r *Recorder) appendFile(name string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkBudgetLocked("", int64(len(payload))); err != nil {
		return err
	}
	path := filepath.Join(r.dir, filepath.Base(name))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func (r *Recorder) writeFile(name string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := filepath.Join(r.dir, filepath.Base(name))
	if err := r.checkBudgetLocked(path, int64(len(payload))); err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (r *Recorder) checkBudgetLocked(replacing string, incoming int64) error {
	used, _ := directoryStats(r.dir)
	if replacing != "" {
		if info, err := os.Stat(replacing); err == nil {
			used -= info.Size()
		}
	}
	if incoming < 0 || used+incoming > r.opts.DiskBudgetBytes {
		return fmt.Errorf("long-session diagnostics disk budget exhausted: used=%d incoming=%d budget=%d", used, incoming, r.opts.DiskBudgetBytes)
	}
	return nil
}

func (r *Recorder) recordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.lastErr = errors.Join(r.lastErr, err)
	r.mu.Unlock()
}

type cappedBuffer struct {
	buf bytes.Buffer
	max int64
	err error
}

func newCappedBuffer(max int64) *cappedBuffer { return &cappedBuffer{max: max} }
func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if int64(b.buf.Len()+len(p)) > b.max {
		b.err = fmt.Errorf("profile exceeds %d-byte artifact limit", b.max)
		return 0, b.err
	}
	return b.buf.Write(p)
}
func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }
func (b *cappedBuffer) Err() error    { return b.err }

var _ io.Writer = (*cappedBuffer)(nil)
