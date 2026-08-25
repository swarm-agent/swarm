package videorender

import (
	"strings"
	"testing"
)

func TestNormalizeInspectionTimestampsCanonicalBounds(t *testing.T) {
	got, err := normalizeInspectionTimestamps(FrameInspectionRequest{
		TimestampsMs: []int64{900, 100, 900},
		Ranges: []FrameInspectionRange{{StartMs: 1_000, EndMs: 2_000, Count: 3}},
	}, 3_000)
	if err != nil { t.Fatal(err) }
	want := []int64{100, 900, 1_000, 1_500, 2_000}
	if len(got) != len(want) { t.Fatalf("timestamps = %#v", got) }
	for index := range want { if got[index] != want[index] { t.Fatalf("timestamps = %#v", got) } }
}

func TestNormalizeInspectionTimestampsRejectsUnboundedRequests(t *testing.T) {
	cases := []struct { request FrameInspectionRequest; duration int64 }{
		{FrameInspectionRequest{}, 3_000},
		{FrameInspectionRequest{TimestampsMs: []int64{3_000}}, 3_000},
		{FrameInspectionRequest{Ranges: []FrameInspectionRange{{StartMs: 0, EndMs: MaxInspectionRangeMs + 1, Count: 2}}}, MaxInspectionRangeMs + 2},
		{FrameInspectionRequest{TimestampsMs: []int64{0, MaxInspectionSpanMs + 1}}, MaxInspectionSpanMs + 2},
	}
	for _, item := range cases {
		request := item.request
		if _, err := normalizeInspectionTimestamps(request, item.duration); err == nil || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("request %+v was not rejected: %v", request, err)
		}
	}
}
