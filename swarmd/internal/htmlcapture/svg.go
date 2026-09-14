package htmlcapture

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SVGRasterizer converts vector SVG images into raster PNG images.
type SVGRasterizer interface {
	RasterizeSVG(ctx context.Context, svgBytes []byte) ([]byte, error)
}

// RasterizeSVG renders SVG bytes into a PNG image using the system-managed Chromium renderer.
func (r *ChromedpRenderer) RasterizeSVG(ctx context.Context, svgBytes []byte) ([]byte, error) {
	if len(svgBytes) == 0 {
		return nil, errors.New("svg bytes are empty")
	}
	binaryPath := SystemChromePath
	cacheRoot := ""
	if r != nil {
		if strings.TrimSpace(r.BinaryPath) != "" && r.BinaryPath != "." {
			binaryPath = r.BinaryPath
		}
		cacheRoot = r.CacheRoot
	}
	info, err := os.Lstat(binaryPath)
	if err != nil {
		return nil, NewError("capture_renderer_unavailable", "system-managed sandboxed browser is unavailable")
	}
	stat, statOK := infoSysStat(info)
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || !statOK || stat.Uid != 0 {
		return nil, NewError("capture_renderer_unavailable", "system-managed sandboxed browser is unavailable")
	}
	if r != nil && r.sem != nil {
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return rasterizeSVGWithBrowser(ctx, binaryPath, cacheRoot, svgBytes)
}

func rasterizeSVGWithBrowser(ctx context.Context, binaryPath, cacheRoot string, svgBytes []byte) ([]byte, error) {
	if cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return nil, NewError("capture_renderer_unavailable", "private capture cache is unavailable")
	}
	jobDir, err := os.MkdirTemp(cacheRoot, "svg-raster-")
	if err != nil {
		return nil, NewError("capture_renderer_unavailable", "private capture job could not be created")
	}
	defer os.RemoveAll(jobDir)

	htmlContent := `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body {
    width: 100%;
    height: 100%;
    overflow: hidden;
    background: transparent;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  svg {
    max-width: 100%;
    max-height: 100%;
    width: 100%;
    height: 100%;
  }
</style>
</head>
<body>
` + string(svgBytes) + `
</body>
</html>`

	dataURI := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(htmlContent))
	outPNG := filepath.Join(jobDir, "output.png")
	profileDir := filepath.Join(jobDir, "profile")

	args := []string{
		"--headless",
		"--disable-gpu",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-scrollbars",
		"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
		"--user-data-dir=" + profileDir,
		"--screenshot=" + outPNG,
		"--window-size=1024,1024",
		dataURI,
	}
	if os.Geteuid() == 0 {
		args = append([]string{"--no-sandbox"}, args...)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return nil, NewError("capture_timeout", "svg rasterization timed out")
		}
		return nil, newErrorWithCause("capture_renderer_failed", "chrome svg rasterization failed", fmt.Errorf("%w: %s", err, string(output)))
	}

	pngData, err := os.ReadFile(outPNG)
	if err != nil {
		return nil, newErrorWithCause("capture_renderer_failed", "read rasterized svg png failed", err)
	}
	if len(pngData) == 0 {
		return nil, NewError("capture_renderer_failed", "rasterized svg output is empty")
	}
	return pngData, nil
}
