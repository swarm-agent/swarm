package sandbox

import (
	"strings"
	"testing"
)

// Purpose: a build log that reaches its bound says so with an end marker
// (once) instead of silently stopping, and a caller watching the build
// (EnvironmentBuild.OnLog) receives exactly the text the log keeps. Threat:
// a log cut without notice, so a reader takes a missing setup step's output
// (a warm-build OK/FAILED line) as never printed. Boundary: envLog.add, the
// log every BuildEnvironment result and progress reader gets. Narrowest
// layer: the log type itself (the Docker build cannot run in unit tests).
func TestEnvironmentBuildLogMarksItsLimitAndStreamsWhatItKeeps(t *testing.T) {
	var streamed strings.Builder
	log := &envLog{emit: func(text string) { streamed.WriteString(text) }}
	line := strings.Repeat("y", 1023) + "\n"
	for i := 0; i < 200; i++ {
		log.add("%s", line)
	}
	kept := log.b.String()
	if !strings.HasSuffix(kept, envBuildLogFullMarker) || strings.Count(kept, envBuildLogFullMarker) != 1 {
		t.Fatalf("log past its bound has no single end marker (len %d)", len(kept))
	}
	if len(kept) > envBuildLogMax+len(line)+len(envBuildLogFullMarker) {
		t.Fatalf("log grew past its bound: %d bytes", len(kept))
	}
	if streamed.String() != kept {
		t.Fatalf("streamed log differs from the kept log: %d vs %d bytes", streamed.Len(), len(kept))
	}
}
