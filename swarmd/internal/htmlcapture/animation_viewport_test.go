package htmlcapture

import (
	"errors"
	"testing"
)

// Requirement: auditAnimationViewport must reject visible overflow, not circle
// geometry clipped by an outer SVG viewport. Browser preflight is the narrowest
// layer that proves CSS/SVG clipping; arithmetic or DOM-string tests cannot.
// The sanitized deterministic fixture also checks rejection returns no preview.
func TestAnimationViewportClippedParticles(t *testing.T) {
	source := `<!doctype html><html><head><style>html,body{margin:0;width:100%;height:100%;overflow:hidden}svg{display:block;width:100%;height:100%}</style></head><body><svg viewBox="0 0 1920 1080" preserveAspectRatio="xMidYMid slice"></svg><script>
 const svg=document.querySelector('svg');
 for(let i=0;i<32;i++){const c=document.createElementNS('http://www.w3.org/2000/svg','circle');c.setAttribute('r',String(1+i%2));svg.append(c)}
 globalThis.__SWARM_ANIMATION_BIND__({version:'swarm.animation/v1',ready(){return {duration_ms:8000,fps:60}},seek(t){
 const x=Math.min(1,Math.max(0,t/4500)),smooth=x*x*(3-2*x);
 Array.from(svg.children).forEach((c,i)=>{const angle=i*2.399963,radius=330+(i%7)*51+65*(1-smooth);c.setAttribute('cx',960+Math.cos(angle)*radius*1.45);c.setAttribute('cy',485+Math.sin(angle)*radius*.78)});
 document.documentElement.dataset.swarmAnimationTimeMs=String(t);return {time_ms:t}}});</script></body></html>`
	result, err := preflightHTML(t, source, 8000, 60)
	if err != nil {
		t.Fatalf("clipped particles rejected: %v diagnostics=%+v", err, result.Diagnostics)
	}
	if len(result.PreviewPNG) == 0 {
		t.Fatal("no validated preview")
	}
}

// Requirement: containment must not become a blanket root-overflow exemption.
// auditAnimationViewport must still reject unclipped SVG and misplaced boxes,
// including fixed descendants escaping an HTML overflow ancestor.
func TestAnimationViewportClippingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		reject     bool
	}{
		{"svg-hidden", `<svg style="width:1920px;height:1080px;overflow:hidden"><circle cx="1930" cy="100" r="20"/></svg>`, false},
		{"svg-group-hidden", `<svg style="width:1920px;height:1080px;overflow:hidden"><g><circle cx="1930" cy="100" r="20"/></g></svg>`, false},
		{"svg-fully-clipped", `<svg style="width:1920px;height:1080px;overflow:hidden"><circle cx="2000" cy="100" r="20"/></svg>`, false},
		{"nested-svg-visible", `<svg style="width:1920px;height:1080px;overflow:visible"><svg x="1900" width="20" height="100" style="overflow:visible"><circle cx="30" cy="50" r="20"/></svg></svg>`, true},
		{"svg-visible", `<svg style="width:1920px;height:1080px;overflow:visible"><circle cx="1930" cy="100" r="20"/></svg>`, true},
		{"svg-box-outside", `<svg style="position:absolute;left:-10px;width:1920px;height:1080px;overflow:hidden"><circle cx="100" cy="100" r="20"/></svg>`, true},
		{"root-hidden-not-exemption", `<div style="position:fixed;left:-10px;top:0;width:20px;height:20px;background:red"></div>`, true},
		{"fixed-escapes-clip", `<div style="overflow:hidden;width:100px;height:100px"><div style="position:fixed;left:-10px;top:0;width:20px;height:20px;background:red"></div></div>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := `<!doctype html><html><head><style>html,body{margin:0;width:100%;height:100%;overflow:hidden}svg{display:block}</style></head><body>` + tc.body + `<script>globalThis.__SWARM_ANIMATION_BIND__({version:'swarm.animation/v1',ready(){return {duration_ms:400,fps:10}},seek(t){document.documentElement.dataset.swarmAnimationTimeMs=String(t);return {time_ms:t}}});</script></body></html>`
			result, err := preflightHTML(t, source, 400, 10)
			if !tc.reject {
				if err != nil {
					t.Fatalf("clipped geometry: %v %+v", err, result.Diagnostics)
				}
				return
			}
			var captureErr *Error
			if !errors.As(err, &captureErr) || captureErr.Code != "animation_viewport_overflow" || len(result.PreviewPNG) != 0 {
				t.Fatalf("overflow result=%+v err=%v", result, err)
			}
			for _, d := range result.Diagnostics {
				if d.Outcome == "bounds_overflow" && d.Selector != "" && d.TimestampMS != nil && d.Bounds != nil {
					return
				}
			}
			t.Fatalf("missing actionable bounds: %+v", result.Diagnostics)
		})
	}
}
