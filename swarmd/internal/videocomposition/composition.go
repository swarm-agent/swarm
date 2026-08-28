// Package videocomposition owns the durable, renderer-independent spatial
// composition contract shared by storyboard authoring and Video Studio.
package videocomposition

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion = 1
	MaxLayouts    = 32
	MaxSlots      = 16

	FitContain = "contain"
	FitCover   = "cover"

	MaskNone        = "none"
	MaskRoundedRect = "rounded_rect"
	MaskEllipse     = "ellipse"

	AudioMute    = "mute"
	AudioInclude = "include"
)

var stableIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Catalog is timeline-owned reusable geometry. A layout may extend exactly one
// other layout; a same-ID child slot deterministically replaces its parent slot.
type Catalog struct {
	SchemaVersion int      `json:"schema_version"`
	Layouts       []Layout `json:"layouts"`
}

type Layout struct {
	ID              string `json:"id"`
	ExtendsLayoutID string `json:"extends_layout_id,omitempty"`
	Slots           []Slot `json:"slots"`
}

type NormalizedRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Crop struct {
	Top    float64 `json:"top,omitempty"`
	Right  float64 `json:"right,omitempty"`
	Bottom float64 `json:"bottom,omitempty"`
	Left   float64 `json:"left,omitempty"`
}

type Mask struct {
	Kind   string  `json:"kind"`
	Radius float64 `json:"radius,omitempty"`
}

// SourceBinding identifies one registered source and gives source time and
// part-relative timeline time independently. Unequal ranges intentionally
// represent deterministic retiming.
type SourceBinding struct {
	SourceRef       string  `json:"source_ref"`
	MediaType       string  `json:"media_type"`
	SourceStartMs   int64   `json:"source_start_ms"`
	SourceEndMs     int64   `json:"source_end_ms"`
	TimelineStartMs int64   `json:"timeline_start_ms"`
	TimelineEndMs   int64   `json:"timeline_end_ms"`
	AudioPolicy     string  `json:"audio_policy"`
	Gain            float64 `json:"gain,omitempty"`
}

type Slot struct {
	ID          string         `json:"id"`
	Requirement string         `json:"requirement"`
	Geometry    NormalizedRect `json:"geometry"`
	ZIndex      int            `json:"z_index"`
	Fit         string         `json:"fit"`
	AlignmentX  float64        `json:"alignment_x"`
	AlignmentY  float64        `json:"alignment_y"`
	Crop        Crop           `json:"crop,omitempty"`
	Mask        Mask           `json:"mask"`
	AspectLock  float64        `json:"aspect_lock,omitempty"`
	Source      *SourceBinding `json:"source,omitempty"`
}

// Link attaches one part to reusable layout geometry. Overrides are sparse and
// shot-owned. DetachedSlots are a complete private layout; Disabled explicitly
// removes composition without making omitted fields destructive during merges.
type Link struct {
	LayoutID      string         `json:"layout_id,omitempty"`
	Overrides     []SlotOverride `json:"overrides,omitempty"`
	Detached      bool           `json:"detached,omitempty"`
	DetachedSlots []Slot         `json:"detached_slots,omitempty"`
	Disabled      bool           `json:"disabled,omitempty"`
}

type SlotOverride struct {
	SlotID      string          `json:"slot_id"`
	Requirement *string         `json:"requirement,omitempty"`
	Geometry    *NormalizedRect `json:"geometry,omitempty"`
	ZIndex      *int            `json:"z_index,omitempty"`
	Fit         *string         `json:"fit,omitempty"`
	AlignmentX  *float64        `json:"alignment_x,omitempty"`
	AlignmentY  *float64        `json:"alignment_y,omitempty"`
	Crop        *Crop           `json:"crop,omitempty"`
	Mask        *Mask           `json:"mask,omitempty"`
	AspectLock  *float64        `json:"aspect_lock,omitempty"`
	Source      *SourceBinding  `json:"source,omitempty"`
	ClearSource bool            `json:"clear_source,omitempty"`
}

type PixelRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type ResolvedSlot struct {
	Slot
	Pixels PixelRect `json:"pixels"`
}

