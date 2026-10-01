package storagehub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestS3Driver_EndToEnd(t *testing.T) {
	var mu sync.RWMutex
	store := make(map[string][]byte)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=test-key/") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-amz-date") == "" {
			http.Error(w, "missing amz date", http.StatusBadRequest)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		path = strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			store[path] = data
			mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			// Check if list query
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				mu.RLock()
				var xmlContents strings.Builder
				for k := range store {
					if strings.HasPrefix(k, prefix) {
						xmlContents.WriteString(fmt.Sprintf("<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(store[k])))
					}
				}
				mu.RUnlock()

				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprintf(w, `<ListBucketResult><Name>test-bucket</Name><Prefix>%s</Prefix><IsTruncated>false</IsTruncated>%s</ListBucketResult>`,
					prefix, xmlContents.String())
				return
			}

			mu.RLock()
			data, ok := store[path]
			mu.RUnlock()
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Write(data)

		case http.MethodHead:
			mu.RLock()
			_, ok := store[path]
			mu.RUnlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			mu.Lock()
			delete(store, path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	driver, err := NewS3Driver(S3DriverConfig{
		Bucket:          "test-bucket",
		Endpoint:        ts.URL,
		Region:          "us-east-1",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		ForcePathStyle:  true,
		HTTPClient:      ts.Client(),
	})
	if err != nil {
		t.Fatalf("failed to create s3 driver: %v", err)
	}

	ctx := context.Background()

	// Put
	err = driver.Put(ctx, "workers/test-worker/worker.json", []byte(`{"name":"test"}`), "application/json")
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	// Exists
	exists, err := driver.Exists(ctx, "workers/test-worker/worker.json")
	if err != nil {
		t.Fatalf("exists failed: %v", err)
	}
	if !exists {
		t.Fatalf("expected key to exist")
	}

	// Non-existent key
	exists, err = driver.Exists(ctx, "workers/non-existent.json")
	if err != nil {
		t.Fatalf("exists failed for non-existent: %v", err)
	}
	if exists {
		t.Fatalf("expected non-existent key to not exist")
	}

	// Get
	data, err := driver.Get(ctx, "workers/test-worker/worker.json")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if string(data) != `{"name":"test"}` {
		t.Fatalf("unexpected data: %s", string(data))
	}

	// Get non-existent
	_, err = driver.Get(ctx, "workers/missing.json")
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got: %v", err)
	}

	// Put a second file
	err = driver.Put(ctx, "workers/test-worker/sessions/sess-1/state.json", []byte(`{"status":"running"}`), "application/json")
	if err != nil {
		t.Fatalf("put second failed: %v", err)
	}

	// List
	keys, err := driver.List(ctx, "workers/test-worker/")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "workers/test-worker/sessions/sess-1/state.json" && keys[1] != "workers/test-worker/sessions/sess-1/state.json" {
		t.Fatalf("unexpected keys: %v", keys)
	}

	// Delete
	err = driver.Delete(ctx, "workers/test-worker/worker.json")
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	exists, err = driver.Exists(ctx, "workers/test-worker/worker.json")
	if err != nil {
		t.Fatalf("exists after delete failed: %v", err)
	}
	if exists {
		t.Fatalf("expected key to be deleted")
	}
}

func TestS3Driver_GCSDefaults(t *testing.T) {
	driver, err := NewS3Driver(S3DriverConfig{
		Bucket:          "my-gcs-bucket",
		Endpoint:        "https://storage.googleapis.com",
		Region:          "auto",
		AccessKeyID:     "GOOG12345",
		SecretAccessKey: "secret12345",
		ForcePathStyle:  true,
	})
	if err != nil {
		t.Fatalf("failed to create gcs driver: %v", err)
	}
	if driver.endpoint != "https://storage.googleapis.com" {
		t.Fatalf("unexpected endpoint: %s", driver.endpoint)
	}
	if driver.region != "auto" {
		t.Fatalf("unexpected region: %s", driver.region)
	}
}
