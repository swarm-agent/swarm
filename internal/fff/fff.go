package fff

/*
#cgo linux,amd64 CFLAGS: -I${SRCDIR}/include
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/lib/linux-amd64-gnu -lfff_c -Wl,-rpath,${SRCDIR}/lib/linux-amd64-gnu
#include "fff.h"
#include <stdlib.h>
#include <stdint.h>
*/
import "C"

import (
	"fmt"
	"strings"
	"time"
	"unsafe"
)

type ResultEnvelope struct {
	ptr *C.struct_FffResult
}

func (r *ResultEnvelope) free() {
	if r == nil || r.ptr == nil {
		return
	}
	C.fff_free_result(r.ptr)
	r.ptr = nil
}

func cString(s string) *C.char {
	return C.CString(s)
}

func fromCString(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func freeOwnedCString(p *C.char) {
	if p == nil {
		return
	}
	C.fff_free_string(p)
}

func wrapResult(ptr *C.struct_FffResult) (*ResultEnvelope, error) {
	if ptr == nil {
		return nil, fmt.Errorf("nil FFF result")
	}
	res := &ResultEnvelope{ptr: ptr}
	if !bool(ptr.success) {
		err := fromCString(ptr.error)
		res.free()
		if err == "" {
			err = "unknown FFF error"
		}
		return nil, fmt.Errorf("%s", err)
	}
	return res, nil
}

type Instance struct {
	handle unsafe.Pointer
}

type CreateMetrics struct {
	CreateDuration time.Duration
}

type CreateOptions struct {
	FrecencyDBPath  string
	HistoryDBPath   string
	UseUnsafeNoLock bool
	WarmupMmapCache bool
	DisableAIMode   bool
}

func Create(basePath string, warmupMmapCache bool) (*Instance, CreateMetrics, error) {
	return CreateWithOptions(basePath, CreateOptions{WarmupMmapCache: warmupMmapCache})
}

func CreateWithOptions(basePath string, opts CreateOptions) (*Instance, CreateMetrics, error) {
	start := time.Now()
	cBase := cString(basePath)
	defer C.free(unsafe.Pointer(cBase))

	var cFrecency *C.char
	if strings.TrimSpace(opts.FrecencyDBPath) != "" {
		cFrecency = cString(opts.FrecencyDBPath)
		defer C.free(unsafe.Pointer(cFrecency))
	}

	var cHistory *C.char
	if strings.TrimSpace(opts.HistoryDBPath) != "" {
		cHistory = cString(opts.HistoryDBPath)
		defer C.free(unsafe.Pointer(cHistory))
	}

	res, err := wrapResult(C.fff_create_instance(cBase, cFrecency, cHistory, C.bool(opts.UseUnsafeNoLock), C.bool(opts.WarmupMmapCache), C.bool(!opts.DisableAIMode)))
	if err != nil {
		return nil, CreateMetrics{}, err
	}
	defer res.free()
	if res.ptr.handle == nil {
		return nil, CreateMetrics{}, fmt.Errorf("fff_create_instance returned nil handle")
	}
	inst := &Instance{handle: res.ptr.handle}
	return inst, CreateMetrics{CreateDuration: time.Since(start)}, nil
}

func (i *Instance) Destroy() {
	if i == nil || i.handle == nil {
		return
	}
	C.fff_destroy(i.handle)
	i.handle = nil
}

func (i *Instance) WaitForScan(timeout time.Duration) (bool, time.Duration, error) {
	if i == nil || i.handle == nil {
		return false, 0, fmt.Errorf("nil FFF instance")
	}
	start := time.Now()
	ms := timeout.Milliseconds()
	res, err := wrapResult(C.fff_wait_for_scan(i.handle, C.uint64_t(ms)))
	if err != nil {
		return false, time.Since(start), err
	}
	defer res.free()
	return res.ptr.int_value != 0, time.Since(start), nil
}

type SearchItem struct {
	Path                      string
	RelativePath              string
	FileName                  string
	GitStatus                 string
	Size                      uint64
	Modified                  uint64
	AccessFrecencyScore       int64
	ModificationFrecencyScore int64
	TotalFrecencyScore        int64
	IsBinary                  bool
	Score                     int
}

type SearchMetrics struct {
	Duration     time.Duration
	Count        uint32
	TotalMatched uint32
	TotalFiles   uint32
}

func (i *Instance) Search(query string, pageSize uint32) ([]SearchItem, SearchMetrics, error) {
	return i.SearchWithOptions(query, pageSize, 0)
}

func (i *Instance) SearchWithOptions(query string, pageSize uint32, pageIndex uint32) ([]SearchItem, SearchMetrics, error) {
	if i == nil || i.handle == nil {
		return nil, SearchMetrics{}, fmt.Errorf("nil FFF instance")
	}
	start := time.Now()
	cQuery := cString(query)
	defer C.free(unsafe.Pointer(cQuery))
	res, err := wrapResult(C.fff_search(i.handle, cQuery, nil, 0, C.uint32_t(pageIndex), C.uint32_t(pageSize), 0, 0))
	if err != nil {
		return nil, SearchMetrics{Duration: time.Since(start)}, err
	}
	defer res.free()

	searchRes := (*C.struct_FffSearchResult)(res.ptr.handle)
	if searchRes == nil {
		return nil, SearchMetrics{Duration: time.Since(start)}, fmt.Errorf("nil search result")
	}
	defer C.fff_free_search_result(searchRes)

	items := make([]SearchItem, 0, int(searchRes.count))
	for idx := C.uint32_t(0); idx < searchRes.count; idx++ {
		item := C.fff_search_result_get_item(searchRes, idx)
		score := C.fff_search_result_get_score(searchRes, idx)
		if item == nil {
			continue
		}
		entry := SearchItem{
			Path:                      fromCString(item.path),
			RelativePath:              fromCString(item.relative_path),
			FileName:                  fromCString(item.file_name),
			GitStatus:                 fromCString(item.git_status),
			Size:                      uint64(item.size),
			Modified:                  uint64(item.modified),
			AccessFrecencyScore:       int64(item.access_frecency_score),
			ModificationFrecencyScore: int64(item.modification_frecency_score),
			TotalFrecencyScore:        int64(item.total_frecency_score),
			IsBinary:                  bool(item.is_binary),
		}
		if score != nil {
			entry.Score = int(score.total)
		}
		items = append(items, entry)
	}
	metrics := SearchMetrics{
		Duration:     time.Since(start),
		Count:        uint32(searchRes.count),
		TotalMatched: uint32(searchRes.total_matched),
		TotalFiles:   uint32(searchRes.total_files),
	}
	return items, metrics, nil
}

type MatchRange struct {
	Start uint32
	End   uint32
}

type GrepMatch struct {
	Path                      string
	RelativePath              string
	FileName                  string
	GitStatus                 string
	LineNumber                uint64
	ByteOffset                uint64
	Column                    uint32
	LineContent               string
	MatchRanges               []MatchRange
	ContextBefore             []string
	ContextAfter              []string
	Size                      uint64
	Modified                  uint64
	TotalFrecencyScore        int64
	AccessFrecencyScore       int64
	ModificationFrecencyScore int64
	FuzzyScore                uint16
	HasFuzzyScore             bool
	IsBinary                  bool
	IsDefinition              bool
}

type GrepMetrics struct {
	Duration           time.Duration
	Count              uint32
	TotalMatched       uint32
	TotalFilesSearched uint32
	TotalFiles         uint32
	FilteredFileCount  uint32
	NextFileOffset     uint32
	RegexFallbackError string
}

type GrepOptions struct {
	PageLimit           uint32
	TimeBudget          time.Duration
	FileOffset          uint32
	Mode                uint8
	MaxFileSize         uint64
	MaxMatchesPerFile   uint32
	DisableSmartCase    bool
	BeforeContext       uint32
	AfterContext        uint32
	ClassifyDefinitions bool
}

func (i *Instance) Grep(query string, pageLimit uint32) ([]GrepMatch, GrepMetrics, error) {
	return i.GrepWithOptions(query, pageLimit, 0, 0, 0)
}

func (i *Instance) GrepWithOptions(query string, pageLimit uint32, timeBudget time.Duration, fileOffset uint32, mode uint8) ([]GrepMatch, GrepMetrics, error) {
	return i.GrepWithConfig(query, GrepOptions{
		PageLimit:  pageLimit,
		TimeBudget: timeBudget,
		FileOffset: fileOffset,
		Mode:       mode,
	})
}

func (i *Instance) GrepWithConfig(query string, opts GrepOptions) ([]GrepMatch, GrepMetrics, error) {
	if i == nil || i.handle == nil {
		return nil, GrepMetrics{}, fmt.Errorf("nil FFF instance")
	}
	start := time.Now()
	cQuery := cString(query)
	defer C.free(unsafe.Pointer(cQuery))
	budgetMS := uint64(0)
	if opts.TimeBudget > 0 {
		budgetMS = uint64(opts.TimeBudget / time.Millisecond)
	}
	pageLimit := opts.PageLimit
	if pageLimit == 0 {
		pageLimit = 50
	}
	res, err := wrapResult(C.fff_live_grep(
		i.handle,
		cQuery,
		C.uint8_t(opts.Mode),
		C.uint64_t(opts.MaxFileSize),
		C.uint32_t(opts.MaxMatchesPerFile),
		C.bool(!opts.DisableSmartCase),
		C.uint32_t(opts.FileOffset),
		C.uint32_t(pageLimit),
		C.uint64_t(budgetMS),
		C.uint32_t(opts.BeforeContext),
		C.uint32_t(opts.AfterContext),
		C.bool(opts.ClassifyDefinitions),
	))
	if err != nil {
		return nil, GrepMetrics{Duration: time.Since(start)}, err
	}
	defer res.free()
	return extractGrepResult((*C.struct_FffGrepResult)(res.ptr.handle), start)
}

func (i *Instance) MultiGrep(patterns []string, constraints string, pageLimit uint32) ([]GrepMatch, GrepMetrics, error) {
	return i.MultiGrepWithOptions(patterns, constraints, pageLimit, 0, 0, 0, 0, false)
}

func (i *Instance) MultiGrepWithOptions(patterns []string, constraints string, pageLimit uint32, timeBudget time.Duration, fileOffset uint32, beforeContext uint32, afterContext uint32, classifyDefinitions bool) ([]GrepMatch, GrepMetrics, error) {
	if i == nil || i.handle == nil {
		return nil, GrepMetrics{}, fmt.Errorf("nil FFF instance")
	}
	cleanPatterns := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		cleanPatterns = append(cleanPatterns, pattern)
	}
	if len(cleanPatterns) == 0 {
		return nil, GrepMetrics{}, fmt.Errorf("multi-grep requires at least one non-empty pattern")
	}

	start := time.Now()
	cPatterns := cString(strings.Join(cleanPatterns, "\n"))
	defer C.free(unsafe.Pointer(cPatterns))
	cConstraints := cString(constraints)
	defer C.free(unsafe.Pointer(cConstraints))
	budgetMS := uint64(0)
	if timeBudget > 0 {
		budgetMS = uint64(timeBudget / time.Millisecond)
	}
	res, err := wrapResult(C.fff_multi_grep(i.handle, cPatterns, cConstraints, 0, 0, true, C.uint32_t(fileOffset), C.uint32_t(pageLimit), C.uint64_t(budgetMS), C.uint32_t(beforeContext), C.uint32_t(afterContext), C.bool(classifyDefinitions)))
	if err != nil {
		return nil, GrepMetrics{Duration: time.Since(start)}, err
	}
	defer res.free()
	return extractGrepResult((*C.struct_FffGrepResult)(res.ptr.handle), start)
}

