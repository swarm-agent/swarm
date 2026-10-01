package storagehub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3DriverConfig configures an S3 / GCS S3-interoperability storage driver.
type S3DriverConfig struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ForcePathStyle  bool
	HTTPClient      *http.Client
}

// S3Driver provides an S3-compatible object storage driver implementing Driver.
type S3Driver struct {
	bucket          string
	endpoint        string
	region          string
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	forcePathStyle  bool
	client          *http.Client
}

// NewS3Driver creates a new S3Driver.
func NewS3Driver(cfg S3DriverConfig) (*S3Driver, error) {
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, errors.New("bucket name is required for s3 driver")
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
	}
	endpoint = strings.TrimRight(endpoint, "/")

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 60 * time.Second,
		}
	}

	return &S3Driver{
		bucket:          bucket,
		endpoint:        endpoint,
		region:          region,
		accessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
		secretAccessKey: strings.TrimSpace(cfg.SecretAccessKey),
		sessionToken:    strings.TrimSpace(cfg.SessionToken),
		forcePathStyle:  cfg.ForcePathStyle,
		client:          client,
	}, nil
}

func (s *S3Driver) buildURL(key string, query url.Values) (*url.URL, error) {
	cleanKey := strings.TrimLeft(key, "/")
	var baseStr string
	if s.forcePathStyle {
		if cleanKey != "" {
			baseStr = fmt.Sprintf("%s/%s/%s", s.endpoint, s.bucket, cleanKey)
		} else {
			baseStr = fmt.Sprintf("%s/%s", s.endpoint, s.bucket)
		}
	} else {
		parsedEndpoint, err := url.Parse(s.endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint: %w", err)
		}
		if cleanKey != "" {
			baseStr = fmt.Sprintf("%s://%s.%s/%s", parsedEndpoint.Scheme, s.bucket, parsedEndpoint.Host, cleanKey)
		} else {
			baseStr = fmt.Sprintf("%s://%s.%s", parsedEndpoint.Scheme, s.bucket, parsedEndpoint.Host)
		}
	}

	u, err := url.Parse(baseStr)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func (s *S3Driver) getSigningKey(dateStamp string) []byte {
	kSecret := []byte("AWS4" + s.secretAccessKey)
	kDate := hmacSHA256(kSecret, []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func (s *S3Driver) signRequest(req *http.Request, payloadHash string) {
	if s.accessKeyID == "" || s.secretAccessKey == "" {
		return
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if s.sessionToken != "" {
		req.Header.Set("x-amz-security-token", s.sessionToken)
	}

	// Signed headers list
	headersToSign := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if s.sessionToken != "" {
		headersToSign = append(headersToSign, "x-amz-security-token")
	}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headersToSign = append(headersToSign, "content-type")
	}
	sort.Strings(headersToSign)

	signedHeaders := strings.Join(headersToSign, ";")

	var canonicalHeaders strings.Builder
	for _, h := range headersToSign {
		var val string
		if h == "host" {
			val = req.URL.Host
		} else {
			val = strings.TrimSpace(req.Header.Get(h))
		}
		canonicalHeaders.WriteString(fmt.Sprintf("%s:%s\n", h, val))
	}

	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalQuery := req.URL.Query().Encode()

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	algorithm := "AWS4-HMAC-SHA256"
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, s.region)
	hashedCanonicalRequest := sha256Hex([]byte(canonicalRequest))

	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		credentialScope,
		hashedCanonicalRequest,
	}, "\n")

	signingKey := s.getSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, s.accessKeyID, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

func (s *S3Driver) Get(ctx context.Context, key string) ([]byte, error) {
	reqURL, err := s.buildURL(key, nil)
	if err != nil {
		return nil, fmt.Errorf("build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create get request: %w", err)
	}

	payloadHash := sha256Hex(nil)
	s.signRequest(req, payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, os.ErrNotExist
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("s3 get error %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read s3 response body: %w", err)
	}
	return data, nil
}

func (s *S3Driver) Put(ctx context.Context, key string, data []byte, contentType string) error {
	reqURL, err := s.buildURL(key, nil)
	if err != nil {
		return fmt.Errorf("build url: %w", err)
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL.String(), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create put request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))

	payloadHash := sha256Hex(data)
	s.signRequest(req, payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (s *S3Driver) Delete(ctx context.Context, key string) error {
	reqURL, err := s.buildURL(key, nil)
	if err != nil {
		return fmt.Errorf("build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}

	payloadHash := sha256Hex(nil)
	s.signRequest(req, payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 delete request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("s3 delete error %d: %s", resp.StatusCode, string(body))
}

func (s *S3Driver) Exists(ctx context.Context, key string) (bool, error) {
	reqURL, err := s.buildURL(key, nil)
	if err != nil {
		return false, fmt.Errorf("build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL.String(), nil)
	if err != nil {
		return false, fmt.Errorf("create head request: %w", err)
	}

	payloadHash := sha256Hex(nil)
	s.signRequest(req, payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("s3 head request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("s3 head error %d", resp.StatusCode)
}

type s3ListBucketResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	Name                  string   `xml:"Name"`
	Prefix                string   `xml:"Prefix"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
}

func (s *S3Driver) List(ctx context.Context, prefix string) ([]string, error) {
	cleanPrefix := strings.TrimLeft(prefix, "/")
	var allKeys []string
	continuationToken := ""
	maxPages := 50 // Safety bound

	for page := 0; page < maxPages; page++ {
		query := url.Values{}
		query.Set("list-type", "2")
		if cleanPrefix != "" {
			query.Set("prefix", cleanPrefix)
		}
		if continuationToken != "" {
			query.Set("continuation-token", continuationToken)
		}

		reqURL, err := s.buildURL("", query)
		if err != nil {
			return nil, fmt.Errorf("build list url: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create list request: %w", err)
		}

		payloadHash := sha256Hex(nil)
		s.signRequest(req, payloadHash)

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("s3 list request: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, fmt.Errorf("s3 list error %d: %s", resp.StatusCode, string(body))
		}

		var listResult s3ListBucketResult
		err = xml.NewDecoder(resp.Body).Decode(&listResult)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode s3 list xml: %w", err)
		}

		for _, item := range listResult.Contents {
			cleanKey := strings.TrimPrefix(item.Key, "/")
			if strings.HasPrefix(cleanKey, cleanPrefix) {
				allKeys = append(allKeys, cleanKey)
			}
		}

		if !listResult.IsTruncated || listResult.NextContinuationToken == "" {
			break
		}
		continuationToken = listResult.NextContinuationToken
	}

	sort.Strings(allKeys)
	return allKeys, nil
}
