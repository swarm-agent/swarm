package videorender

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritePPMColorFrameProducesBoundedFullCanvasBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background.ppm")
	if err := writePPMColorFrame(path, 64, 64, "blue"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const header = "P6\n64 64\n255\n"
	if len(body) != len(header)+64*64*3 || string(body[:len(header)]) != header {
		t.Fatalf("PPM frame length/header = %d %q", len(body), body[:min(len(body), len(header))])
	}
	if body[len(header)] != 0 || body[len(header)+1] != 0 || body[len(header)+2] != 255 {
		t.Fatalf("first pixel = %v", body[len(header):len(header)+3])
	}
}
