package design

import (
	"fmt"
	"strings"
)

const generateInstructions = `You are Swarm's Designer. You make one finished, polished web design as a single standalone HTML file.

Return only the document, inside one ` + "```html" + ` block.

Rules:
- One file: all CSS in <style>, all JavaScript in <script>. No build step.
- No external resources: no CDNs, web fonts, remote images, scripts or stylesheets. Use system font stacks, inline SVG, CSS gradients and shapes for visuals.
- It must load in a current browser with no JavaScript errors and no failed loads.
- Realistic content, not lorem ipsum. A complete design, not a wireframe. Responsive down to phone width.`

const repairInstructions = `You fix errors in an HTML document. You get the document and the exact problems a browser check reported.

Return only JSON, in one ` + "```json" + ` block: {"edits":[{"old":"exact text copied from the document","new":"replacement text"}]}

Rules:
- Each "old" must be copied exactly from the document and appear in it exactly once; include enough surrounding text to make it unique.
- Change only what fixes the listed problems. Do not restyle, reformat or rewrite anything else.
- At most 20 edits.`

const editInstructions = `You edit an existing HTML document as asked. You get the document and the requested change.

Return only JSON, in one ` + "```json" + ` block: {"edits":[{"old":"exact text copied from the document","new":"replacement text"}]}

Rules:
- Each "old" must be copied exactly from the document and appear in it exactly once; include enough surrounding text to make it unique.
- Change only what the request needs. Leave everything else byte for byte as it is.
- No external resources: no CDNs, web fonts, remote images, scripts or stylesheets.
- At most 20 edits.`

// variationAxes gives a batch visible range when the caller names no
// variations: design i follows axis i mod len.
var variationAxes = []string{
	"clean and minimal, typographic, lots of whitespace",
	"bold color blocks and big type",
	"dark theme with vivid accent colors",
	"editorial: serif headlines, magazine layout",
	"playful, with illustrations drawn in inline SVG",
	"dense and data-rich, dashboard feel",
	"soft gradients and frosted glass panels",
	"brutalist: raw grid, hard edges, mono type",
	"warm and friendly, rounded shapes, pastel palette",
	"premium and restrained, luxury brand feel",
}

// variationFor returns design index's variation direction, or "" for a
// single design with no variations named.
func variationFor(req Request, index int) string {
	if len(req.Variations) > 0 {
		return strings.TrimSpace(req.Variations[index%len(req.Variations)])
	}
	if req.Count <= 1 {
		return ""
	}
	return variationAxes[index%len(variationAxes)]
}

func generateInput(req Request, index int, variation string) string {
	var b strings.Builder
	b.WriteString("Brief:\n")
	b.WriteString(strings.TrimSpace(req.Brief))
	if style := strings.TrimSpace(req.Style); style != "" {
		b.WriteString("\n\nStyle for every design:\n")
		b.WriteString(style)
	}
	if req.Count > 1 {
		fmt.Fprintf(&b, "\n\nThis is design %d of %d; each design in the set must look clearly different.", index+1, req.Count)
	}
	if variation != "" {
		b.WriteString("\nDirection for this design: ")
		b.WriteString(variation)
	}
	return b.String()
}

func repairInput(html string, diags []Diagnostic) string {
	var b strings.Builder
	b.WriteString("Problems the browser check reported:\n")
	for i, d := range diags {
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, d.Kind, d.Message)
		if d.URL != "" {
			fmt.Fprintf(&b, " (url: %s)", d.URL)
		}
		if d.Line > 0 {
			fmt.Fprintf(&b, " (line %d, column %d)", d.Line, d.Column)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nDocument:\n```html\n")
	b.WriteString(html)
	b.WriteString("\n```")
	return b.String()
}

func editInput(brief, html string) string {
	return "Requested change:\n" + strings.TrimSpace(brief) + "\n\nDocument:\n```html\n" + html + "\n```"
}
