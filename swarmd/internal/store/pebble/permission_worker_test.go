package pebblestore

import (
	"io"

	"testing"

	"github.com/cockroachdb/pebble"
)

type permissionCountingReader struct {
	pebble.Reader
	gets []string
	t    *testing.T
}

func (r *permissionCountingReader) Get(key []byte) ([]byte, io.Closer, error) {
	r.gets = append(r.gets, string(key))
	return r.Reader.Get(key)
}
func (r *permissionCountingReader) NewIter(o *pebble.IterOptions) (*pebble.Iterator, error) {
	r.t.Fatal("permission lookup must not iterate history")
	return nil, nil
}
