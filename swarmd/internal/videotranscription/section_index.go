package videotranscription

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	VideoSectionIndexSchemaVersion   = "video_section_index.v1"
	VideoSpliceManifestSchemaVersion = "video_splice_manifest.v1"
	videoSectionMinDurationMs        = int64(8_000)
	videoSectionMaxDurationMs        = int64(30_000)
	videoAutomaticCutConfidence      = 0.86
	videoSectionScoringVersion       = "video_section_scoring.v1"
)

type VideoIndexSource struct {
	SourceFingerprint       string `json:"source_fingerprint"`
	TranscriptRef           string `json:"transcript_ref"`
	TranscriptContentDigest string `json:"transcript_content_digest"`
	DurationMs              int64  `json:"duration_ms"`
}

type VideoEvidenceProvenance struct {
	TranscriptRef           string `json:"transcript_ref"`
	TranscriptContentDigest string `json:"transcript_content_digest"`
	FirstSegment            int    `json:"first_segment"`
	LastSegment             int    `json:"last_segment"`
}

type VideoEvidence struct {
	ID              string                  `json:"id"`
	StartMs         int64                   `json:"start_ms"`
	EndMs           int64                   `json:"end_ms"`
	Modality        string                  `json:"modality"`
	Text            string                  `json:"text"`
	Confidence      float64                 `json:"confidence"`
	ConfidenceBasis string                  `json:"confidence_basis"`
	Provenance      VideoEvidenceProvenance `json:"provenance"`
}

type VideoEvidenceRange struct {
	StartMs    int64    `json:"start_ms"`
	EndMs      int64    `json:"end_ms"`
	Modalities []string `json:"modalities"`
}

