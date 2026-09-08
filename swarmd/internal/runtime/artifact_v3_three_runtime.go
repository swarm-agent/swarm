package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/net/html"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// prepareArtifactV3Runtime changes ephemeral renderer bytes only. The runtime is
// read from the same installed Desktop distribution, never from authored files,
// a package resolver, a CDN, or a guessed development checkout.
func prepareArtifactV3Runtime(profile *pebblestore.SessionArtifactAnimationProfile, entry string, files map[string][]byte) error {
	if err := artifact.ValidateAnimationProfileSnapshot(profile); err != nil {
		return err
	}
	if profile == nil || profile.ProfileID == "motion_ui" {
		return nil
	}
	if profile.ProfileID != "spatial_3d" {
		return errors.New("native HTML preview supports only reviewed motion_ui and spatial_3d profiles")
	}
	tokens := html.NewTokenizer(bytes.NewReader(files[entry]))
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		tag := tokens.Token()
		if tag.Data == "base" {
			return errors.New("spatial_3d does not support authored base URLs")
		}
		if tag.Data == "script" {
			for _, attr := range tag.Attr {
				if attr.Key == "type" && strings.EqualFold(strings.TrimSpace(attr.Val), "importmap") {
					return errors.New("spatial_3d import maps are server-owned")
				}
			}
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "swarm-animation-runtime/") {
			return errors.New("authored files cannot replace the trusted animation runtime")
		}
	}
	dist := strings.TrimSpace(os.Getenv("SWARM_WEB_DIST_DIR"))
	if dist == "" {
		return errors.New("installed Desktop runtime is unavailable: SWARM_WEB_DIST_DIR is not configured")
	}
	root, err := os.OpenRoot(dist)
	if err != nil {
		return errors.New("installed Desktop runtime is unavailable")
	}
	defer root.Close()
	read := func(name string, limit int64) ([]byte, error) {
		f, err := root.Open("swarm-animation-runtime/" + name)
		if err != nil {
			return nil, errors.New("installed Three.js dependency graph is incomplete")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			return nil, errors.New("installed Three.js runtime exceeds reviewed file bounds")
		}
		body, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(body)) > limit {
			return nil, errors.New("installed Three.js runtime could not be read within bounds")
		}
		return body, nil
	}
	body, err := read("three-manifest.json", 4096)
	if err != nil {
		return err
	}
	var manifest struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if json.Unmarshal(body, &manifest) != nil || manifest.Version != profile.RuntimeVersion || len(manifest.Files) != 2 {
		return errors.New("installed Three.js runtime version or graph is not reviewed")
	}
	trusted := map[string][]byte{}
	for _, name := range []string{"three.module.js", "three.core.js"} {
		body, err := read(name, 4<<20)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if len(body) == 0 || manifest.Files[name] != hex.EncodeToString(sum[:]) {
			return errors.New("installed Three.js runtime integrity check failed")
		}
		trusted["swarm-animation-runtime/"+name] = body
	}
	// Relative import-map targets retain the private capture server's token prefix,
	// including when the authored entrypoint lives below the project root.
	prefix := "./"
	if dir := path.Dir(entry); dir != "." {
		prefix = strings.Repeat("../", len(strings.Split(dir, "/")))
	}
	moduleURL := prefix + "swarm-animation-runtime/three.module.js"
	encoded, _ := json.Marshal(map[string]any{"imports": map[string]string{"three": moduleURL}})
	config, _ := json.Marshal(map[string]any{"modules": map[string]string{"three": moduleURL}, "wasm": map[string]string{}})
	injection := []byte(`<script type="importmap">` + string(encoded) + `</script><script>globalThis.__SWARM_ANIMATION_RUNTIME__=` + string(config) + `;</script>`)
	source := files[entry]
	// Prepend ahead of every authored module/import map; no accepted source bytes
	// or immutable profile snapshot are rewritten in storage.
	lower := strings.ToLower(string(source))
	if start := strings.Index(lower, "<head"); start >= 0 {
		if end := strings.Index(lower[start:], ">"); end >= 0 {
			offset := start + end + 1
			files[entry] = []byte(string(source[:offset]) + string(injection) + string(source[offset:]))
		} else {
			return errors.New("native HTML head is malformed")
		}
	} else {
		return errors.New("spatial_3d requires an explicit HTML head")
	}
	for name, body := range trusted {
		files[name] = body
	}
	return nil
}
