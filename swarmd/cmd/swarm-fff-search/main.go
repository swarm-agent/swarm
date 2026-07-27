package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"swarm/packages/swarmd/internal/fff"
	"swarm/packages/swarmd/internal/tool/searchipc"
)

const (
	operationContent     = "content"
	operationFiles       = "files"
	operationDirectories = "directories"
	operationMixed       = "mixed"
	operationGlob        = "glob"
	defaultTimeout       = 8 * time.Second
)

type worker struct {
	inst             *fff.Instance
	indexRoot        string
	createdAt        time.Time
	initialScan      time.Duration
	watcherWait      time.Duration
	watcherReady     bool
	coldStarts       uint64
	protocolFailures uint64
	rootFailures     uint64
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = os.Stdin.Close()
	}()
	return runStream(ctx, os.Stdin, os.Stdout)
}

func runStream(ctx context.Context, input io.Reader, output io.Writer) error {
	dec := json.NewDecoder(input)
	enc := json.NewEncoder(output)
	w := &worker{}
	defer w.close()

	for {
		var req searchipc.Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			w.protocolFailures++
			resp := w.errorResponse(req, "malformed_request", fmt.Sprintf("decode search request: %v", err), time.Time{})
			_ = enc.Encode(resp)
			return fmt.Errorf("decode search request: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		resp := w.handle(req, started)
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("encode search response: %w", err)
		}
	}
}

func (w *worker) close() {
	if w.inst != nil {
		w.inst.Destroy()
		w.inst = nil
	}
}

func (w *worker) handle(req searchipc.Request, started time.Time) searchipc.Response {
	resp := searchipc.Response{ProtocolVersion: searchipc.ProtocolVersion, RequestID: req.RequestID}
	if req.ProtocolVersion != 0 && req.ProtocolVersion != searchipc.ProtocolVersion {
		w.protocolFailures++
		return w.errorResponse(req, "protocol_version_mismatch", fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), started)
	}
	indexRoot, targetPath, err := normalizeScope(req)
	if err != nil {
		w.rootFailures++
		return w.errorResponse(req, "invalid_scope", err.Error(), started)
	}
	if w.indexRoot != "" && !samePath(w.indexRoot, indexRoot) {
		w.rootFailures++
		return w.errorResponse(req, "root_mismatch", fmt.Sprintf("worker is bound to index root %q, request specified %q", w.indexRoot, indexRoot), started)
	}
	if w.inst == nil {
		if err := w.initialize(indexRoot, requestTimeout(req)); err != nil {
			return w.errorResponse(req, "initialization_failed", err.Error(), started)
		}
	}

	req.IndexRoot = indexRoot
	req.TargetPath = targetPath
	req.SearchRoot = ""
	if err := normalizeRequest(&req); err != nil {
		w.protocolFailures++
		return w.errorResponse(req, "invalid_request", err.Error(), started)
	}

	timeout := requestTimeout(req)
	operation := normalizeOperation(req.Operation)
	resp.Completed = true
	switch operation {
	case operationContent:
		resp.ContentResults = runContentSearches(w.inst, req, timeout)
		if len(resp.ContentResults) == 1 {
			resp.Content = resp.ContentResults[0]
		}
		resp.FileResults = runFileSearches(w.inst, req, resp.ContentResults)
	case operationFiles:
		resp.FileResults = runFileFinds(w.inst, req, operationFiles)
	case operationGlob:
		resp.FileResults = runGlobSearches(w.inst, req)
	case operationDirectories:
		resp.DirectoryResults = runDirectorySearches(w.inst, req)
	case operationMixed:
		resp.MixedResults = runMixedSearches(w.inst, req)
	default:
		return w.errorResponse(req, "unsupported_operation", fmt.Sprintf("unsupported search operation %q", req.Operation), started)
	}
	if elapsed := time.Since(started); elapsed > timeout {
		return w.errorResponse(req, "hard_timeout", fmt.Sprintf("request exceeded hard timeout %s (elapsed %s)", timeout, elapsed), started)
	}
	if err := constrainAndNormalizeResponse(&resp, indexRoot, targetPath); err != nil {
		return w.errorResponse(req, "unsafe_scope_result", err.Error(), started)
	}
	resp.Diagnostics = w.diagnostics(started)
	return resp
}

