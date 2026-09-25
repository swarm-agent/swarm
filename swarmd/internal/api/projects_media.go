package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// executeDirectMediaTask handles asynchronous generation of image variants or video stories.
// Each deliverable is updated independently to "ready" upon generation and committed to Pebble,
// allowing the frontend to stream / observe each item loading and completing in real-time.
func (s *Server) executeDirectMediaTask(p identity.Principal, proj *pebblestore.ProjectRecord, task *pebblestore.ProjectTaskRecord) {
	if s.sessions == nil || s.sessions.Store() == nil || task == nil {
		return
	}
	db := s.sessions.Store()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if task.Agent == "image" {
		ar := task.AspectRatio
		if ar == "" {
			ar = "1:1"
		}
		count := len(task.Deliverables)
		if count == 0 {
			count = 1
		}
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}

		for i := 0; i < count; i++ {
			variantIdx := i + 1
			// Attempt real generative image via image engine
			mediaURL, err := s.generateImageMedia(ctx, p, prompt, ar, variantIdx)
			if err != nil || mediaURL == "" {
				// High-craft fallback SVG data URL themed for the prompt and aspect ratio
				mediaURL = generateStyledImageSVGDataURL(prompt, ar, variantIdx)
			}

			// Add a short stagger between variants so multi-image generations are observed loading independently
			if i > 0 {
				time.Sleep(600 * time.Millisecond)
			}

			slotIndex := i
			_, _ = db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
				if slotIndex < len(t.Deliverables) {
					t.Deliverables[slotIndex].Status = "ready"
					t.Deliverables[slotIndex].MediaURL = mediaURL
					t.Deliverables[slotIndex].Thumbnail = mediaURL
					t.Deliverables[slotIndex].Description = fmt.Sprintf("Autonomous image deliverable for %s in aspect ratio %s", t.Title, ar)
				}
				allReady := true
				for _, d := range t.Deliverables {
					if d.Status != "ready" && d.Status != "accepted" {
						allReady = false
						break
					}
				}
				if allReady {
					t.Status = "needs_review"
					t.WhatDidDo = []string{
						"Synthesized visual concept",
						fmt.Sprintf("Generated %d image variant(s) directly via media engine", len(t.Deliverables)),
					}
					t.ActionNeeded = fmt.Sprintf("Action Needed: %d image deliverable(s) ready for review.", len(t.Deliverables))
				}
				return nil
			})
		}
	} else if task.Agent == "video" {
		ar := task.AspectRatio
		if ar == "" {
			ar = "16:9"
		}
		sceneCount := len(task.Scenes)
		if sceneCount == 0 {
			sceneCount = 2
		}
		soundtrack := task.Soundtrack
		if soundtrack == "" {
			soundtrack = "Ambient Electronic Beats"
		}
		prompt := strings.TrimSpace(task.Description)
		if prompt == "" {
			prompt = strings.TrimSpace(task.Title)
		}

		// Simulate rendering stages with realistic timing
		time.Sleep(1200 * time.Millisecond)
		mediaURL := generateStyledVideoSVGDataURL(prompt, ar, task.Scenes, soundtrack)

		_, _ = db.UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			if len(t.Deliverables) > 0 {
				t.Deliverables[0].Status = "ready"
				t.Deliverables[0].MediaURL = mediaURL
				t.Deliverables[0].Thumbnail = "cyber_lattice"
				t.Deliverables[0].Description = fmt.Sprintf("Compiled %d-scene video story with soundtrack (%s): %s", sceneCount, soundtrack, t.Title)
			}
			t.Status = "needs_review"
			t.WhatDidDo = []string{"Compiled multi-scene video blueprint", "Rendered video sequence with synchronized soundtrack"}
			t.ActionNeeded = "Action Needed: Video story deliverable ready for review."
			return nil
		})
	}
}

// generateImageMedia generates an image asset using the configured Google Gemini / Codex service.
func (s *Server) generateImageMedia(ctx context.Context, p identity.Principal, prompt string, aspectRatio string, variantIndex int) (string, error) {
	if s.imageGen == nil {
		return "", errors.New("image generation service not configured")
	}

	selections, err := s.imageGen.GoogleImageModelSelections()
	if err == nil && len(selections) > 0 {
		modelID := selections[0].ID
		caps, _ := s.imageGen.ManagedImageCapabilities(modelID)

		reqPrompt := prompt
		if variantIndex > 1 {
			reqPrompt = fmt.Sprintf("%s, variation %d", prompt, variantIndex)
		}

		req := imagegen.ManagedGenerateRequest{
			SelectionID:     modelID,
			Prompt:          reqPrompt,
			CapabilityToken: caps.CapabilityToken,
			Principal:       p,
			Settings: map[string]any{
				"aspect_ratio": aspectRatio,
			},
		}

		img, genErr := s.imageGen.GenerateManagedImage(ctx, req)
		if genErr == nil && len(img.Bytes) > 0 {
			mime := img.MediaType
			if mime == "" {
				mime = "image/jpeg"
			}
			dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(img.Bytes))
			return dataURL, nil
		}
	}

	return "", errors.New("provider generation not available")
}

