package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the outbox (docs/checkpoints/outbox.md) is how a machine's feed
// and job results reach storage the owner chose. Requirement: it is off
// unless configured and refuses bad settings; it writes the documented
// layout in order (blobs, then the result manifest, then the log record);
// every object is created once and a retry never changes or duplicates it;
// it resumes from a saved cursor; a result that keeps failing does not stop
// the log; and on a bucket it signs requests and asks for create-only.
// Threats: a crash between writes leaving a record that points at missing
// files, a retry overwriting what a reader already saw, credentials leaking
// into status, and a stuck job blocking every later record. Owner: Exporter
// over a real pebble signal store, the folder destination, and the bucket
// destination against a local HTTP server.

func TestConfigFromEnv(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	if _, on, err := ConfigFromEnv(env(nil), "pc"); on || err != nil {
		t.Fatalf("unset must be off: %v %v", on, err)
	}
	for name, values := range map[string]map[string]string{
		"relative folder":   {"SWARM_OUTBOX_URL": "file://outbox"},
		"unknown scheme":    {"SWARM_OUTBOX_URL": "ftp://bucket"},
		"bucket no key":     {"SWARM_OUTBOX_URL": "gs://my-bucket/x"},
		"bad prefix":        {"SWARM_OUTBOX_URL": "gs://my-bucket/a b", "SWARM_OUTBOX_ACCESS_KEY_ID": "k", "SWARM_OUTBOX_SECRET_ACCESS_KEY": "s"},
		"credentials inUrl": {"SWARM_OUTBOX_URL": "s3://user:pass@my-bucket"},
		"bad machine":       {"SWARM_OUTBOX_URL": "file:///tmp/o", "SWARM_OUTBOX_MACHINE": "Not OK!"},
		"huge file limit":   {"SWARM_OUTBOX_URL": "file:///tmp/o", "SWARM_OUTBOX_MAX_FILE_MB": "99999"},
	} {
		if _, on, err := ConfigFromEnv(env(values), "pc"); !on || err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	cfg, on, err := ConfigFromEnv(env(map[string]string{"SWARM_OUTBOX_URL": "gs://my-bucket/swarm/", "SWARM_OUTBOX_ACCESS_KEY_ID": "GOOGID", "SWARM_OUTBOX_SECRET_ACCESS_KEY": "secret-value"}), "Roy's PC")
	if err != nil || !on || cfg.Endpoint != "https://storage.googleapis.com" || cfg.Prefix != "swarm" || cfg.Machine != "roy-s-pc" || cfg.MaxFileBytes != 512<<20 {
		t.Fatalf("gs config: %+v %v", cfg, err)
	}
	if strings.Contains(cfg.Describe(), "secret") || cfg.Describe() != "gs://my-bucket/swarm" {
		t.Fatalf("describe: %q", cfg.Describe())
	}
}

func newFeed(t *testing.T) (*pebblestore.Store, *pebblestore.SignalStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "main.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, pebblestore.NewSignalStore(store)
}

func appendSignal(t *testing.T, feed *pebblestore.SignalStore, sig pebblestore.Signal) pebblestore.Signal {
	t.Helper()
	sig.Source = pebblestore.SignalSourceSwarmd
	if sig.Severity == "" {
		sig.Severity = pebblestore.SignalSeverityInfo
	}
	out, err := feed.Append(sig)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExporterWritesLayoutInOrderOnceAndResumes(t *testing.T) {
	store, feed := newFeed(t)
	root := t.TempDir()
	cfg := Config{Scheme: "file", Dir: root, Machine: "pc", MaxFileBytes: 1 << 20}
	dest, err := cfg.Open()
	if err != nil {
		t.Fatal(err)
	}
	failResult := true
	build := func(ctx context.Context, sig pebblestore.Signal, b *Builder) (*Export, error) {
		switch sig.Kind {
		case "worker.run.succeeded":
			tmp := filepath.Join(t.TempDir(), "clip.png")
			if err := os.WriteFile(tmp, []byte("png bytes"), 0o600); err != nil {
				return nil, err
			}
			sum := sha256.Sum256([]byte("png bytes"))
			hash := hex.EncodeToString(sum[:])
			return &Export{JobID: "worker/w1/" + sig.Refs["run_id"], RunID: sig.Refs["run_id"],
				Manifest: map[string]any{"outcome": "media", "files": []map[string]any{{"path": "clip.png", "key": b.BlobKey(hash)}}},
				Blobs:    []Blob{{Path: tmp, SHA256: hash, Size: 9, Type: "image/png"}}}, nil
		case "worker.run.failed":
			if failResult {
				return nil, errors.New("job unreadable")
			}
		}
		return nil, nil
	}
	exporter := NewExporter(cfg, dest, feed, store, func() string { return "acct" }, build)

	first := appendSignal(t, feed, pebblestore.Signal{Kind: "daemon.started", Summary: "Swarm started"})
	job := appendSignal(t, feed, pebblestore.Signal{Kind: "worker.run.succeeded", Account: "acct", Summary: "Worker coder run succeeded", Refs: map[string]string{"worker_id": "w1", "run_id": "r1"}})
	appendSignal(t, feed, pebblestore.Signal{Kind: "worker.run.succeeded", Account: "someone-else", Summary: "not ours", Refs: map[string]string{"worker_id": "w9", "run_id": "r9"}})
	if err := exporter.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(root, "machines", "pc", "log")
	entries, _ := os.ReadDir(logDir)
	if len(entries) != 2 {
		t.Fatalf("log records: %d (another account's signal must not be exported)", len(entries))
	}
	rec := readJSON(t, filepath.Join(logDir, "000000000002.json"))
	if rec["v"].(float64) != 1 || rec["machine"] != "pc" || rec["kind"] != "worker.run.succeeded" || rec["job_id"] != "worker/w1/r1" || rec["result"] != "machines/pc/results/r1.json" || uint64(rec["seq"].(float64)) != job.Seq {
		t.Fatalf("job record: %v", rec)
	}
	manifest := readJSON(t, filepath.Join(root, "machines", "pc", "results", "r1.json"))
	if manifest["outcome"] != "media" || manifest["machine"] != "pc" || manifest["v"].(float64) != 1 {
		t.Fatalf("manifest: %v", manifest)
	}
	key := manifest["files"].([]any)[0].(map[string]any)["key"].(string)
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key))); err != nil || string(data) != "png bytes" {
		t.Fatalf("blob %s: %q %v", key, data, err)
	}
	if status := exporter.Status(); status.Cursor < job.Seq || status.Results != 1 || status.Failing || status.Destination != "file://"+root {
		t.Fatalf("status: %+v", status)
	}

	// A record already written is never changed by a retry.
	firstPath := filepath.Join(logDir, "000000000001.json")
	if err := dest.PutBytes(context.Background(), "machines/pc/log/000000000001.json", []byte(`{"changed":true}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	if again := readJSON(t, firstPath); again["kind"] != "daemon.started" || uint64(again["seq"].(float64)) != first.Seq {
		t.Fatalf("an existing record was overwritten: %v", again)
	}

	// A new exporter resumes from the saved cursor; a result that keeps
	// failing stops retrying and does not block the record after it.
	failed := appendSignal(t, feed, pebblestore.Signal{Kind: "worker.run.failed", Account: "acct", Summary: "Worker coder run failed", Refs: map[string]string{"worker_id": "w1", "run_id": "r2"}})
	after := appendSignal(t, feed, pebblestore.Signal{Kind: "permission.pending", Account: "acct", Summary: "waits"})
	resumed := NewExporter(cfg, dest, feed, store, func() string { return "acct" }, build)
	for i := 1; i < maxResultRetries; i++ {
		if err := resumed.ExportOnce(context.Background()); err == nil {
			t.Fatalf("attempt %d: a failing result must be retried", i)
		}
	}
	if err := resumed.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	failedRec := readJSON(t, filepath.Join(logDir, filepathSeq(failed.Seq)))
	if failedRec["result"] != nil || !strings.Contains(failedRec["attrs"].(map[string]any)["result_error"].(string), "job unreadable") {
		t.Fatalf("failed result record: %v", failedRec)
	}
	if _, err := os.Stat(filepath.Join(logDir, filepathSeq(after.Seq))); err != nil {
		t.Fatalf("record after the failing one: %v", err)
	}
	entries, _ = os.ReadDir(logDir)
	if len(entries) != 4 {
		t.Fatalf("log records after resume: %d", len(entries))
	}
}

func filepathSeq(seq uint64) string {
	data, _ := json.Marshal(seq)
	return strings.Repeat("0", 12-len(data)) + string(data) + ".json"
}

func TestBucketSignsAndCreatesOnly(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		if r.Method != http.MethodPut || !strings.HasPrefix(auth, "GOOG4-HMAC-SHA256 Credential=KEYID/") || !strings.Contains(auth, "/auto/storage/goog4_request") || r.Header.Get("x-amz-date") != "" ||
			!strings.Contains(auth, "x-goog-if-generation-match") || strings.Contains(auth, "secret-value") {
			http.Error(w, "<Error><Code>AccessDenied</Code><Message>no</Message></Error>", http.StatusForbidden)
			return
		}
		if r.Header.Get("x-goog-if-generation-match") != "0" {
			http.Error(w, "<Error><Code>MissingPrecondition</Code></Error>", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		if r.Header.Get("x-goog-content-sha256") != hex.EncodeToString(sum[:]) {
			http.Error(w, "<Error><Code>BadDigest</Code></Error>", http.StatusBadRequest)
			return
		}
		if _, exists := objects[r.URL.Path]; exists {
			http.Error(w, "<Error><Code>PreconditionFailed</Code></Error>", http.StatusPreconditionFailed)
			return
		}
		objects[r.URL.Path] = string(body)
	}))
	defer server.Close()
	cfg := Config{Scheme: "gs", Bucket: "my-bucket", Prefix: "swarm", Endpoint: server.URL, Region: "auto", AccessKeyID: "KEYID", SecretAccessKey: "secret-value", Machine: "pc"}
	dest, _ := cfg.Open()
	ctx := context.Background()
	if err := dest.PutBytes(ctx, cfg.key("machines/pc/log/000000000001.json"), []byte(`{"v":1}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	if err := dest.PutBytes(ctx, cfg.key("machines/pc/log/000000000001.json"), []byte(`{"v":2}`), "application/json"); err != nil {
		t.Fatalf("an existing object must count as written: %v", err)
	}
	if got := objects["/my-bucket/swarm/machines/pc/log/000000000001.json"]; got != `{"v":1}` {
		t.Fatalf("object: %q", got)
	}
	file := filepath.Join(t.TempDir(), "blob")
	_ = os.WriteFile(file, []byte("video"), 0o600)
	sum := sha256.Sum256([]byte("video"))
	if err := dest.PutFile(ctx, cfg.key("machines/pc/blobs/sha256/"+hex.EncodeToString(sum[:])), file, 5, hex.EncodeToString(sum[:]), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	bad := newBucket(Config{Scheme: "gs", Bucket: "my-bucket", Endpoint: server.URL, Region: "auto", AccessKeyID: "OTHER", SecretAccessKey: "x"})
	err := bad.PutBytes(ctx, "machines/pc/x.json", []byte("{}"), "")
	if err == nil || !strings.Contains(err.Error(), "403 AccessDenied") || strings.Contains(err.Error(), "no") && strings.Contains(err.Error(), "Message") {
		t.Fatalf("refused upload error: %v", err)
	}
	if err := dest.PutBytes(ctx, "machines/../etc/x", []byte("{}"), ""); err == nil {
		t.Fatal("a key with .. was accepted")
	}
}

func TestS3BucketUsesAWSSignatureAndIfNoneMatch(t *testing.T) {
	var auth, condition, google string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, condition, google = r.Header.Get("Authorization"), r.Header.Get("If-None-Match"), r.Header.Get("x-goog-if-generation-match")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dest, _ := Config{Scheme: "s3", Bucket: "my-bucket", Endpoint: server.URL, Region: "eu-west-1", AccessKeyID: "AKID", SecretAccessKey: "s"}.Open()
	if err := dest.PutBytes(context.Background(), "machines/pc/log/000000000001.json", []byte("{}"), "application/json"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/") || !strings.Contains(auth, "/eu-west-1/s3/aws4_request") || !strings.Contains(auth, "if-none-match") || condition != "*" || google != "" {
		t.Fatalf("s3 request: auth %q if-none-match %q goog %q", auth, condition, google)
	}
}
