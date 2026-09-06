package htmlcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type documentTile struct {
	Offset float64
	Next float64
}

func boundedCaptureLabel(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

// The request-wide tile budget bounds browser work and digest retention. Only
// the first tile PNG is retained for each semantic section; every tile is audited.
func captureDocumentSection(ctx context.Context, id string, width, height int, selectors []string, budget *int) ([]byte, []string, error) {
	var first []byte
	var digests []string
	tile := documentTile{}
	for {
		if *budget <= 0 {
			return nil, nil, NewError("capture_source_limit_exceeded", "document exceeds 64 scroll tiles; narrow the document or its section capture scope")
		}
		(*budget)--
		png, err := captureState(ctx, id, width, height, selectors, false, &tile)
		if err != nil {
			if failure, ok := err.(*Error); ok && len(selectors) == 1 {
				return nil, nil, NewError(failure.Code, fmt.Sprintf("%s; selector %q, vertical offset %.0fpx, viewport %dx%d", failure.SafeMessage, boundedCaptureLabel(selectors[0]), tile.Offset, width, height))
			}
			return nil, nil, err
		}
		if first == nil {
			first = png
		}
		sum := sha256.Sum256(png)
		digests = append(digests, hex.EncodeToString(sum[:]))
		if tile.Next == 0 {
			return first, digests, nil
		}
		if tile.Next <= tile.Offset {
			return nil, nil, NewError("capture_required_element_clipped", "document section scrolling made no progress")
		}
		tile.Offset = tile.Next
	}
}

// Runs only in the isolated renderer, after readiness, resource and blocker
// checks. It never resizes, unhides, reflows or removes authored content.
// Vertical overflow is allowed only through actual auto/scroll containers;
// hidden/clip ancestors and horizontal overflow remain rejection boundaries.
func documentTileScript(offset float64) string {
	return fmt.Sprintf(`
{
const offset=%f;
let node; try { node=document.querySelector(requiredSelectors[0]); } catch (_) { return {code:"capture_required_element_invalid"}; }
if(!node||!visible(node))return {code:"capture_required_element_missing"};
const nodes=Array.from(document.querySelectorAll('*'));
if(nodes.length>10000)return {code:"capture_source_limit_exceeded"};
const scrollable=n=>/^(auto|scroll)$/.test(getComputedStyle(n).overflowY);
const root=document.scrollingElement;
for(let p=node;p;p=p.parentElement){const s=getComputedStyle(p);if(s.visibility==='hidden'||s.visibility==='collapse'||Number(s.opacity)===0||s.contentVisibility==='hidden')return {code:"capture_required_element_missing"};}
if(document.documentElement.scrollWidth>width+1||document.body.scrollWidth>width+1)return {code:"capture_viewport_overflow"};
// Audit clipping in every visible descendant, including below-fold descendants.
for(const n of [node,...node.querySelectorAll('*')]){
 if(!visible(n))continue;
 const r=n.getBoundingClientRect();
 for(let p=n.parentElement;p;p=p.parentElement){
  const s=getComputedStyle(p),b=p.getBoundingClientRect();
  if(/^(hidden|clip)$/.test(s.overflowY)&&(r.top<b.top-1||r.bottom>b.bottom+1))return {code:"capture_required_element_clipped"};
  if(/^(hidden|clip|auto|scroll)$/.test(s.overflowX)&&(r.left<b.left-1||r.right>b.right+1))return {code:"capture_required_element_clipped"};
 }
}
let style=document.getElementById('__swarm_document_capture_style');
if(!style){style=document.createElement('style');style.id='__swarm_document_capture_style';style.textContent='*,*::before,*::after{animation:none!important;transition:none!important;scroll-behavior:auto!important;scroll-snap-type:none!important;caret-color:transparent!important;cursor:none!important}'+(needsOpaqueCanvas?'html{background:#fff!important}':'');document.head.append(style);}
// Reset actual scroll containers for deterministic independent section states.
for(const n of nodes)if(scrollable(n))n.scrollTop=0;
window.scrollTo(0,0);
const ownScroll=scrollable(node)&&node.scrollHeight>node.clientHeight+1;
const extent=ownScroll?node.scrollHeight:node.getBoundingClientRect().height;
if(offset>=extent)return {code:"capture_required_element_clipped"};
if(ownScroll)node.scrollTop=offset;
// Move the requested vertical point through nested scroll panes out to the root.
for(let p=node.parentElement;p;p=p.parentElement){
 const r=node.getBoundingClientRect(),b=p.getBoundingClientRect();
 if(p===root){if(!/^(hidden|clip)$/.test(getComputedStyle(p).overflowY))window.scrollBy(0,r.top+(ownScroll?0:offset));}
 else if(scrollable(p))p.scrollTop+=r.top+(ownScroll?0:offset)-b.top-p.clientTop;
}
const r=node.getBoundingClientRect();
let top=0,bottom=height,left=0,right=width;
for(let p=node.parentElement;p;p=p.parentElement){if(p===root||p===document.body)continue;const s=getComputedStyle(p),b=p.getBoundingClientRect();if(s.overflowY!=='visible'){top=Math.max(top,b.top+p.clientTop);bottom=Math.min(bottom,b.top+p.clientTop+p.clientHeight);}if(s.overflowX!=='visible'){left=Math.max(left,b.left+p.clientLeft);right=Math.min(right,b.left+p.clientLeft+p.clientWidth);}}
const start=ownScroll?r.top+node.clientTop:r.top+offset;
if(r.left<left-1||r.right>right+1||start<top-1||start>=bottom-1)return {code:"capture_required_element_clipped"};
const covered=ownScroll?Math.min(node.clientHeight,bottom-start):bottom-start;
if(covered<=0||(ownScroll&&node.scrollTop+covered<offset+1))return {code:"capture_required_element_clipped"};
const next=(ownScroll?node.scrollTop:offset)+covered;
return {code:"ok",next:next>=extent-1?0:next};
}
`, offset)
}
