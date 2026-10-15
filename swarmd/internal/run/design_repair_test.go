package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"

	"image"
	"image/png"

	"swarm/packages/swarmd/internal/htmlcapture"
)

type designTestRenderer struct {
	call func(context.Context, htmlcapture.StandaloneRequest) error
}

func (r designTestRenderer) CaptureStandalone(ctx context.Context, req htmlcapture.StandaloneRequest) (htmlcapture.StandaloneResult, error) {
	if r.call != nil {
		if err := r.call(ctx, req); err != nil {
			return htmlcapture.StandaloneResult{}, err
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		return htmlcapture.StandaloneResult{}, err
	}
	digest := sha256.Sum256(req.HTML)
	return htmlcapture.StandaloneResult{PNG: b.Bytes(), SourceSHA256: hex.EncodeToString(digest[:])}, nil
}