func ValidateCatalog(catalog *Catalog) error {
	if catalog == nil {
		return nil
	}
	if catalog.SchemaVersion != SchemaVersion {
		return fmt.Errorf("composition schema_version must be %d", SchemaVersion)
	}
	if len(catalog.Layouts) < 1 || len(catalog.Layouts) > MaxLayouts {
		return fmt.Errorf("composition layouts must contain 1 to %d entries", MaxLayouts)
	}
	layouts := make(map[string]Layout, len(catalog.Layouts))
	for _, layout := range catalog.Layouts {
		if !stableIDPattern.MatchString(layout.ID) {
			return errors.New("composition layout has invalid stable id")
		}
		if _, exists := layouts[layout.ID]; exists {
			return fmt.Errorf("duplicate composition layout id %q", layout.ID)
		}
		if layout.ExtendsLayoutID != "" && !stableIDPattern.MatchString(layout.ExtendsLayoutID) {
			return fmt.Errorf("composition layout %q has invalid parent id", layout.ID)
		}
		if len(layout.Slots) > MaxSlots || (len(layout.Slots) == 0 && layout.ExtendsLayoutID == "") {
			return fmt.Errorf("composition layout %q has invalid slot count", layout.ID)
		}
		seen := map[string]struct{}{}
		for _, slot := range layout.Slots {
			if _, duplicate := seen[slot.ID]; duplicate {
				return fmt.Errorf("composition layout %q has duplicate slot %q", layout.ID, slot.ID)
			}
			seen[slot.ID] = struct{}{}
			if err := validateSlot(slot); err != nil {
				return fmt.Errorf("composition layout %q: %w", layout.ID, err)
			}
		}
		layouts[layout.ID] = layout
	}
	for id := range layouts {
		visiting := map[string]bool{}
		seen := map[string]bool{}
		var visit func(string) error
		visit = func(current string) error {
			if visiting[current] {
				return fmt.Errorf("composition layout inheritance cycle includes %q", current)
			}
			if seen[current] {
				return nil
			}
			layout, ok := layouts[current]
			if !ok {
				return fmt.Errorf("composition layout %q references missing parent", current)
			}
			visiting[current] = true
			if layout.ExtendsLayoutID != "" {
				if _, ok := layouts[layout.ExtendsLayoutID]; !ok {
					return fmt.Errorf("composition layout %q references missing parent %q", current, layout.ExtendsLayoutID)
				}
				if err := visit(layout.ExtendsLayoutID); err != nil {
					return err
				}
			}
			visiting[current] = false
			seen[current] = true
			return nil
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func ValidateLink(catalog *Catalog, link *Link, partDurationMs int64) error {
	if link == nil {
		return nil
	}
	if link.Disabled {
		if link.LayoutID != "" || link.Detached || len(link.Overrides) != 0 || len(link.DetachedSlots) != 0 {
			return errors.New("disabled composition link cannot carry layout data")
		}
		return nil
	}
	if partDurationMs <= 0 {
		return errors.New("composition requires a positive part duration")
	}
	if link.Detached {
		if link.LayoutID != "" || len(link.DetachedSlots) == 0 || len(link.DetachedSlots) > MaxSlots {
			return errors.New("detached composition requires only a bounded private slot list")
		}
	} else if !stableIDPattern.MatchString(link.LayoutID) || len(link.DetachedSlots) != 0 {
		return errors.New("linked composition requires one stable layout_id and no detached slots")
	}
	seen := map[string]struct{}{}
	for _, slot := range link.DetachedSlots {
		if _, duplicate := seen[slot.ID]; duplicate {
			return fmt.Errorf("duplicate detached composition slot %q", slot.ID)
		}
		seen[slot.ID] = struct{}{}
		if err := validateSlotForDuration(slot, partDurationMs); err != nil {
			return err
		}
	}
	seenOverrides := map[string]struct{}{}
	for _, override := range link.Overrides {
		if !stableIDPattern.MatchString(override.SlotID) {
			return errors.New("composition override has invalid slot_id")
		}
		if _, duplicate := seenOverrides[override.SlotID]; duplicate {
			return fmt.Errorf("duplicate composition override for slot %q", override.SlotID)
		}
		seenOverrides[override.SlotID] = struct{}{}
		if override.Source != nil && override.ClearSource {
			return fmt.Errorf("composition override %q cannot set and clear source", override.SlotID)
		}
	}
	_, err := Resolve(catalog, link, 1920, 1080, partDurationMs)
	return err
}

func Resolve(catalog *Catalog, link *Link, width, height int, partDurationMs int64) ([]ResolvedSlot, error) {
	if link == nil || link.Disabled {
		return nil, nil
	}
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, errors.New("composition output dimensions must be positive even pixels")
	}
	if err := ValidateCatalog(catalog); err != nil {
		return nil, err
	}
	var slots []Slot
	if link.Detached {
		slots = append(slots, link.DetachedSlots...)
	} else {
		if catalog == nil {
			return nil, errors.New("composition link requires a catalog")
		}
		layouts := make(map[string]Layout, len(catalog.Layouts))
		for _, layout := range catalog.Layouts {
			layouts[layout.ID] = layout
		}
		var err error
		slots, err = resolveLayout(layouts, link.LayoutID, nil)
		if err != nil {
			return nil, err
		}
	}
	indices := make(map[string]int, len(slots))
	for i := range slots {
		indices[slots[i].ID] = i
	}
	for _, override := range link.Overrides {
		index, ok := indices[override.SlotID]
		if !ok {
			return nil, fmt.Errorf("composition override references missing slot %q", override.SlotID)
		}
		applyOverride(&slots[index], override)
	}
	if len(slots) == 0 || len(slots) > MaxSlots {
		return nil, errors.New("resolved composition slot count is invalid")
	}
	resolved := make([]ResolvedSlot, len(slots))
	for i, slot := range slots {
		if err := validateSlotForDuration(slot, partDurationMs); err != nil {
			return nil, err
		}
		pixels, err := resolvePixels(slot, width, height)
		if err != nil {
			return nil, fmt.Errorf("composition slot %q: %w", slot.ID, err)
		}
		resolved[i] = ResolvedSlot{Slot: slot, Pixels: pixels}
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].ZIndex == resolved[j].ZIndex {
			return resolved[i].ID < resolved[j].ID
		}
		return resolved[i].ZIndex < resolved[j].ZIndex
	})
	return resolved, nil
}

