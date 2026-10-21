package videorender

import (
	"strings"
	"testing"
)

func TestEscapeFFmpegDrawText(t *testing.T) {
	input := `Line 1: 50% discount on Tom's \ special`
	escaped := escapeFFmpegDrawText(input)
	if strings.Contains(escaped, `50%`) {
		t.Errorf("%% was not escaped: %s", escaped)
	}
	if strings.Contains(escaped, `Tom's`) {
		t.Errorf("' was not escaped: %s", escaped)
	}
}
