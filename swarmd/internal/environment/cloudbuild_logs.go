package environment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// LogTail reads only the end of the build's GCS log, using the Builds client's
// credentials. Missing or unreadable logs leave statusDetail as the diagnostic.
func (g GoogleREST) LogTail(ctx context.Context, b Build) (string, error) {
	location, err := url.Parse(b.LogsBucket)
	if err != nil || location.Scheme != "gs" || location.Host == "" || b.ID == "" || g.Client == nil {
		return "", fmt.Errorf("build log location unavailable")
	}
	object := strings.TrimPrefix(strings.TrimRight(location.Path, "/")+"/log-"+b.ID+".txt", "/")
	endpoint := "https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(location.Host) + "/o/" + url.PathEscape(object) + "?alt=media"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Range", "bytes=-8192")
	res, err := g.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("build log HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil {
		return "", err
	}
	if len(data) > 8192 {
		return "", fmt.Errorf("build log server ignored bounded range")
	}
	return boundedBuildTail(string(data), 8192), nil
}

func boundedBuildTail(s string, limit int) string {
	s = strings.TrimRight(s, "\r\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 40 {
		s = strings.Join(lines[len(lines)-40:], "\n")
	}
	if limit <= 0 {
		return ""
	}
	if len(s) > limit {
		s = s[len(s)-limit:]
	}
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
