package egress

import (
	"bytes"
	"io"
)

// scrubReader replaces every occurrence of each value with its stand-in in a
// stream, holding back just enough bytes that a value split across reads is
// still caught. It keeps a secret that an upstream echoes back (in an error
// message, a debug endpoint) from reaching the sandbox.
type scrubReader struct {
	src     io.ReadCloser
	pairs   [][2][]byte // value, replacement
	hold    int
	buf     []byte
	out     []byte
	srcDone bool
}

func newScrubReader(src io.ReadCloser, pairs [][2][]byte) io.ReadCloser {
	hold := 0
	for _, p := range pairs {
		if len(p[0])-1 > hold {
			hold = len(p[0]) - 1
		}
	}
	return &scrubReader{src: src, pairs: pairs, hold: hold}
}

func replaceAll(data []byte, pairs [][2][]byte) []byte {
	for _, p := range pairs {
		if len(p[0]) > 0 {
			data = bytes.ReplaceAll(data, p[0], p[1])
		}
	}
	return data
}

func (r *scrubReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.srcDone {
			if len(r.buf) == 0 {
				return 0, io.EOF
			}
			r.out = replaceAll(r.buf, r.pairs)
			r.buf = nil
			break
		}
		chunk := make([]byte, 32<<10)
		n, err := r.src.Read(chunk)
		r.buf = append(r.buf, chunk[:n]...)
		if err == io.EOF {
			r.srcDone = true
		} else if err != nil {
			return 0, err
		}
		r.buf = replaceAll(r.buf, r.pairs)
		if len(r.buf) > r.hold {
			cut := len(r.buf) - r.hold
			r.out = append(r.out, r.buf[:cut]...)
			r.buf = append([]byte(nil), r.buf[cut:]...)
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *scrubReader) Close() error { return r.src.Close() }
