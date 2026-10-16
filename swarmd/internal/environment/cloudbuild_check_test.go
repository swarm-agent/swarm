package environment

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type checkFake struct {
	objects                                 map[string]bool
	putErr, deleteErr, listErr, metadataErr error
	listed                                  bool
	puts, deletes, metadataReads            int
}

func (f *checkFake) BucketMetadata(context.Context, string) error {
	f.metadataReads++
	return f.metadataErr
}

func (f *checkFake) Put(_ context.Context, _, name string, r io.Reader) error {
	f.puts++
	_, _ = io.ReadAll(r)
	f.objects[name] = true
	return f.putErr
}
func (f *checkFake) Get(context.Context, string, string) ([]byte, error) { return nil, nil }
func (f *checkFake) DeletePrefix(_ context.Context, _, prefix string) error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			delete(f.objects, k)
		}
	}
	return nil
}
func (f *checkFake) List(context.Context) error { f.listed = true; return f.listErr }

// Purpose: CheckCloudBuild must verify metadata/write/delete/list without creating builds,
// preserve unrelated objects and expose API errors. Interface fakes are the
// narrowest boundary that proves these operator-check side effects.
func TestCloudBuildCheck(t *testing.T) {
	for _, mode := range []string{"ok", "metadata", "put", "delete", "list"} {
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("API denied")
			f := &checkFake{objects: map[string]bool{"unrelated": true}}
			switch mode {
			case "metadata":
				f.metadataErr = sentinel
			case "put":
				f.putErr = sentinel
			case "delete":
				f.deleteErr = sentinel
			case "list":
				f.listErr = sentinel
			}
			err := CheckCloudBuild(context.Background(), "bucket", f, f)
			if f.metadataReads != 1 || (mode == "metadata" && (f.puts != 0 || f.deletes != 0)) {
				t.Fatalf("metadata ordering: %+v", f)
			}
			if mode == "ok" && err != nil {
				t.Fatal(err)
			}
			if mode != "ok" && !errors.Is(err, sentinel) {
				t.Fatalf("lost API error: %v", err)
			}
			if !f.objects["unrelated"] {
				t.Fatal("deleted unrelated object")
			}
			if mode != "delete" && len(f.objects) != 1 {
				t.Fatal("probe leaked")
			}
			if f.listed != (mode == "ok" || mode == "list") {
				t.Fatal("incorrect list ordering")
			}
		})
	}
}

// Purpose: the CLI's GoogleStorage metadata boundary must issue a bucket GET,
// reject denied identities before any mutation, and name the missing bucket role.
// A transport fake is the narrowest layer proving the URL and HTTP status mapping.
func TestCloudBuildBucketMetadata(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		calls := 0
		g := GoogleStorage{REST: GoogleREST{Client: &http.Client{Transport: cloudTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "GET" || r.URL.String() != "https://storage.googleapis.com/storage/v1/b/test-bucket" {
				t.Fatalf("unexpected metadata request: %s %s", r.Method, r.URL)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})}}}
		err := g.BucketMetadata(context.Background(), "test-bucket")
		if (err == nil) != (status == 200) || calls != 1 {
			t.Fatalf("status=%d calls=%d err=%v", status, calls, err)
		}
		if status == 403 && (!strings.Contains(err.Error(), "roles/storage.legacyBucketReader") || !strings.Contains(err.Error(), "build's service account")) {
			t.Fatalf("missing remediation: %v", err)
		}
	}
}
