package environment

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "strings"
 "time"
)

// CloudStorage and CloudBuilds are the two remote boundaries used by a landing.
// Implementations must return errors for unsuccessful remote operations.
type CloudStorage interface {
 Put(context.Context, string, string, io.Reader) error
 Get(context.Context, string, string) ([]byte, error)
 DeletePrefix(context.Context, string, string) error
}
type CloudBuilds interface {
 Create(context.Context, Build) (Build, error)
 Get(context.Context, string) (Build, error)
}
type Build struct {
 ID string `json:"id,omitempty"`
 Status string `json:"status,omitempty"`
 Steps []BuildStep `json:"steps,omitempty"`
 Timeout string `json:"timeout,omitempty"`
 ServiceAccount string `json:"serviceAccount,omitempty"`
 Options BuildOptions `json:"options,omitempty"`
}
type BuildOptions struct {
 MachineType string `json:"machineType,omitempty"`
 Logging string `json:"logging,omitempty"`
}
type BuildStep struct {
 Name string `json:"name"`
 Entrypoint string `json:"entrypoint"`
 Args []string `json:"args"`
}

// GoogleREST uses an authenticated HTTP client supplied by startup. Credentials
// must be resolved by ADC with the cloud-platform scope, never stored in config.
type GoogleREST struct {
 Client *http.Client
 Project, Region string
}
func (g GoogleREST) call(ctx context.Context, method, endpoint string, body io.Reader, limit int64) ([]byte, error) {
 if g.Client == nil { return nil, fmt.Errorf("Google ADC HTTP client unavailable") }
 req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
 if err != nil { return nil, err }
 if body != nil { req.Header.Set("Content-Type", "application/json") }
 res, err := g.Client.Do(req)
 if err != nil { return nil, err }
 defer res.Body.Close()
 if res.StatusCode < 200 || res.StatusCode >= 300 { return nil, fmt.Errorf("Google API %s: HTTP %d", method, res.StatusCode) }
 data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
 if int64(len(data)) > limit { return nil, fmt.Errorf("Google API response exceeds limit") }
 return data, err
}
func (g GoogleREST) buildsURL() string {
 root := "https://cloudbuild.googleapis.com/v1/projects/"+url.PathEscape(g.Project)
 if g.Region != "" && g.Region != "global" { root += "/locations/"+url.PathEscape(g.Region) }
 return root+"/builds"
}
func (g GoogleREST) Create(ctx context.Context, build Build) (Build, error) {
 data, err := json.Marshal(build); if err != nil { return Build{}, err }
 data, err = g.call(ctx, "POST", g.buildsURL(), bytes.NewReader(data), 1<<20)
 if err != nil { return Build{}, err }
 // CreateBuild returns a long-running Operation, not a Build.
 var op struct { Metadata struct { Build Build `json:"build"` } `json:"metadata"` }
 if err := json.Unmarshal(data, &op); err != nil { return Build{}, err }
 if op.Metadata.Build.ID == "" { return Build{}, fmt.Errorf("Cloud Build operation has no build ID") }
 return op.Metadata.Build, nil
}
func (g GoogleREST) Get(ctx context.Context, id string) (Build, error) {
 data, err := g.call(ctx, "GET", g.buildsURL()+"/"+url.PathEscape(id), nil, 1<<20)
 if err != nil { return Build{}, err }
 var b Build; err = json.Unmarshal(data, &b); return b, err
}
// GoogleStorage shares the same ADC transport as the build client.
type GoogleStorage struct { REST GoogleREST }
func (g GoogleStorage) Put(ctx context.Context, bucket, object string, body io.Reader) error {
 endpoint := "https://storage.googleapis.com/upload/storage/v1/b/"+url.PathEscape(bucket)+"/o?uploadType=media&name="+url.QueryEscape(object)
 _, err := g.REST.call(ctx, "POST", endpoint, body, 1<<20); return err
}
func (g GoogleStorage) Get(ctx context.Context, bucket, object string) ([]byte, error) {
 return g.REST.call(ctx, "GET", "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o/"+url.PathEscape(object)+"?alt=media", nil, 4<<20)
}
func (g GoogleStorage) DeletePrefix(ctx context.Context, bucket, prefix string) error {
 if !strings.HasPrefix(prefix, "landings/") || !strings.HasSuffix(prefix, "/") || prefix == "landings/" { return fmt.Errorf("invalid landing cleanup prefix") }
 root := "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o"
 token := ""
 for {
 data, err := g.REST.call(ctx, "GET", root+"?prefix="+url.QueryEscape(prefix)+"&pageToken="+url.QueryEscape(token), nil, 4<<20)
 if err != nil { return err }
 var page struct { Items []struct { Name string `json:"name"` } `json:"items"`; Next string `json:"nextPageToken"` }
 if err := json.Unmarshal(data, &page); err != nil { return err }
 for _, item := range page.Items {
 if !strings.HasPrefix(item.Name, prefix) { return fmt.Errorf("storage returned object outside landing prefix") }
 if _, err := g.REST.call(ctx, "DELETE", root+"/"+url.PathEscape(item.Name), nil, 1<<20); err != nil { return err }
 }
 if page.Next == "" { return nil }; if page.Next == token { return fmt.Errorf("storage repeated page token") }; token = page.Next
 }
}

type CloudBuildConfig struct {
 Project, Region, Bucket, MachineType, ServiceAccount string
 TimeoutMinutes int
}
type CloudBuild struct {
 Config CloudBuildConfig
 Storage CloudStorage
 Builds CloudBuilds
}
func (p CloudBuild) timeout() time.Duration { return time.Duration(p.Config.TimeoutMinutes)*time.Minute }
