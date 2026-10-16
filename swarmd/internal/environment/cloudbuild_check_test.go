package environment

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type checkFake struct {
	objects                    map[string]bool
	putErr, deleteErr, listErr error
	listed                     bool
}

func (f *checkFake) Put(_ context.Context, _, name string, r io.Reader) error {
	_, _ = io.ReadAll(r)
	f.objects[name] = true
	return f.putErr
}
func (f *checkFake) Get(context.Context, string, string) ([]byte, error) { return nil, nil }
func (f *checkFake) DeletePrefix(_ context.Context, _, prefix string) error {
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

// Purpose: CheckCloudBuild must verify write/delete/list without creating builds,
// preserve unrelated objects and expose API errors. Interface fakes are the
// narrowest boundary that proves these operator-check side effects.
func TestCloudBuildCheck(t *testing.T) {
	for _, mode := range []string{"ok", "put", "delete", "list"} {
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("API denied")
			f := &checkFake{objects: map[string]bool{"unrelated": true}}
			switch mode {
			case "put":
				f.putErr = sentinel
			case "delete":
				f.deleteErr = sentinel
			case "list":
				f.listErr = sentinel
			}
			err := CheckCloudBuild(context.Background(), "bucket", f, f)
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