type ScanProgress struct {
	ScannedFilesCount uint64
	IsScanning        bool
}

func (i *Instance) ScanFiles() error {
	if i == nil || i.handle == nil {
		return fmt.Errorf("nil FFF instance")
	}
	res, err := wrapResult(C.fff_scan_files(i.handle))
	if err != nil {
		return err
	}
	defer res.free()
	return nil
}

func (i *Instance) IsScanning() (bool, error) {
	if i == nil || i.handle == nil {
		return false, fmt.Errorf("nil FFF instance")
	}
	return bool(C.fff_is_scanning(i.handle)), nil
}

func (i *Instance) GetScanProgress() (ScanProgress, error) {
	if i == nil || i.handle == nil {
		return ScanProgress{}, fmt.Errorf("nil FFF instance")
	}
	res, err := wrapResult(C.fff_get_scan_progress(i.handle))
	if err != nil {
		return ScanProgress{}, err
	}
	defer res.free()

	progress := (*C.struct_FffScanProgress)(res.ptr.handle)
	if progress == nil {
		return ScanProgress{}, fmt.Errorf("nil scan progress")
	}
	defer C.fff_free_scan_progress(progress)
	return ScanProgress{
		ScannedFilesCount: uint64(progress.scanned_files_count),
		IsScanning:        bool(progress.is_scanning),
	}, nil
}

