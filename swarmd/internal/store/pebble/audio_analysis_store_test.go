package pebblestore

import (
	"os"
	"path/filepath"
	"testing"
)

func registeredAnalysisSource(t *testing.T, sessions *SessionStore) AudioSourceRecord {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "analysis.wav")
	payload := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := sessions.PutAudioSourceRecord(AudioSourceRecord{AccountScopeID: "account", WorkspaceID: "workspace", RootPath: root, RelativePath: "analysis.wav", DisplayName: "analysis.wav", MIMEType: "audio/wav", SizeBytes: info.Size(), ModifiedAt: info.ModTime().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAudioAnalysisSnapshotIsBoundedScopedAndImmutable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "analysis.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	source := registeredAnalysisSource(t, sessions)
	snapshot := AudioAnalysisSnapshot{
		AccountScopeID: "account", WorkspaceID: "workspace", SourceRef: source.Ref,
		SourceFingerprint: source.SourceFingerprint, AnalyzerVersion: "swarm-dsp.v1", DurationMs: 2000,
		SampleIntervalMs: 100, CreatedAt: 100,
		Levels: []AudioAnalysisLevel{{StartMs: 0, EndMs: 100, RMS: .2, Peak: .8}, {StartMs: 100, EndMs: 200, RMS: .3, Peak: .9}},
		Onsets: []AudioAnalysisOnset{{TimeMs: 120, Strength: .9}},
		Tempo: &AudioAnalysisTempo{BPM: 120, Confidence: .8},
		Beats: []AudioAnalysisBeat{{TimeMs: 120, Confidence: .9, BarBeat: 1}, {TimeMs: 620, Confidence: .8, BarBeat: 2}},
		Sections: []AudioAnalysisSection{{StartMs: 0, EndMs: 2000, Label: "intro", Confidence: .7}},
	}
	stored, replayed, err := sessions.PutAudioAnalysisSnapshot(snapshot)
	if err != nil || replayed || stored.Ref == "" || stored.ContentDigest == "" {
		t.Fatalf("put=%+v replayed=%v err=%v", stored, replayed, err)
	}
	loaded, ok, err := sessions.GetAudioAnalysisSnapshot("account", "workspace", snapshot.SourceFingerprint, snapshot.AnalyzerVersion)
	if err != nil || !ok || loaded.ContentDigest != stored.ContentDigest || len(loaded.Beats) != 2 {
		t.Fatalf("get=%+v ok=%v err=%v", loaded, ok, err)
	}
	if _, ok, err := sessions.GetAudioAnalysisSnapshot("account", "other", snapshot.SourceFingerprint, snapshot.AnalyzerVersion); err != nil || ok {
		t.Fatalf("cross workspace ok=%v err=%v", ok, err)
	}
	if _, replayed, err := sessions.PutAudioAnalysisSnapshot(snapshot); err != nil || !replayed {
		t.Fatalf("replay=%v err=%v", replayed, err)
	}
	snapshot.Beats[0].TimeMs++
	if _, _, err := sessions.PutAudioAnalysisSnapshot(snapshot); err == nil {
		t.Fatal("expected immutable analysis collision")
	}
}

func TestAudioAnalysisRejectsUnboundedOrInvalidModelTiming(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "analysis.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := NewSessionStore(store)
	source := registeredAnalysisSource(t, sessions)
	base := AudioAnalysisSnapshot{AccountScopeID: "account", WorkspaceID: "workspace", SourceRef: source.Ref, SourceFingerprint: source.SourceFingerprint, AnalyzerVersion: "swarm-dsp.v1", DurationMs: 1000, SampleIntervalMs: 100, CreatedAt: 1, Levels: []AudioAnalysisLevel{{StartMs: 0, EndMs: 100, RMS: .2, Peak: .8}}}
	base.Beats = []AudioAnalysisBeat{{TimeMs: 1001, Confidence: .9}}
	if _, _, err := sessions.PutAudioAnalysisSnapshot(base); err == nil {
		t.Fatal("expected out-of-range beat rejection")
	}
	base.Beats = nil
	base.Levels = make([]AudioAnalysisLevel, maxAudioAnalysisLevels+1)
	if _, _, err := sessions.PutAudioAnalysisSnapshot(base); err == nil {
		t.Fatal("expected unbounded levels rejection")
	}
}