func resolveLayout(layouts map[string]Layout, id string, stack []string) ([]Slot, error) {
	for _, current := range stack {
		if current == id {
			return nil, fmt.Errorf("composition layout inheritance cycle includes %q", id)
		}
	}
	layout, ok := layouts[id]
	if !ok {
		return nil, fmt.Errorf("composition layout %q is missing", id)
	}
	var slots []Slot
	if layout.ExtendsLayoutID != "" {
		var err error
		slots, err = resolveLayout(layouts, layout.ExtendsLayoutID, append(stack, id))
		if err != nil {
			return nil, err
		}
	}
	indices := map[string]int{}
	for i := range slots {
		indices[slots[i].ID] = i
	}
	for _, slot := range layout.Slots {
		if i, exists := indices[slot.ID]; exists {
			slots[i] = slot
		} else {
			indices[slot.ID] = len(slots)
			slots = append(slots, slot)
		}
	}
	return slots, nil
}

func validateSlotForDuration(slot Slot, duration int64) error {
	if err := validateSlot(slot); err != nil {
		return err
	}
	if slot.Source != nil {
		s := slot.Source
		if s.TimelineStartMs < 0 || s.TimelineEndMs > duration {
			return fmt.Errorf("composition slot %q source timeline range exceeds part duration", slot.ID)
		}
	}
	return nil
}

func validateSlot(slot Slot) error {
	if !stableIDPattern.MatchString(slot.ID) {
		return errors.New("composition slot has invalid stable id")
	}
	if strings.TrimSpace(slot.Requirement) == "" || strings.TrimSpace(slot.Requirement) != slot.Requirement || len(slot.Requirement) > 1000 {
		return fmt.Errorf("composition slot %q has invalid requirement", slot.ID)
	}
	if err := validateRect(slot.Geometry); err != nil {
		return fmt.Errorf("composition slot %q: %w", slot.ID, err)
	}
	if slot.ZIndex < 0 || slot.ZIndex > 255 {
		return fmt.Errorf("composition slot %q z_index is outside 0..255", slot.ID)
	}
	if slot.Fit != FitContain && slot.Fit != FitCover {
		return fmt.Errorf("composition slot %q fit must be contain or cover", slot.ID)
	}
	if !unit(slot.AlignmentX) || !unit(slot.AlignmentY) {
		return fmt.Errorf("composition slot %q alignment is outside 0..1", slot.ID)
	}
	if !unit(slot.Crop.Top) || !unit(slot.Crop.Right) || !unit(slot.Crop.Bottom) || !unit(slot.Crop.Left) || slot.Crop.Left+slot.Crop.Right >= 1 || slot.Crop.Top+slot.Crop.Bottom >= 1 {
		return fmt.Errorf("composition slot %q crop is invalid", slot.ID)
	}
	if slot.Mask.Kind != MaskNone && slot.Mask.Kind != MaskRoundedRect && slot.Mask.Kind != MaskEllipse {
		return fmt.Errorf("composition slot %q mask is unsupported", slot.ID)
	}
	if slot.Mask.Kind == MaskRoundedRect && (slot.Mask.Radius < 0 || slot.Mask.Radius > .5) {
		return fmt.Errorf("composition slot %q mask radius is outside 0..0.5", slot.ID)
	}
	if slot.Mask.Kind != MaskRoundedRect && slot.Mask.Radius != 0 {
		return fmt.Errorf("composition slot %q mask radius requires rounded_rect", slot.ID)
	}
	if !finite(slot.AspectLock) || slot.AspectLock < 0 || slot.AspectLock > 10 {
		return fmt.Errorf("composition slot %q aspect_lock is invalid", slot.ID)
	}
	if slot.Source != nil {
		s := slot.Source
		if strings.TrimSpace(s.SourceRef) == "" || strings.TrimSpace(s.SourceRef) != s.SourceRef || len(s.SourceRef) > 256 || !strings.HasPrefix(s.MediaType, "video/") {
			return fmt.Errorf("composition slot %q requires an exact registered video source", slot.ID)
		}
		if s.SourceStartMs < 0 || s.SourceEndMs <= s.SourceStartMs || s.TimelineStartMs < 0 || s.TimelineEndMs <= s.TimelineStartMs {
			return fmt.Errorf("composition slot %q has invalid independent timing ranges", slot.ID)
		}
		if s.AudioPolicy != AudioMute && s.AudioPolicy != AudioInclude {
			return fmt.Errorf("composition slot %q requires explicit audio_policy", slot.ID)
		}
		if !finite(s.Gain) || s.Gain < 0 || s.Gain > 2 || (s.AudioPolicy == AudioMute && s.Gain != 0) {
			return fmt.Errorf("composition slot %q has invalid audio gain", slot.ID)
		}
	}
	return nil
}

