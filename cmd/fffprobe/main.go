package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/fff"
)

func main() {
	base := "."
	if len(os.Args) > 1 {
		base = os.Args[1]
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve base path: %v\n", err)
		os.Exit(1)
	}

	mode := "search"
	if len(os.Args) > 2 {
		mode = os.Args[2]
	}
	query := "service"
	if len(os.Args) > 3 {
		query = os.Args[3]
	}

	inst, createMetrics, err := fff.Create(absBase, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create FFF instance: %v\n", err)
		os.Exit(1)
	}
	defer inst.Destroy()

	completed, scanDuration, err := inst.WaitForScan(2 * time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wait for scan: %v\n", err)
		os.Exit(1)
	}
	if !completed {
		fmt.Fprintf(os.Stderr, "scan timed out after %s\n", scanDuration)
		os.Exit(2)
	}

	fmt.Printf("base=%s\n", absBase)
	fmt.Printf("mode=%q\n", mode)
	fmt.Printf("query=%q\n", query)
	fmt.Printf("create_ms=%d\n", createMetrics.CreateDuration.Milliseconds())
	fmt.Printf("scan_wait_ms=%d\n", scanDuration.Milliseconds())

	switch mode {
	case "search":
		items, searchMetrics, err := inst.Search(query, 10)
		if err != nil {
			fmt.Fprintf(os.Stderr, "search: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("search_ms=%d\n", searchMetrics.Duration.Milliseconds())
		fmt.Printf("search_count=%d\n", searchMetrics.Count)
		fmt.Printf("search_total_matched=%d\n", searchMetrics.TotalMatched)
		fmt.Printf("search_total_files=%d\n", searchMetrics.TotalFiles)
		for i, item := range items {
			fmt.Printf("result_%d=%s|score=%d\n", i+1, item.RelativePath, item.Score)
		}
	case "grep":
		items, grepMetrics, err := inst.Grep(query, 20)
		if err != nil {
			fmt.Fprintf(os.Stderr, "grep: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("grep_ms=%d\n", grepMetrics.Duration.Milliseconds())
		fmt.Printf("grep_count=%d\n", grepMetrics.Count)
		fmt.Printf("grep_total_matched=%d\n", grepMetrics.TotalMatched)
		fmt.Printf("grep_total_files_searched=%d\n", grepMetrics.TotalFilesSearched)
		fmt.Printf("grep_total_files=%d\n", grepMetrics.TotalFiles)
		fmt.Printf("grep_filtered_file_count=%d\n", grepMetrics.FilteredFileCount)
		fmt.Printf("grep_next_file_offset=%d\n", grepMetrics.NextFileOffset)
		if grepMetrics.RegexFallbackError != "" {
			fmt.Printf("grep_regex_fallback_error=%q\n", grepMetrics.RegexFallbackError)
		}
		for i, item := range items {
			fmt.Printf("match_%d=%s:%d:%d|%s\n", i+1, item.RelativePath, item.LineNumber, item.Column, item.LineContent)
		}
	case "grep-config":
		items, grepMetrics, err := inst.GrepWithConfig(query, fff.GrepOptions{
			PageLimit:           10,
			BeforeContext:       1,
			AfterContext:        3,
			ClassifyDefinitions: true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "grep-config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("grep_ms=%d\n", grepMetrics.Duration.Milliseconds())
		fmt.Printf("grep_count=%d\n", grepMetrics.Count)
		for i, item := range items {
			fmt.Printf("match_%d=%s:%d:%d|def=%t|before=%d|after=%d|%s\n", i+1, item.RelativePath, item.LineNumber, item.Column, item.IsDefinition, len(item.ContextBefore), len(item.ContextAfter), item.LineContent)
		}
	case "multigrep":
		patterns := splitPatterns(query)
		items, grepMetrics, err := inst.MultiGrepWithOptions(patterns, "", 20, 0, 0, 0, 0, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "multigrep: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("multigrep_patterns=%q\n", patterns)
		fmt.Printf("multigrep_ms=%d\n", grepMetrics.Duration.Milliseconds())
		fmt.Printf("multigrep_count=%d\n", grepMetrics.Count)
		for i, item := range items {
			fmt.Printf("match_%d=%s:%d:%d|def=%t|%s\n", i+1, item.RelativePath, item.LineNumber, item.Column, item.IsDefinition, item.LineContent)
		}
	case "health":
		value, err := inst.HealthCheck(absBase)
		if err != nil {
			fmt.Fprintf(os.Stderr, "health: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(value)
	case "progress":
		progress, err := inst.GetScanProgress()
		if err != nil {
			fmt.Fprintf(os.Stderr, "progress: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("scan_progress_files=%d\n", progress.ScannedFilesCount)
		fmt.Printf("scan_progress_active=%t\n", progress.IsScanning)
	case "track":
		ok, err := inst.TrackQuery(query, filepath.Join(absBase, "swarmd", "internal", "tool", "runtime.go"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "track: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("track_ok=%t\n", ok)
	case "history":
		value, ok, err := inst.GetHistoricalQuery(0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "history: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("history_ok=%t\n", ok)
		fmt.Printf("history_value=%q\n", value)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(1)
	}
}

func splitPatterns(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 && strings.TrimSpace(raw) != "" {
		out = append(out, strings.TrimSpace(raw))
	}
	return out
}
