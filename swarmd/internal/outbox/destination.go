package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Destination creates objects once. Creating an object that already exists
// succeeds without changing it, so every write can be retried.
type Destination interface {
	PutBytes(ctx context.Context, key string, data []byte, contentType string) error
	PutFile(ctx context.Context, key, path string, size int64, sha256Hex, contentType string) error
}

func checkKey(key string) error {
	if !keyPattern.MatchString(key) || strings.Contains("/"+key+"/", "/../") || strings.Contains("/"+key+"/", "/./") {
		return fmt.Errorf("outbox: invalid object key %q", key)
	}
	return nil
}

// ---- folder ----------------------------------------------------------------

type folder struct{ root string }

func newFolder(root string) (*folder, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("outbox folder: %w", err)
	}
	return &folder{root: root}, nil
}

func (f *folder) PutBytes(_ context.Context, key string, data []byte, _ string) error {
	return f.put(key, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func (f *folder) PutFile(_ context.Context, key, path string, _ int64, _ string, _ string) error {
	return f.put(key, func(w io.Writer) error {
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
}

// put writes a temporary file and links it into place: the link fails when
// the object exists, which makes creation atomic and write-once.
func (f *folder) put(key string, write func(io.Writer) error) error {
	if err := checkKey(key); err != nil {
		return err
	}
	final := filepath.Join(f.root, filepath.FromSlash(key))
	if _, err := os.Lstat(final); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".outbox-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), final); err != nil && !errors.Is(err, syscall.EEXIST) && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// ---- bucket (S3 API; Google Cloud Storage through its XML API) -------------

type bucket struct {
	cfg    Config
	gcs    bool
	client *http.Client
	now    func() time.Time
}

func newBucket(cfg Config) *bucket {
	return &bucket{
		cfg: cfg,
		gcs: cfg.Scheme == "gs",
		client: &http.Client{
			// Never follow a redirect elsewhere with a signed request.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}
}

func (b *bucket) PutBytes(ctx context.Context, key string, data []byte, contentType string) error {
	sum := sha256.Sum256(data)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return b.put(ctx, key, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(sum[:]), contentType)
}

func (b *bucket) PutFile(ctx context.Context, key, path string, size int64, sha256Hex, contentType string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	// A minute, plus a minute for every 64 MB.
	ctx, cancel := context.WithTimeout(ctx, time.Minute+time.Duration(size>>26)*time.Minute)
	defer cancel()
	return b.put(ctx, key, src, size, sha256Hex, contentType)
}

func (b *bucket) put(ctx context.Context, key string, body io.Reader, size int64, payloadHash, contentType string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	target, err := url.Parse(b.cfg.Endpoint + "/" + b.cfg.Bucket + "/" + key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	// Create only: an existing object answers 412 and stays as it was.
	if b.gcs {
		req.Header.Set("x-goog-if-generation-match", "0")
	} else {
		req.Header.Set("If-None-Match", "*")
	}
	b.sign(req, payloadHash)
	resp, err := b.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("outbox upload: %w", urlErr.Err)
		}
		return fmt.Errorf("outbox upload: %w", err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299, resp.StatusCode == http.StatusPreconditionFailed:
		return nil
	default:
		return fmt.Errorf("outbox upload of %s: HTTP %d %s", key, resp.StatusCode, storageErrorCode(reply))
	}
}

// storageErrorCode keeps the error code of an S3/GCS XML error reply; the
// rest of the reply is not repeated.
func storageErrorCode(reply []byte) string {
	text := string(reply)
	start := strings.Index(text, "<Code>")
	end := strings.Index(text, "</Code>")
	if start < 0 || end <= start+6 {
		return ""
	}
	return text[start+6 : end]
}

// sign adds an AWS Signature Version 4 (HMAC) authorization; Google Cloud
// Storage accepts it with an HMAC key, region "auto" and service "s3".
func (b *bucket) sign(req *http.Request, payloadHash string) {
	now := b.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	names := []string{"host"}
	for name := range req.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		value := req.URL.Host
		if name != "host" {
			value = strings.TrimSpace(req.Header.Get(name))
		}
		canonicalHeaders.WriteString(name + ":" + value + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonicalHeaders.String(), signed, payloadHash}, "\n")
	scope := day + "/" + b.cfg.Region + "/s3/aws4_request"
	digest := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(digest[:])}, "\n")
	key := hmacSHA256([]byte("AWS4"+b.cfg.SecretAccessKey), day)
	key = hmacSHA256(key, b.cfg.Region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+b.cfg.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