func (i *Instance) RestartIndex(newPath string) error {
	if i == nil || i.handle == nil {
		return fmt.Errorf("nil FFF instance")
	}
	cPath := cString(newPath)
	defer C.free(unsafe.Pointer(cPath))
	res, err := wrapResult(C.fff_restart_index(i.handle, cPath))
	if err != nil {
		return err
	}
	defer res.free()
	return nil
}

func (i *Instance) RefreshGitStatus() (int64, error) {
	if i == nil || i.handle == nil {
		return 0, fmt.Errorf("nil FFF instance")
	}
	res, err := wrapResult(C.fff_refresh_git_status(i.handle))
	if err != nil {
		return 0, err
	}
	defer res.free()
	return int64(res.ptr.int_value), nil
}

func (i *Instance) TrackQuery(query, filePath string) (bool, error) {
	if i == nil || i.handle == nil {
		return false, fmt.Errorf("nil FFF instance")
	}
	cQuery := cString(query)
	defer C.free(unsafe.Pointer(cQuery))
	cFilePath := cString(filePath)
	defer C.free(unsafe.Pointer(cFilePath))
	res, err := wrapResult(C.fff_track_query(i.handle, cQuery, cFilePath))
	if err != nil {
		return false, err
	}
	defer res.free()
	return res.ptr.int_value != 0, nil
}

