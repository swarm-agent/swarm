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
		Completed: true,
		Content:   runContentSearch(inst, req, timeout),
	}
	if strings.TrimSpace(resp.Content.Error) != "" {
		resp.FileResults = runFileSearches(inst, req)
	}
	return encodeResponse(resp)
}

func runContentSearch(inst *fff.Instance, req searchipc.Request, timeout time.Duration) searchipc.GrepQueryResult {
	result := searchipc.GrepQueryResult{
		Query: strings.TrimSpace(req.Queries[0]),
		Mode:  "content",
	}
	if len(req.Queries) == 1 {
		matches, metrics, err := inst.GrepWithConfig(buildFFFGrepQuery(req.Include, req.Queries[0]), fff.GrepOptions{
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
		return result
	}

	matches, metrics, err := inst.MultiGrepWithOptions(req.Queries, strings.TrimSpace(req.Include), req.PageLimit, timeout, 0, 0, req.AfterContext, true)
	result.Matches = matches
	result.Metrics = metrics
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
	}
	return result
}

func runFileSearches(inst *fff.Instance, req searchipc.Request) []searchipc.SearchQueryResult {
	results := make([]searchipc.SearchQueryResult, 0, len(req.Queries))
	pageSize := uint32(req.MaxResults + 1)
	for _, query := range req.Queries {
		result := searchipc.SearchQueryResult{
			Query: strings.TrimSpace(query),
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