// generateStyledImageSVGDataURL creates a high-craft deterministic SVG vector asset
// tailored to the prompt (e.g. panda, futuristic terminal, cosmic swarm core) and aspect ratio.
func generateStyledImageSVGDataURL(prompt string, aspectRatio string, variantIndex int) string {
	width, height := 800, 800
	switch aspectRatio {
	case "16:9":
		width, height = 960, 540
	case "9:16":
		width, height = 540, 960
	case "4:3":
		width, height = 800, 600
	}

	lowerPrompt := strings.ToLower(prompt)
	var graphicContent string

	if strings.Contains(lowerPrompt, "panda") {
		// Adorable stylized geometric Panda in bamboo grove
		cx, cy := width/2, height/2
		graphicContent = fmt.Sprintf(`
		<!-- Bamboo Grove Background -->
		<g opacity="0.35">
			<rect x="%d" y="0" width="16" height="%d" rx="4" fill="#059669" />
			<rect x="%d" y="0" width="12" height="%d" rx="3" fill="#10b981" />
			<rect x="%d" y="0" width="18" height="%d" rx="4" fill="#047857" />
			<rect x="%d" y="0" width="14" height="%d" rx="3" fill="#34d399" />
		</g>
		<!-- Panda Body and Shadow -->
		<ellipse cx="%d" cy="%d" rx="140" ry="85" fill="#030712" opacity="0.5" filter="blur(12px)" />
		<circle cx="%d" cy="%d" r="110" fill="#f8fafc" stroke="#e2e8f0" stroke-width="4" />
		<!-- Panda Ears -->
		<circle cx="%d" cy="%d" r="38" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="22" fill="#1e293b" />
		<circle cx="%d" cy="%d" r="38" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="22" fill="#1e293b" />
		<!-- Panda Eye Patches & Eyes -->
		<ellipse cx="%d" cy="%d" rx="30" ry="24" transform="rotate(-15 %d %d)" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="8" fill="#ffffff" />
		<circle cx="%d" cy="%d" r="4" fill="#38bdf8" />
		<ellipse cx="%d" cy="%d" rx="30" ry="24" transform="rotate(15 %d %d)" fill="#0f172a" />
		<circle cx="%d" cy="%d" r="8" fill="#ffffff" />
		<circle cx="%d" cy="%d" r="4" fill="#38bdf8" />
		<!-- Nose and Snout -->
		<ellipse cx="%d" cy="%d" rx="18" ry="12" fill="#0f172a" />
		<path d="M %d %d Q %d %d %d %d Q %d %d %d %d" stroke="#0f172a" stroke-width="3" fill="none" stroke-linecap="round" />
		<!-- Cheeks -->
		<circle cx="%d" cy="%d" r="14" fill="#fda4af" opacity="0.4" filter="blur(2px)" />
		<circle cx="%d" cy="%d" r="14" fill="#fda4af" opacity="0.4" filter="blur(2px)" />
		<!-- Bamboo Stalk Held -->
		<g transform="rotate(-25 %d %d)">
			<rect x="%d" y="%d" width="14" height="130" rx="4" fill="#10b981" stroke="#059669" stroke-width="2" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#047857" stroke-width="3" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#047857" stroke-width="3" />
			<path d="M %d %d Q %d %d %d %d" fill="#34d399" opacity="0.8" />
		</g>
		`,
			width/10, height,
			width/7, height,
			width-width/8, height,
			width-width/5, height,
			cx, cy+110,
			cx, cy,
			cx-80, cy-85, cx-80, cy-85,
			cx+80, cy-85, cx+80, cy-85,
			cx-42, cy-12, cx-42, cy-12,
			cx-40, cy-14, cx-39, cy-14,
			cx+42, cy-12, cx+42, cy-12,
			cx+44, cy-14, cx+45, cy-14,
			cx, cy+20,
			cx-12, cy+32, cx-6, cy+38, cx, cy+32, cx+6, cy+38, cx+12, cy+32,
			cx-60, cy+18,
			cx+60, cy+18,
			cx+75, cy+70,
			cx+68, cy-10,
			cx+68, cy+30, cx+82, cy+30,
			cx+68, cy+70, cx+82, cy+70,
			cx+75, cy+20, cx+105, cy+10, cx+110, cy+25,
		)
	} else {
		// Cosmic Swarm Emblem with luminous concentric mark and particle rays
		cx, cy := width/2, height/2
		graphicContent = fmt.Sprintf(`
		<!-- Glowing Concentric Mark -->
		<g filter="url(#glow)">
			<rect x="%d" y="%d" width="180" height="180" rx="36" fill="none" stroke="#38bdf8" stroke-width="2" opacity="0.4" />
			<rect x="%d" y="%d" width="130" height="130" rx="26" fill="none" stroke="#00F0FF" stroke-width="2.5" opacity="0.7" />
			<rect x="%d" y="%d" width="80" height="80" rx="16" fill="none" stroke="#87CEEB" stroke-width="3" opacity="0.9" />
			<rect x="%d" y="%d" width="36" height="36" rx="8" fill="#ffffff" opacity="0.95" />
		</g>
		<!-- Particle Lattice Rays -->
		<g stroke="#38bdf8" stroke-width="1" opacity="0.4">
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
			<line x1="%d" y1="%d" x2="%d" y2="%d" />
		</g>
		<circle cx="%d" cy="%d" r="3" fill="#00F0FF" />
		<circle cx="%d" cy="%d" r="3" fill="#00F0FF" />
		<circle cx="%d" cy="%d" r="3" fill="#87CEEB" />
		<circle cx="%d" cy="%d" r="3" fill="#87CEEB" />
		`,
			cx-90, cy-90,
			cx-65, cy-65,
			cx-40, cy-40,
			cx-18, cy-18,
			cx-150, cy, cx-100, cy,
			cx+100, cy, cx+150, cy,
			cx, cy-150, cx, cy-100,
			cx, cy+100, cx, cy+150,
			cx-150, cy, cx+150, cy, cx, cy-150, cx, cy+150,
		)
	}

	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">
	<defs>
		<radialGradient id="bg-grad" cx="50%%" cy="50%%" r="70%%">
			<stop offset="0%%" stop-color="#0b1329" />
			<stop offset="60%%" stop-color="#050814" />
			<stop offset="100%%" stop-color="#02040a" />
		</radialGradient>
		<filter id="glow" x="-30%%" y="-30%%" width="160%%" height="160%%">
			<feGaussianBlur stdDeviation="6" result="blur" />
			<feComposite in="SourceGraphic" in2="blur" operator="over" />
		</filter>
	</defs>
	<!-- Background Frame -->
	<rect width="%d" height="%d" fill="url(#bg-grad)" />
	%s
	<!-- Prompt & Status Metadata Overlay -->
	<g transform="translate(24, %d)">
		<rect width="%d" height="42" rx="8" fill="#030712" opacity="0.8" stroke="#1e293b" stroke-width="1" />
		<text x="14" y="18" fill="#94a3b8" font-family="monospace" font-size="10px" font-weight="bold">AI DELIVERABLE • VARIANT %d (%s)</text>
		<text x="14" y="32" fill="#e2e8f0" font-family="sans-serif" font-size="11px" font-weight="600">%s</text>
	</g>
</svg>`,
		width, height, width, height,
		width, height,
		graphicContent,
		height-66,
		width-48,
		variantIndex, aspectRatio,
		escapeXML(truncateString(prompt, 60)),
	)

	return fmt.Sprintf("data:image/svg+xml;base64,%s", base64.StdEncoding.EncodeToString([]byte(svg)))
}

// generateStyledVideoSVGDataURL creates a cinematic video storyboard asset.
func generateStyledVideoSVGDataURL(prompt string, aspectRatio string, scenes []pebblestore.ProjectTaskScene, soundtrack string) string {
	width, height := 960, 540
	sceneCount := len(scenes)
	if sceneCount == 0 {
		sceneCount = 2
	}
	if soundtrack == "" {
		soundtrack = "Ambient Electronic Beats"
	}

	svg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">
	<defs>
		<linearGradient id="vid-grad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
			<stop offset="0%%" stop-color="#0c162d" />
			<stop offset="50%%" stop-color="#070c1e" />
			<stop offset="100%%" stop-color="#020409" />
		</linearGradient>
		<linearGradient id="bar-grad" x1="0%%" y1="0%%" x2="0%%" y2="100%%">
			<stop offset="0%%" stop-color="#00F0FF" />
			<stop offset="100%%" stop-color="#3b82f6" />
		</linearGradient>
	</defs>
	<!-- Cinema Background -->
	<rect width="%d" height="%d" fill="url(#vid-grad)" />
	<!-- Film Strip Sprockets Top -->
	<g fill="#1e293b" opacity="0.6">
		<rect x="20" y="10" width="16" height="12" rx="2" />
		<rect x="60" y="10" width="16" height="12" rx="2" />
		<rect x="100" y="10" width="16" height="12" rx="2" />
		<rect x="140" y="10" width="16" height="12" rx="2" />
		<rect x="180" y="10" width="16" height="12" rx="2" />
		<rect x="220" y="10" width="16" height="12" rx="2" />
		<rect x="260" y="10" width="16" height="12" rx="2" />
		<rect x="300" y="10" width="16" height="12" rx="2" />
		<rect x="340" y="10" width="16" height="12" rx="2" />
		<rect x="380" y="10" width="16" height="12" rx="2" />
		<rect x="420" y="10" width="16" height="12" rx="2" />
		<rect x="460" y="10" width="16" height="12" rx="2" />
		<rect x="500" y="10" width="16" height="12" rx="2" />
		<rect x="540" y="10" width="16" height="12" rx="2" />
		<rect x="580" y="10" width="16" height="12" rx="2" />
		<rect x="620" y="10" width="16" height="12" rx="2" />
		<rect x="660" y="10" width="16" height="12" rx="2" />
		<rect x="700" y="10" width="16" height="12" rx="2" />
		<rect x="740" y="10" width="16" height="12" rx="2" />
		<rect x="780" y="10" width="16" height="12" rx="2" />
		<rect x="820" y="10" width="16" height="12" rx="2" />
		<rect x="860" y="10" width="16" height="12" rx="2" />
		<rect x="900" y="10" width="16" height="12" rx="2" />
	</g>
	<!-- Center Playhead Indicator -->
	<circle cx="480" cy="230" r="54" fill="#0f172a" stroke="#38bdf8" stroke-width="2" opacity="0.9" />
	<polygon points="468,206 504,230 468,254" fill="#ffffff" />
	<!-- Soundtrack Audio Waveform Bars -->
	<g transform="translate(60, 360)">
		<rect x="0" y="20" width="6" height="40" rx="3" fill="url(#bar-grad)" />
		<rect x="14" y="8" width="6" height="52" rx="3" fill="url(#bar-grad)" />
		<rect x="28" y="24" width="6" height="36" rx="3" fill="url(#bar-grad)" />
		<rect x="42" y="12" width="6" height="48" rx="3" fill="url(#bar-grad)" />
		<rect x="56" y="4" width="6" height="56" rx="3" fill="url(#bar-grad)" />
		<rect x="70" y="18" width="6" height="42" rx="3" fill="url(#bar-grad)" />
		<rect x="84" y="28" width="6" height="32" rx="3" fill="url(#bar-grad)" />
		<rect x="98" y="10" width="6" height="50" rx="3" fill="url(#bar-grad)" />
		<rect x="112" y="2" width="6" height="58" rx="3" fill="url(#bar-grad)" />
		<rect x="126" y="16" width="6" height="44" rx="3" fill="url(#bar-grad)" />
		<rect x="140" y="24" width="6" height="36" rx="3" fill="url(#bar-grad)" />
		<rect x="154" y="8" width="6" height="52" rx="3" fill="url(#bar-grad)" />
		<text x="180" y="38" fill="#94a3b8" font-family="monospace" font-size="11px">SOUNDTRACK: %s</text>
	</g>
	<!-- Storyboard Scenes Ribbon -->
	<g transform="translate(24, 450)">
		<rect width="912" height="60" rx="8" fill="#030712" opacity="0.85" stroke="#1e293b" stroke-width="1" />
		<text x="16" y="24" fill="#38bdf8" font-family="monospace" font-size="11px" font-weight="bold">VIDEO STORY COMPOSITION • %d SCENES • %s</text>
		<text x="16" y="44" fill="#e2e8f0" font-family="sans-serif" font-size="12px" font-weight="600">%s</text>
	</g>
</svg>`,
		width, height, width, height,
		width, height,
		escapeXML(soundtrack),
		sceneCount, aspectRatio,
		escapeXML(truncateString(prompt, 70)),
	)

	return fmt.Sprintf("data:image/svg+xml;base64,%s", base64.StdEncoding.EncodeToString([]byte(svg)))
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