func validateRect(rect NormalizedRect) error {
	if !finite(rect.X) || !finite(rect.Y) || !finite(rect.Width) || !finite(rect.Height) || rect.X < 0 || rect.Y < 0 || rect.Width <= 0 || rect.Height <= 0 || rect.X+rect.Width > 1 || rect.Y+rect.Height > 1 {
		return errors.New("normalized geometry must be finite and contained in 0..1")
	}
	return nil
}

func resolvePixels(slot Slot, width, height int) (PixelRect, error) {
	x0 := evenFloor(slot.Geometry.X * float64(width))
	y0 := evenFloor(slot.Geometry.Y * float64(height))
	x1 := evenCeil((slot.Geometry.X + slot.Geometry.Width) * float64(width))
	y1 := evenCeil((slot.Geometry.Y + slot.Geometry.Height) * float64(height))
	if x1 > width {
		x1 = width
	}
	if y1 > height {
		y1 = height
	}
	w, h := x1-x0, y1-y0
	if slot.AspectLock > 0 {
		target := slot.AspectLock
		if float64(w)/float64(h) > target {
			w = evenFloor(float64(h) * target)
		} else {
			h = evenFloor(float64(w) / target)
		}
		x0 += evenFloor(float64((x1-x0)-w) * slot.AlignmentX)
		y0 += evenFloor(float64((y1-y0)-h) * slot.AlignmentY)
	}
	if w < 2 || h < 2 || x0 < 0 || y0 < 0 || x0+w > width || y0+h > height {
		return PixelRect{}, errors.New("geometry resolves outside safe even pixel bounds")
	}
	return PixelRect{X: x0, Y: y0, Width: w, Height: h}, nil
}

func applyOverride(slot *Slot, override SlotOverride) {
	if override.Requirement != nil {
		slot.Requirement = *override.Requirement
	}
	if override.Geometry != nil {
		slot.Geometry = *override.Geometry
	}
	if override.ZIndex != nil {
		slot.ZIndex = *override.ZIndex
	}
	if override.Fit != nil {
		slot.Fit = *override.Fit
	}
	if override.AlignmentX != nil {
		slot.AlignmentX = *override.AlignmentX
	}
	if override.AlignmentY != nil {
		slot.AlignmentY = *override.AlignmentY
	}
	if override.Crop != nil {
		slot.Crop = *override.Crop
	}
	if override.Mask != nil {
		slot.Mask = *override.Mask
	}
	if override.AspectLock != nil {
		slot.AspectLock = *override.AspectLock
	}
	if override.ClearSource {
		slot.Source = nil
	} else if override.Source != nil {
		slot.Source = override.Source
	}
}

func finite(v float64) bool   { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func unit(v float64) bool     { return finite(v) && v >= 0 && v <= 1 }
func evenFloor(v float64) int { return int(math.Floor(v/2)) * 2 }
func evenCeil(v float64) int  { return int(math.Ceil(v/2)) * 2 }