type VideoFrameAnchor struct {
	TimestampMs int64  `json:"timestamp_ms"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
}

type VideoSectionConfidence struct {
	Overall       float64 `json:"overall"`
	StartBoundary float64 `json:"start_boundary"`
	EndBoundary   float64 `json:"end_boundary"`
}

type VideoSection struct {
	ID             string                 `json:"id"`
	Order          int                    `json:"order"`
	StartMs        int64                  `json:"start_ms"`
	EndMs          int64                  `json:"end_ms"`
	Title          string                 `json:"title"`
	Summary        string                 `json:"summary"`
	Topics         []string               `json:"topics"`
	EvidenceRanges []VideoEvidenceRange   `json:"evidence_ranges"`
	FrameAnchors   []VideoFrameAnchor     `json:"frame_anchors"`
	Confidence     VideoSectionConfidence `json:"confidence"`
	Verification   string                 `json:"verification"`
}

type VideoSectionIndex struct {
	SchemaVersion string            `json:"schema_version"`
	Source        VideoIndexSource  `json:"source"`
	Sections      []VideoSection    `json:"sections"`
	Quality       VideoIndexQuality `json:"quality"`
}

type VideoIndexQuality struct {
	EvidenceCount        int     `json:"evidence_count"`
	DeduplicatedCount    int     `json:"deduplicated_count"`
	CoverageRatio        float64 `json:"coverage_ratio"`
	VerificationRequired bool    `json:"verification_required"`
	ScoringVersion       string  `json:"scoring_version"`
}

type VideoSpliceCut struct {
	AfterSectionID string             `json:"after_section_id"`
	TimestampMs    int64              `json:"timestamp_ms"`
	Confidence     float64            `json:"confidence"`
	Status         string             `json:"status"`
	Reasons        []string           `json:"reasons"`
	FrameAnchors   []VideoFrameAnchor `json:"frame_anchors"`
	ExactCutMethod string             `json:"exact_cut_method"`
}

type VideoSpliceManifest struct {
	SchemaVersion string           `json:"schema_version"`
	Source        VideoIndexSource `json:"source"`
	Cuts          []VideoSpliceCut `json:"cuts"`
	Automatic     bool             `json:"automatic"`
	FailureMode   string           `json:"failure_mode"`
	PolicyVersion string           `json:"policy_version"`
}

type boundaryCandidate struct {
	at         int64
	confidence float64
	reasons    []string
}

var videoWordPattern = regexp.MustCompile(`[\pL\pN][\pL\pN_-]*`)

// BuildVideoSectionIndex derives a compact, immutable-content-addressed navigation
// view from the durable transcript. It does not create a second transcript
// authority: every evidence record links back to exact transcript segments and
// the transcript content digest.
func BuildVideoSectionIndex(transcript pebblestore.NormalizedTranscript) (VideoSectionIndex, VideoSpliceManifest, error) {
	if transcript.Ref == "" || transcript.SourceFingerprint == "" || transcript.ContentDigest == "" || transcript.Metadata.DurationMs <= 0 || len(transcript.Segments) == 0 || transcript.Validation.State != pebblestore.TranscriptValidationValidated {
		return VideoSectionIndex{}, VideoSpliceManifest{}, errors.New("section indexing requires a durable validated transcript")
	}
	evidence, rawCount := BuildVideoEvidence(transcript, 0, transcript.Metadata.DurationMs)
	if len(evidence) == 0 {
		return VideoSectionIndex{}, VideoSpliceManifest{}, errors.New("section indexing requires bounded multimodal evidence")
	}
	source := VideoIndexSource{
		SourceFingerprint:       transcript.SourceFingerprint,
		TranscriptRef:           transcript.Ref,
		TranscriptContentDigest: transcript.ContentDigest,
		DurationMs:              transcript.Metadata.DurationMs,
	}
	candidates := scoreVideoBoundaries(transcript.Segments, transcript.Metadata.DurationMs)
	boundaries := selectVideoBoundaries(candidates, transcript.Metadata.DurationMs)
	sections := make([]VideoSection, 0, len(boundaries)-1)
	for index := 0; index+1 < len(boundaries); index++ {
		start, end := boundaries[index].at, boundaries[index+1].at
		sectionEvidence := evidenceInRange(evidence, start, end)
		startConfidence := boundaries[index].confidence
		if index == 0 {
			startConfidence = 1
		}
		endConfidence := boundaries[index+1].confidence
		if index+1 == len(boundaries)-1 {
			endConfidence = 1
		}
		overall := roundConfidence(0.45 + 0.25*startConfidence + 0.30*endConfidence)
		section := VideoSection{
			ID: fmt.Sprintf("sec_%04d", index+1), Order: index + 1, StartMs: start, EndMs: end,
			Title: sectionTitle(sectionEvidence, index+1), Summary: sectionSummary(sectionEvidence), Topics: sectionTopics(sectionEvidence),
			EvidenceRanges: sectionEvidenceRanges(sectionEvidence, start, end),
			FrameAnchors:   sectionFrameAnchors(start, end),
			Confidence:     VideoSectionConfidence{Overall: overall, StartBoundary: roundConfidence(startConfidence), EndBoundary: roundConfidence(endConfidence)},
			Verification:   "transcript_only",
		}
		sections = append(sections, section)
	}
	coverage := evidenceCoverage(evidence, transcript.Metadata.DurationMs)
	index := VideoSectionIndex{
		SchemaVersion: VideoSectionIndexSchemaVersion, Source: source, Sections: sections,
		Quality: VideoIndexQuality{EvidenceCount: len(evidence), DeduplicatedCount: maxInt(0, rawCount-len(evidence)), CoverageRatio: roundConfidence(coverage), ScoringVersion: videoSectionScoringVersion},
	}
	manifest := buildVideoSpliceManifest(source, sections, evidence, candidates)
	index.Quality.VerificationRequired = !manifest.Automatic
	return index, manifest, nil
}

// BuildVideoEvidence returns deduplicated, bounded evidence for a requested
// timeline range. Adjacent repeated speech/OCR/visual descriptions are folded
// independently, preserving exact source-segment provenance.
func BuildVideoEvidence(transcript pebblestore.NormalizedTranscript, startMs, endMs int64) ([]VideoEvidence, int) {
	if startMs < 0 {
		startMs = 0
	}
	if endMs <= 0 || endMs > transcript.Metadata.DurationMs {
		endMs = transcript.Metadata.DurationMs
	}
	if startMs >= endMs {
		return []VideoEvidence{}, 0
	}
	type modalityValue struct{ modality, text string }
	evidence := make([]VideoEvidence, 0, len(transcript.Segments)*2)
	rawCount := 0
	for segmentIndex, segment := range transcript.Segments {
		if segment.EndMs <= startMs || segment.StartMs >= endMs {
			continue
		}
		values := []modalityValue{{"speech", segment.Speech}, {"audio", segment.Audio}, {"visual", segment.Visual}, {"on_screen_text", segment.OnScreenText}}
		for _, value := range values {
			text := compactVideoText(value.text, 2_000)
			if text == "" {
				continue
			}
			rawCount++
			entryStart, entryEnd := maxInt64(startMs, segment.StartMs), minInt64(endMs, segment.EndMs)
			merged := false
			for previous := len(evidence) - 1; previous >= 0 && previous >= len(evidence)-4; previous-- {
				candidate := &evidence[previous]
				if candidate.Modality != value.modality || entryStart > candidate.EndMs+1_000 || !equivalentVideoText(candidate.Text, text) {
					continue
				}
				candidate.EndMs = maxInt64(candidate.EndMs, entryEnd)
				candidate.Provenance.LastSegment = segmentIndex
				if utf8.RuneCountInString(text) > utf8.RuneCountInString(candidate.Text) {
					candidate.Text = text
				}
				merged = true
				break
			}
			if merged {
				continue
			}
			evidence = append(evidence, VideoEvidence{
				StartMs: entryStart, EndMs: entryEnd, Modality: value.modality, Text: text,
				Confidence: modalityEvidenceConfidence(value.modality), ConfidenceBasis: "heuristic_modality_prior",
				Provenance: VideoEvidenceProvenance{TranscriptRef: transcript.Ref, TranscriptContentDigest: transcript.ContentDigest, FirstSegment: segmentIndex, LastSegment: segmentIndex},
			})
		}
	}
	sort.SliceStable(evidence, func(i, j int) bool {
		if evidence[i].StartMs == evidence[j].StartMs {
			return evidence[i].Modality < evidence[j].Modality
		}
		return evidence[i].StartMs < evidence[j].StartMs
	})
	for index := range evidence {
		evidence[index].ID = fmt.Sprintf("ev_%06d", index+1)
	}
	return evidence, rawCount
}

func scoreVideoBoundaries(segments []pebblestore.NormalizedTranscriptSegment, durationMs int64) []boundaryCandidate {
	candidates := []boundaryCandidate{{at: 0, confidence: 1, reasons: []string{"source_start"}}}
	for index := 1; index < len(segments); index++ {
		previous, current := segments[index-1], segments[index]
		at := current.StartMs
		if at <= 0 || at >= durationMs {
			continue
		}
		score := 0.12
		reasons := make([]string, 0, 4)
		visualChange := videoTextDistance(previous.Visual, current.Visual)
		ocrChange := videoTextDistance(previous.OnScreenText, current.OnScreenText)
		speechChange := videoTextDistance(previous.Speech, current.Speech)
		if visualChange >= 0.65 {
			score += 0.32
			reasons = append(reasons, "visual_change")
		} else if visualChange >= 0.35 {
			score += 0.17
		}
		if ocrChange >= 0.5 && strings.TrimSpace(previous.OnScreenText) != "" && strings.TrimSpace(current.OnScreenText) != "" {
			score += 0.28
			reasons = append(reasons, "on_screen_text_change")
		} else if ocrChange >= 0.4 {
			score += 0.13
		}
		if speechChange >= 0.75 && (strings.TrimSpace(previous.Speech) == "" || strings.TrimSpace(current.Speech) == "") {
			score += 0.14
			reasons = append(reasons, "speech_transition")
		} else if speechChange >= 0.55 {
			score += 0.08
		}
		if current.StartMs > previous.EndMs+500 {
			score += 0.24
			reasons = append(reasons, "timeline_gap")
		}
		candidates = append(candidates, boundaryCandidate{at: at, confidence: math.Min(0.98, score), reasons: reasons})
	}
	candidates = append(candidates, boundaryCandidate{at: durationMs, confidence: 1, reasons: []string{"source_end"}})
	return candidates
}

func selectVideoBoundaries(candidates []boundaryCandidate, durationMs int64) []boundaryCandidate {
	selected := []boundaryCandidate{{at: 0, confidence: 1, reasons: []string{"source_start"}}}
	for selected[len(selected)-1].at < durationMs {
		last := selected[len(selected)-1].at
		if durationMs-last <= videoSectionMaxDurationMs {
			break
		}
		windowStart := last + videoSectionMinDurationMs
		windowEnd := minInt64(last+videoSectionMaxDurationMs, durationMs-videoSectionMinDurationMs)
		best := boundaryCandidate{at: minInt64(last+videoSectionMaxDurationMs, durationMs), confidence: 0.45, reasons: []string{"maximum_section_duration"}}
		for _, candidate := range candidates {
			if candidate.at < windowStart || candidate.at > windowEnd {
				continue
			}
			if candidate.confidence > best.confidence || (candidate.confidence == best.confidence && candidate.at > best.at) {
				best = candidate
			}
		}
		if best.at <= last || best.at >= durationMs {
			break
		}
		selected = append(selected, best)
	}
	if selected[len(selected)-1].at != durationMs {
		selected = append(selected, boundaryCandidate{at: durationMs, confidence: 1, reasons: []string{"source_end"}})
	}
	return selected
}

func buildVideoSpliceManifest(source VideoIndexSource, sections []VideoSection, evidence []VideoEvidence, candidates []boundaryCandidate) VideoSpliceManifest {
	manifest := VideoSpliceManifest{SchemaVersion: VideoSpliceManifestSchemaVersion, Source: source, Automatic: true, FailureMode: "require_boundary_verification", PolicyVersion: videoSectionScoringVersion}
	for index := 0; index+1 < len(sections); index++ {
		at := sections[index].EndMs
		confidence := sections[index].Confidence.EndBoundary
		reasons := boundaryReasonsAt(candidates, at)
		if speechCrossesBoundary(evidence, at) {
			confidence = math.Min(confidence, 0.54)
			reasons = append(reasons, "speech_crosses_boundary")
		}
		status := "ready"
		if confidence < videoAutomaticCutConfidence {
			status = "verification_required"
			manifest.Automatic = false
		}
		manifest.Cuts = append(manifest.Cuts, VideoSpliceCut{
			AfterSectionID: sections[index].ID, TimestampMs: at, Confidence: roundConfidence(confidence), Status: status,
			Reasons: uniqueStrings(reasons), FrameAnchors: boundaryFrameAnchors(at, source.DurationMs), ExactCutMethod: "reencode_exact",
		})
	}
	return manifest
}

func evidenceInRange(evidence []VideoEvidence, start, end int64) []VideoEvidence {
	out := make([]VideoEvidence, 0)
	for _, item := range evidence {
		if item.EndMs > start && item.StartMs < end {
			out = append(out, item)
		}
	}
	return out
}

func sectionEvidenceRanges(evidence []VideoEvidence, start, end int64) []VideoEvidenceRange {
	modalities := make([]string, 0, 4)
	for _, item := range evidence {
		modalities = append(modalities, item.Modality)
	}
	return []VideoEvidenceRange{{StartMs: start, EndMs: end, Modalities: uniqueStrings(modalities)}}
}

func sectionFrameAnchors(start, end int64) []VideoFrameAnchor {
	middle := start + (end-start)/2
	return []VideoFrameAnchor{{TimestampMs: start, Kind: "start", Status: "not_requested"}, {TimestampMs: middle, Kind: "representative", Status: "not_requested"}, {TimestampMs: end, Kind: "boundary", Status: "not_requested"}}
}

func boundaryFrameAnchors(at, duration int64) []VideoFrameAnchor {
	values := []int64{maxInt64(0, at-1_000), at, minInt64(duration, at+1_000)}
	kinds := []string{"before", "boundary", "after"}
	out := make([]VideoFrameAnchor, 0, 3)
	for index, value := range values {
		out = append(out, VideoFrameAnchor{TimestampMs: value, Kind: kinds[index], Status: "not_requested"})
	}
	return out
}

func sectionTitle(evidence []VideoEvidence, order int) string {
	for _, modality := range []string{"on_screen_text", "speech", "visual", "audio"} {
		for _, item := range evidence {
			if item.Modality == modality {
				if title := compactVideoText(item.Text, 88); title != "" {
					return title
				}
			}
		}
	}
	return fmt.Sprintf("Section %d", order)
}

func sectionSummary(evidence []VideoEvidence) string {
	parts := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, item := range evidence {
		key := canonicalVideoText(item.Text)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, item.Text)
		if len(parts) == 3 {
			break
		}
	}
	return compactVideoText(strings.Join(parts, " "), 320)
}

func sectionTopics(evidence []VideoEvidence) []string {
	counts := map[string]int{}
	original := map[string]string{}
	for _, item := range evidence {
		for _, word := range videoWordPattern.FindAllString(item.Text, -1) {
			key := strings.ToLower(word)
			if len([]rune(key)) < 4 || videoStopWords[key] {
				continue
			}
			counts[key]++
			if _, ok := original[key]; !ok {
				original[key] = word
			}
		}
	}
	type scoredWord struct {
		key   string
		count int
	}
	words := make([]scoredWord, 0, len(counts))
	for key, count := range counts {
		words = append(words, scoredWord{key, count})
	}
	sort.Slice(words, func(i, j int) bool {
		if words[i].count == words[j].count {
			return words[i].key < words[j].key
		}
		return words[i].count > words[j].count
	})
	out := make([]string, 0, 5)
	for _, word := range words {
		out = append(out, original[word.key])
		if len(out) == 5 {
			break
		}
	}
	return out
}

var videoStopWords = map[string]bool{
	"about": true, "after": true, "again": true, "also": true, "before": true, "being": true, "during": true,
	"from": true, "into": true, "shows": true, "that": true, "their": true, "then": true, "there": true, "these": true,
	"this": true, "through": true, "using": true, "visible": true, "where": true, "while": true, "with": true,
}

func evidenceCoverage(evidence []VideoEvidence, duration int64) float64 {
	if duration <= 0 {
		return 0
	}
	type span struct{ start, end int64 }
	spans := make([]span, 0, len(evidence))
	for _, item := range evidence {
		spans = append(spans, span{item.StartMs, item.EndMs})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	covered, start, end := int64(0), int64(-1), int64(-1)
	for _, item := range spans {
		if start < 0 {
			start, end = item.start, item.end
			continue
		}
		if item.start <= end {
			end = maxInt64(end, item.end)
			continue
		}
		covered += end - start
		start, end = item.start, item.end
	}
	if start >= 0 {
		covered += end - start
	}
	return math.Min(1, float64(covered)/float64(duration))
}

func speechCrossesBoundary(evidence []VideoEvidence, at int64) bool {
	for _, item := range evidence {
		if item.Modality == "speech" && item.StartMs < at && item.EndMs > at {
			return true
		}
	}
	return false
}

func boundaryReasonsAt(candidates []boundaryCandidate, at int64) []string {
	for _, candidate := range candidates {
		if candidate.at == at {
			return append([]string(nil), candidate.reasons...)
		}
	}
	return []string{"derived_boundary"}
}

func modalityEvidenceConfidence(modality string) float64 {
	switch modality {
	case "speech":
		return 0.86
	case "on_screen_text":
		return 0.78
	case "visual":
		return 0.74
	case "audio":
		return 0.72
	default:
		return 0.65
	}
}

func equivalentVideoText(left, right string) bool {
	left, right = canonicalVideoText(left), canonicalVideoText(right)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	shorter, longer := left, right
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	return len(shorter)*100 >= len(longer)*85 && strings.Contains(longer, shorter)
}

func videoTextDistance(left, right string) float64 {
	leftWords, rightWords := wordSet(left), wordSet(right)
	if len(leftWords) == 0 && len(rightWords) == 0 {
		return 0
	}
	if len(leftWords) == 0 || len(rightWords) == 0 {
		return 1
	}
	intersection := 0
	for word := range leftWords {
		if rightWords[word] {
			intersection++
		}
	}
	union := len(leftWords) + len(rightWords) - intersection
	return 1 - float64(intersection)/float64(union)
}

func wordSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, word := range videoWordPattern.FindAllString(strings.ToLower(value), -1) {
		out[word] = true
	}
	return out
}

func canonicalVideoText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	space := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func compactVideoText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func roundConfidence(value float64) float64 {
	return math.Round(math.Max(0, math.Min(1, value))*1000) / 1000
}
func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
