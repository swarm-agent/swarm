package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/fff"
	"swarm/packages/swarmd/internal/tool/searchipc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var req searchipc.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		return fmt.Errorf("decode search request: %w", err)
	}
	req.SearchRoot = strings.TrimSpace(req.SearchRoot)
	if req.SearchRoot == "" {
		return errors.New("search root is required")
	}
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
	timeout := time.Duration(req.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	inst, _, err := fff.Create(req.SearchRoot, false)
	if err != nil {
		return encodeResponse(searchipc.Response{Completed: false, HelperError: err.Error()})
	}
	defer inst.Destroy()

	completed, _, err := inst.WaitForScan(timeout)
	if err != nil {
		return encodeResponse(searchipc.Response{Completed: false, HelperError: err.Error()})
	}
	if !completed {
		return encodeResponse(searchipc.Response{Completed: false})
	}

	resp := searchipc.Response{
		Completed:      true,
		ContentResults: runContentSearches(inst, req, timeout),
	}
	if len(resp.ContentResults) == 1 {
		resp.Content = resp.ContentResults[0]
	}
	resp.FileResults = runFileSearches(inst, req, resp.ContentResults)
	return encodeResponse(resp)
}

func runContentSearches(inst *fff.Instance, req searchipc.Request, timeout time.Duration) []searchipc.GrepQueryResult {
	results := make([]searchipc.GrepQueryResult, 0, len(req.Queries))
	for _, query := range req.Queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		result := searchipc.GrepQueryResult{
			Query: query,
			Mode:  "content",
		}
		matches, metrics, err := inst.GrepWithConfig(buildFFFGrepQuery(req.Include, query), fff.GrepOptions{
			PageLimit:           req.PageLimit,
			TimeBudget:          timeout,
			AfterContext:        req.AfterContext,
			ClassifyDefinitions: true,
		})
		result.Matches = matches
		result.Metrics = metrics
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
		query = strings.TrimSpace(query)
		if query == "" || !needsFileSearchFallback(query, contentResults) {
			continue
		}
		result := searchipc.SearchQueryResult{
			Query: query,
			Mode:  "files",
		}
		items, metrics, err := inst.SearchWithOptions(buildFFFSearchQuery(req.Include, query), pageSize, 0)
		result.Items = items
		result.Metrics = metrics
		if err != nil {
			result.Error = strings.TrimSpace(err.Error())
		}
		results = append(results, result)
	}
	return results
}

func needsFileSearchFallback(query string, contentResults []searchipc.GrepQueryResult) bool {
	for _, result := range contentResults {
		if strings.EqualFold(strings.TrimSpace(result.Query), query) {
			return strings.TrimSpace(result.Error) != "" || result.Metrics.TotalMatched == 0
		}
	}
	return true
}

func encodeResponse(resp searchipc.Response) error {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("encode search response: %w", err)
	}
	return nil
}

func buildFFFGrepQuery(include, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	include = strings.TrimSpace(include)
	if include == "" {
		return pattern
	}
	return include + " " + pattern
}

func buildFFFSearchQuery(include, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	include = strings.TrimSpace(include)
	if include == "" {
		return pattern
	}
	return include + " " + pattern
}

func compactQueries(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" {
			out = append(out, query)
		}
	}
	return out
}
