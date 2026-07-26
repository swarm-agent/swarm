package longsessiondiag

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDisabledRecorderCreatesNoArtifacts(t *testing.T) {
	root := t.TempDir()
	recorder, err := Start(Options{Enabled: false, LogsRoot: root})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if recorder != nil {
		t.Fatalf("disabled recorder = %#v, want nil", recorder)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("disabled recorder created %d artifacts", len(entries))
	}
}

func TestRecorderCreatesPrivateMetadataOnlyArtifactsAndFindings(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "db")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatalf("MkdirAll database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(db, "data.sst"), []byte("storage-pressure"), 0o600); err != nil {
		t.Fatalf("WriteFile database: %v", err)
	}
	recorder, err := Start(Options{
		Enabled: true, LogsRoot: root, DatabasePath: db,
		SampleInterval: time.Hour, ProfileInterval: time.Hour,
		CPUProfileInterval: time.Hour, DiskBudgetBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	dir := recorder.Directory()
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat run dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %o, want 700", info.Mode().Perm())
	}
	for _, name := range []string{"manifest.json", "samples.jsonl", "latest-findings.json"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if strings.Contains(string(data), db) || strings.Contains(string(data), "storage-pressure") {
			t.Fatalf("%s leaked a path or file content", name)
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", name, err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, fileInfo.Mode().Perm())
		}
	}
	findings, err := os.ReadFile(filepath.Join(dir, "latest-findings.json"))
	if err != nil {
		t.Fatalf("ReadFile findings: %v", err)
	}
	if !strings.Contains(string(findings), "profile-") || !strings.Contains(string(findings), "BaselineAt") && !strings.Contains(string(findings), "baseline_at") {
		t.Fatalf("findings do not link baseline evidence and profiles: %s", findings)
	}
}

func TestRecorderRecordsBoundedSubsystemAndHashedOperationMetadata(t *testing.T) {
	recorder, err := Start(Options{
		Enabled: true, LogsRoot: t.TempDir(), SampleInterval: time.Hour,
		ProfileInterval: time.Hour, CPUProfileInterval: time.Hour, DiskBudgetBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	recorder.RegisterSnapshotProvider("queues", func() map[string]any {
		return map[string]any{"pending": 3}
	})
	first := recorder.HashIdentifier("private-session-id")
	second := recorder.HashIdentifier("private-session-id")
	if first == "" || first != second || first == "private-session-id" {
		t.Fatalf("HashIdentifier = %q, %q", first, second)
	}
	recorder.ObserveOperation("provider.total", "private-session-id", 15*time.Millisecond, false, map[string]int64{"input_items": 4, "invalid label": 9}, map[string]bool{"continuation": true})
	if err := recorder.captureSample(); err != nil {
		t.Fatalf("captureSample: %v", err)
	}
	dir := recorder.Directory()
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	samples, err := os.ReadFile(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile samples: %v", err)
	}
	if !strings.Contains(string(samples), `"queues":{"pending":3}`) || !strings.Contains(string(samples), `"provider.total"`) {
		t.Fatalf("samples missing registered metadata: %s", samples)
	}
	operations, err := os.ReadFile(filepath.Join(dir, "operations.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile operations: %v", err)
	}
	if strings.Contains(string(operations), "private-session-id") || strings.Contains(string(operations), "invalid label") || !strings.Contains(string(operations), first) {
		t.Fatalf("operation metadata is not bounded and pseudonymized: %s", operations)
	}
}

func TestRecorderDesktopSamplesAreBoundedPrivateAndIncludedInFindings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recorder, err := Start(Options{
		Enabled: true, LogsRoot: t.TempDir(), SampleInterval: time.Hour,
		ProfileInterval: time.Hour, CPUProfileInterval: time.Hour, DiskBudgetBytes: 64 << 20,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recorder.Close() }()
	first := DesktopSample{BrowserMemoryAvailable: true, BrowserMemoryBytes: 100, V3CacheEstimatedBytes: 200, DOMNodes: 10,
		V3Sections: map[string]uint64{"messages": 2}, LargestSessions: []DesktopSessionSample{{SessionHash: "0123456789abcdef", EstimatedBytes: 100}}}
	if err := recorder.RecordDesktopSample(first); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordDesktopSample(first); !errors.Is(err, ErrDesktopSampleTooFrequent) {
		t.Fatalf("rate limit error=%v", err)
	}
	now = now.Add(30 * time.Second)
	second := first
	second.BrowserMemoryBytes = 300
	second.V3CacheEstimatedBytes = 700
	second.DOMNodes = 30
	if err := recorder.RecordDesktopSample(second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(recorder.Directory(), "desktop-samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "session-id") || strings.Count(string(data), "0123456789abcdef") != 2 {
		t.Fatalf("desktop samples leaked identifiers or lost hashes: %s", data)
	}
	findings, err := os.ReadFile(filepath.Join(recorder.Directory(), "latest-findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []string{"desktop.browser_memory_growth", "desktop.v3_cache_growth", "desktop.dom_growth"} {
		if !strings.Contains(string(findings), signal) {
			t.Fatalf("findings missing %s: %s", signal, findings)
		}
	}
	invalid := DesktopSample{LargestSessions: []DesktopSessionSample{{SessionHash: "raw-session-id"}}}
	if err := recorder.RecordDesktopSample(invalid); !errors.Is(err, ErrInvalidDesktopSample) {
		t.Fatalf("invalid hash error=%v", err)
	}
}

func TestRecorderEnforcesDiskBudget(t *testing.T) {
	recorder, err := Start(Options{
		Enabled: true, LogsRoot: t.TempDir(), SampleInterval: time.Hour,
		ProfileInterval: time.Hour, CPUProfileInterval: time.Hour,
		DiskBudgetBytes: 1,
	})
	if err == nil {
		_ = recorder.Close()
		t.Fatalf("Start succeeded with an unusable disk budget")
	}
	if !strings.Contains(err.Error(), "disk budget exhausted") {
		t.Fatalf("Start error = %v, want disk budget error", err)
	}
}