func (w *worker) initialize(root string, timeout time.Duration) error {
	inst, _, err := fff.CreateWithOptions(root, fff.CreateOptions{Watch: true, DisableAIMode: true})
	if err != nil {
		return fmt.Errorf("create watcher-backed FFF instance: %w", err)
	}
	w.inst = inst
	w.indexRoot = root
	w.createdAt = time.Now()
	w.coldStarts++
	completed, elapsed, err := inst.WaitForScan(timeout)
	w.initialScan = elapsed
	if err != nil {
		w.close()
		return fmt.Errorf("wait for initial scan: %w", err)
	}
	if !completed {
		w.close()
		return fmt.Errorf("initial scan did not complete within %s", timeout)
	}
	remaining := timeout - elapsed
	if remaining <= 0 {
		w.close()
		return fmt.Errorf("watcher was not ready within %s", timeout)
	}
	ready, watcherElapsed, err := inst.WaitForWatcher(remaining)
	w.watcherWait = watcherElapsed
	w.watcherReady = ready
	if err != nil {
		w.close()
		return fmt.Errorf("wait for watcher: %w", err)
	}
	if !ready {
		w.close()
		return fmt.Errorf("watcher was not ready within %s", timeout)
	}
	return nil
}

func (w *worker) diagnostics(started time.Time) searchipc.Diagnostics {
	d := searchipc.Diagnostics{
		ColdStartCount: w.coldStarts, InitialScanMillis: w.initialScan.Milliseconds(),
		WatcherWaitMillis: w.watcherWait.Milliseconds(), WatcherReady: w.watcherReady,
		ProtocolFailureCount: w.protocolFailures, RootFailureCount: w.rootFailures,
	}
	if !w.createdAt.IsZero() {
		d.IndexAgeMillis = time.Since(w.createdAt).Milliseconds()
	}
	if !started.IsZero() {
		d.RequestDurationMillis = time.Since(started).Milliseconds()
	}
	return d
}

func (w *worker) errorResponse(req searchipc.Request, code, message string, started time.Time) searchipc.Response {
	return searchipc.Response{ProtocolVersion: searchipc.ProtocolVersion, RequestID: req.RequestID, Completed: false, HelperError: message, ErrorCode: code, Diagnostics: w.diagnostics(started)}
}

func normalizeScope(req searchipc.Request) (string, string, error) {
	indexRoot := strings.TrimSpace(req.IndexRoot)
	targetPath := strings.TrimSpace(req.TargetPath)
	legacy := strings.TrimSpace(req.SearchRoot)
	if indexRoot == "" {
		indexRoot = legacy
	}
	if targetPath == "" {
		targetPath = legacy
	}
	if indexRoot == "" {
		return "", "", errors.New("index root is required")
	}
	if targetPath == "" {
		targetPath = indexRoot
	}
	var err error
	if indexRoot, err = canonicalExistingPath(indexRoot); err != nil {
		return "", "", fmt.Errorf("resolve index root: %w", err)
	}
	info, err := os.Stat(indexRoot)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("index root must be an existing directory")
	}
	if targetPath, err = canonicalExistingPath(targetPath); err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	if !pathWithin(indexRoot, targetPath) {
		return "", "", fmt.Errorf("target path %q is outside index root %q", targetPath, indexRoot)
	}
	return indexRoot, targetPath, nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func normalizeRequest(req *searchipc.Request) error {
	req.Queries = compactQueries(req.Queries)
	if len(req.Queries) == 0 {
		return errors.New("at least one search query is required")
	}
	if req.MaxResults < 1 {
		req.MaxResults = 1
	}
	if req.PageLimit == 0 {
		req.PageLimit = uint32(req.MaxResults)
	}
	return nil
}

func requestTimeout(req searchipc.Request) time.Duration {
	timeout := time.Duration(req.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		return defaultTimeout
	}
	return timeout
}

func normalizeOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "", "content", "grep":
		return operationContent
	case "file", "files", "find", "path", "paths":
		return operationFiles
	case "dir", "dirs", "directory", "directories":
		return operationDirectories
	case "mixed", "all":
		return operationMixed
	case "glob", "pattern":
		return operationGlob
	default:
		return strings.ToLower(strings.TrimSpace(operation))
	}
}

func runContentSearches(inst *fff.Instance, req searchipc.Request, timeout time.Duration) []searchipc.GrepQueryResult {
	results := make([]searchipc.GrepQueryResult, 0, len(req.Queries))
	for _, query := range req.Queries {
		result := searchipc.GrepQueryResult{Query: query, Mode: operationContent}
		matches, metrics, err := inst.GrepWithConfig(buildFFFGrepQuery(req, query), fff.GrepOptions{PageLimit: req.PageLimit, TimeBudget: timeout, FileOffset: req.FileOffset, Mode: grepMode(req.ContentMode), MaxMatchesPerFile: req.MaxMatchesPerFile, BeforeContext: req.BeforeContext, AfterContext: req.AfterContext, ClassifyDefinitions: true})
		result.Matches, result.Metrics = matches, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func runFileSearches(inst *fff.Instance, req searchipc.Request, contentResults []searchipc.GrepQueryResult) []searchipc.SearchQueryResult {
	results := make([]searchipc.SearchQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		if !needsFileSearchFallback(query, contentResults) {
			continue
		}
		result := searchipc.SearchQueryResult{Query: query, Mode: operationFiles}
		items, metrics, err := inst.SearchWithOptions(buildFFFSearchQuery(req, query), pageSize, req.PageIndex)
		result.Items, result.Metrics = items, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func runFileFinds(inst *fff.Instance, req searchipc.Request, mode string) []searchipc.SearchQueryResult {
	results := make([]searchipc.SearchQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		result := searchipc.SearchQueryResult{Query: query, Mode: mode}
		items, metrics, err := inst.SearchWithOptions(buildFFFSearchQuery(req, query), pageSize, req.PageIndex)
		result.Items, result.Metrics = items, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func runGlobSearches(inst *fff.Instance, req searchipc.Request) []searchipc.SearchQueryResult {
	results := make([]searchipc.SearchQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		result := searchipc.SearchQueryResult{Query: query, Mode: operationGlob}
		items, metrics, err := inst.GlobWithOptions(scopedGlob(req, query), pageSize, req.PageIndex)
		result.Items, result.Metrics = items, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func runDirectorySearches(inst *fff.Instance, req searchipc.Request) []searchipc.DirectoryQueryResult {
	results := make([]searchipc.DirectoryQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		result := searchipc.DirectoryQueryResult{Query: query, Mode: operationDirectories}
		items, metrics, err := inst.SearchDirectoriesWithOptions(buildFFFSearchQuery(req, query), pageSize, req.PageIndex)
		result.Items, result.Metrics = items, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func runMixedSearches(inst *fff.Instance, req searchipc.Request) []searchipc.MixedQueryResult {
	results := make([]searchipc.MixedQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		result := searchipc.MixedQueryResult{Query: query, Mode: operationMixed}
		items, metrics, err := inst.SearchMixedWithOptions(buildFFFSearchQuery(req, query), pageSize, req.PageIndex)
		result.Items, result.Metrics = items, metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func targetConstraint(req searchipc.Request) string {
	rel, _ := filepath.Rel(req.IndexRoot, req.TargetPath)
	if rel == "." {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if info, err := os.Stat(req.TargetPath); err == nil && !info.IsDir() {
		return rel
	}
	return strings.TrimSuffix(rel, "/") + "/"
}

func buildFFFGrepQuery(req searchipc.Request, pattern string) string {
	parts := []string{}
	if c := targetConstraint(req); c != "" {
		parts = append(parts, c)
	}
	if include := strings.TrimSpace(req.Include); include != "" {
		parts = append(parts, include)
	}
	parts = append(parts, strings.TrimSpace(pattern))
	return strings.Join(parts, " ")
}
func buildFFFSearchQuery(req searchipc.Request, pattern string) string {
	parts := []string{}
	if c := targetConstraint(req); c != "" {
		parts = append(parts, c)
	}
	if include := strings.TrimSpace(req.Include); include != "" {
		parts = append(parts, include)
	}
	parts = append(parts, strings.TrimSpace(pattern))
	return strings.Join(parts, " ")
}
func scopedGlob(req searchipc.Request, pattern string) string {
	c := targetConstraint(req)
	if c == "" {
		return pattern
	}
	if info, err := os.Stat(req.TargetPath); err == nil && !info.IsDir() {
		return filepath.ToSlash(c)
	}
	return strings.TrimSuffix(filepath.ToSlash(c), "/") + "/" + strings.TrimPrefix(pattern, "/")
}

func constrainAndNormalizeResponse(resp *searchipc.Response, root, target string) error {
	for i := range resp.ContentResults {
		resp.ContentResults[i].Matches = filterGrep(resp.ContentResults[i].Matches, root, target)
	}
	if len(resp.ContentResults) == 1 {
		resp.Content = resp.ContentResults[0]
	}
	for i := range resp.FileResults {
		resp.FileResults[i].Items = filterFiles(resp.FileResults[i].Items, root, target)
	}
	for i := range resp.DirectoryResults {
		resp.DirectoryResults[i].Items = filterDirectories(resp.DirectoryResults[i].Items, root, target)
	}
	for i := range resp.MixedResults {
		resp.MixedResults[i].Items = filterMixed(resp.MixedResults[i].Items, root, target)
	}
	return nil
}
func normalizedRelative(root, target, raw string) (string, bool) {
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, filepath.FromSlash(raw))
	}
	candidate = filepath.Clean(candidate)
	if !pathWithin(target, candidate) {
		return "", false
	}
	base := target
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		base = filepath.Dir(target)
	}
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
func filterGrep(in []fff.GrepMatch, root, target string) []fff.GrepMatch {
	out := in[:0]
	for _, v := range in {
		if rel, ok := normalizedRelative(root, target, v.RelativePath); ok {
			if !filepath.IsAbs(v.Path) {
				v.Path = filepath.Join(root, filepath.FromSlash(v.RelativePath))
			}
			v.RelativePath = rel
			out = append(out, v)
		}
	}
	return out
}
func filterFiles(in []fff.SearchItem, root, target string) []fff.SearchItem {
	out := in[:0]
	for _, v := range in {
		if rel, ok := normalizedRelative(root, target, v.RelativePath); ok {
			if !filepath.IsAbs(v.Path) {
				v.Path = filepath.Join(root, filepath.FromSlash(v.RelativePath))
			}
			v.RelativePath = rel
			out = append(out, v)
		}
	}
	return out
}
func filterDirectories(in []fff.DirectoryItem, root, target string) []fff.DirectoryItem {
	out := in[:0]
	for _, v := range in {
		if rel, ok := normalizedRelative(root, target, v.RelativePath); ok {
			if !filepath.IsAbs(v.Path) {
				v.Path = filepath.Join(root, filepath.FromSlash(v.RelativePath))
			}
			v.RelativePath = rel
			out = append(out, v)
		}
	}
	return out
}
func filterMixed(in []fff.MixedItem, root, target string) []fff.MixedItem {
	out := in[:0]
	for _, v := range in {
		if rel, ok := normalizedRelative(root, target, v.RelativePath); ok {
			if !filepath.IsAbs(v.Path) {
				v.Path = filepath.Join(root, filepath.FromSlash(v.RelativePath))
			}
			v.RelativePath = rel
			out = append(out, v)
		}
	}
	return out
}
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
func grepMode(mode string) uint8 {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "regex", "regexp":
		return 1
	case "fuzzy":
		return 2
	default:
		return 0
	}
}
func needsFileSearchFallback(query string, results []searchipc.GrepQueryResult) bool {
	for _, r := range results {
		if strings.EqualFold(strings.TrimSpace(r.Query), strings.TrimSpace(query)) {
			return strings.TrimSpace(r.Error) != "" || r.Metrics.TotalMatched == 0
		}
	}
	return true
}
func compactQueries(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, q := range queries {
		if q = strings.TrimSpace(q); q != "" {
			out = append(out, q)
		}
	}
	return out
}
