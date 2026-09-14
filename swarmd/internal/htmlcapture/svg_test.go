package htmlcapture

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/png"
	"os"
	"testing"
)

func TestChromedpRendererRasterizeSVGEmpty(t *testing.T) {
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	_, err := renderer.RasterizeSVG(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty svg bytes")
	}
}

func TestChromedpRendererRasterizeSVGUnavailable(t *testing.T) {
	renderer := NewChromedpRenderer("/nonexistent/chrome", t.TempDir())
	_, err := renderer.RasterizeSVG(context.Background(), []byte("<svg></svg>"))
	var captureErr *Error
	if !errors.As(err, &captureErr) || captureErr.Code != "capture_renderer_unavailable" {
		t.Fatalf("expected capture_renderer_unavailable, got: %v", err)
	}
}

func TestChromedpRendererRasterizeSVGWithSystemChrome(t *testing.T) {
	if _, err := os.Stat(SystemChromePath); err != nil {
		t.Skipf("system-managed Chrome unavailable: %v", err)
	}
	renderer := NewChromedpRenderer(SystemChromePath, t.TempDir())
	svgData := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200" viewBox="0 0 200 200">
		<rect width="200" height="200" fill="#2563eb"/>
		<circle cx="100" cy="100" r="50" fill="#ffffff"/>
	</svg>`)
	pngBytes, err := renderer.RasterizeSVG(context.Background(), svgData)
	if err != nil {
		t.Fatalf("RasterizeSVG failed: %v", err)
	}
	if len(pngBytes) == 0 {
		t.Fatal("expected non-empty png bytes")
	}
	// Check PNG magic header: \x89PNG\r\n\x1a\n
	pngHeader := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(pngBytes, pngHeader) {
		t.Fatalf("expected PNG header, got %x", pngBytes[:min(len(pngBytes), 8)])
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode PNG config: %v", err)
	}
	if format != "png" {
		t.Fatalf("expected format png, got %s", format)
	}
	if cfg.Width != 1024 || cfg.Height != 1024 {
		t.Fatalf("expected 1024x1024, got %dx%d", cfg.Width, cfg.Height)
	}
}