func (i *Instance) GetHistoricalQuery(offset uint64) (string, bool, error) {
	if i == nil || i.handle == nil {
		return "", false, fmt.Errorf("nil FFF instance")
	}
	res, err := wrapResult(C.fff_get_historical_query(i.handle, C.uint64_t(offset)))
	if err != nil {
		return "", false, err
	}
	defer res.free()
	if res.ptr.handle == nil {
		return "", false, nil
	}
	value := (*C.char)(res.ptr.handle)
	defer freeOwnedCString(value)
	return fromCString(value), true, nil
}

func (i *Instance) HealthCheck(testPath string) (string, error) {
	if i == nil || i.handle == nil {
		return "", fmt.Errorf("nil FFF instance")
	}
	cPath := cString(testPath)
	defer C.free(unsafe.Pointer(cPath))
	res, err := wrapResult(C.fff_health_check(i.handle, cPath))
	if err != nil {
		return "", err
	}
	defer res.free()
	if res.ptr.handle == nil {
		return "", nil
	}
	value := (*C.char)(res.ptr.handle)
	defer freeOwnedCString(value)
	return fromCString(value), nil
}

func extractGrepResult(grepRes *C.struct_FffGrepResult, start time.Time) ([]GrepMatch, GrepMetrics, error) {
	if grepRes == nil {
		return nil, GrepMetrics{Duration: time.Since(start)}, fmt.Errorf("nil grep result")
	}
	defer C.fff_free_grep_result(grepRes)

	items := make([]GrepMatch, 0, int(grepRes.count))
	for idx := C.uint32_t(0); idx < grepRes.count; idx++ {
		match := C.fff_grep_result_get_match(grepRes, idx)
		if match == nil {
			continue
		}
		items = append(items, GrepMatch{
			Path:                      fromCString(match.path),
			RelativePath:              fromCString(match.relative_path),
			FileName:                  fromCString(match.file_name),
			GitStatus:                 fromCString(match.git_status),
			LineNumber:                uint64(match.line_number),
			ByteOffset:                uint64(match.byte_offset),
			Column:                    uint32(match.col),
			LineContent:               fromCString(match.line_content),
			MatchRanges:               parseMatchRanges(match.match_ranges, match.match_ranges_count),
			ContextBefore:             parseCStringArray(match.context_before, match.context_before_count),
			ContextAfter:              parseCStringArray(match.context_after, match.context_after_count),
			Size:                      uint64(match.size),
			Modified:                  uint64(match.modified),
			TotalFrecencyScore:        int64(match.total_frecency_score),
			AccessFrecencyScore:       int64(match.access_frecency_score),
			ModificationFrecencyScore: int64(match.modification_frecency_score),
			FuzzyScore:                uint16(match.fuzzy_score),
			HasFuzzyScore:             bool(match.has_fuzzy_score),
			IsBinary:                  bool(match.is_binary),
			IsDefinition:              bool(match.is_definition),
		})
	}
	metrics := GrepMetrics{
		Duration:           time.Since(start),
		Count:              uint32(grepRes.count),
		TotalMatched:       uint32(grepRes.total_matched),
		TotalFilesSearched: uint32(grepRes.total_files_searched),
		TotalFiles:         uint32(grepRes.total_files),
		FilteredFileCount:  uint32(grepRes.filtered_file_count),
		NextFileOffset:     uint32(grepRes.next_file_offset),
		RegexFallbackError: fromCString(grepRes.regex_fallback_error),
	}
	return items, metrics, nil
}

func parseMatchRanges(base *C.struct_FffMatchRange, count C.uint32_t) []MatchRange {
	if base == nil || count == 0 {
		return nil
	}
	raw := unsafe.Slice(base, int(count))
	out := make([]MatchRange, 0, len(raw))
	for _, entry := range raw {
		out = append(out, MatchRange{Start: uint32(entry.start), End: uint32(entry.end)})
	}
	return out
}

func parseCStringArray(base **C.char, count C.uint32_t) []string {
	if base == nil || count == 0 {
		return nil
	}
	raw := unsafe.Slice(base, int(count))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		out = append(out, fromCString(entry))
	}
	return out
}
