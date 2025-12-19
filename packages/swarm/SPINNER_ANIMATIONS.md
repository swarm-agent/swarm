# 🎨 Spinner & Animation Components

This document showcases all available spinner and animation components in OpenTUI.

## 📍 Location

`packages/opencode/src/cli/cmd/tui/ui/tool-animations.tsx`

## 🎭 Animation System

OpenTUI supports two animation methods:

### Method 1: Frame-based (setInterval)

- Best for: Character rotation, discrete states
- Uses: `setInterval()` + `onCleanup()`
- Example: Spinner cycling through braille characters

### Method 2: Timeline-based (smooth interpolation)

- Best for: Opacity, color, position tweening
- Uses: `useTimeline()` from `@opentui/solid`
- Easing functions: `linear`, `inOutQuad`, `outBounce`, `outElastic`, `inBack`, `outBack`, `inOutSine`, etc.
- Example: Pulsing opacity effects

## 🌟 Available Components

### 1. **Spinner** (Original)

```tsx
<Spinner color={RGBA} speed={80} />
```

- **Characters**: `["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]`
- **Speed**: 80ms per frame (default)
- **Use case**: General loading indicator

### 2. **PulsingDot** (Original)

```tsx
<PulsingDot color={RGBA} />
```

- **Character**: `●`
- **Effect**: Opacity pulses from 0.5 to 1.0
- **Duration**: 1400ms loop
- **Easing**: `inOutQuad`
- **Use case**: Active tool indicator

### 3. **BouncyDot** ✨ NEW

```tsx
<BouncyDot color={RGBA} />
```

- **Character**: `●`
- **Effect**: Opacity pulses with bounce easing
- **Duration**: 1200ms loop
- **Easing**: `outBounce` (gives a bouncy spring effect)
- **Use case**: Playful loading states

### 4. **SpinningDot** ✨ NEW

```tsx
<SpinningDot color={RGBA} speed={150} />
```

- **Characters**: `["●", "◐", "◓", "◑", "◒"]` (circle rotation)
- **Effect**: Rotates through circle phases + opacity pulse
- **Speed**: 150ms per frame
- **Use case**: Active processing with visual rotation

### 5. **ClaudeSpinner** ✨ NEW

```tsx
<ClaudeSpinner color={RGBA} speed={80} />
```

- **Characters**: Braille spinner `["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]`
- **Effect**: Combines frame rotation + color intensity pulse
- **Duration**: 1600ms intensity pulse
- **Easing**: `inOutSine` (smooth wave)
- **Use case**: Claude's active thinking state 🤖

### 6. **WavyDots** ✨ NEW

```tsx
<WavyDots color={RGBA} />
```

- **Characters**: Three dots `●●●`
- **Effect**: Sine wave animation (each dot waves with phase offset)
- **Use case**: Streaming/loading with fluid motion

### 7. **RotatingArrow** ✨ NEW

```tsx
<RotatingArrow color={RGBA} speed={120} />
```

- **Characters**: `["↑", "↗", "→", "↘", "↓", "↙", "←", "↖"]`
- **Speed**: 120ms per frame
- **Use case**: Directional processing indicators

### 8. **StreamingDots** (Original)

```tsx
<StreamingDots />
```

- **Effect**: Animated ellipsis (`.`, `..`, `...`)
- **Use case**: Text streaming indicator

### 9. **PulsingText** (Original)

```tsx
<PulsingText color={RGBA}>Your text</PulsingText>
```

- **Effect**: Text opacity pulses
- **Use case**: Highlighting active text

### 10. **SuccessCheckmark** (Original)

```tsx
<SuccessCheckmark delay={0} />
```

- **Character**: `✓`
- **Effect**: Scales in with `outBack` easing (pop-in)
- **Use case**: Task completion

### 11. **ErrorX** (Original)

```tsx
<ErrorX delay={0} />
```

- **Character**: `✗`
- **Effect**: Shakes in with `outElastic` easing
- **Use case**: Error states

## 🎨 Easing Functions Available

From `@opentui/core/animation/Timeline`:

- `linear` - No easing
- `inQuad`, `outQuad`, `inOutQuad` - Quadratic
- `inExpo`, `outExpo` - Exponential
- `inOutSine` - Sine wave (smooth)
- `outBounce`, `inBounce` - Bouncy spring
- `outElastic` - Elastic snap
- `inCirc`, `outCirc`, `inOutCirc` - Circular
- `inBack`, `outBack`, `inOutBack` - Overshooting (s parameter)

## 💡 Usage Examples

### Replace the `●` in TextPart with ClaudeSpinner:

**Before:**

```tsx
<text fg={theme.accent}>●</text>
```

**After:**

```tsx
<text>
  <ClaudeSpinner color={theme.accent} />
</text>
```

⚠️ **Important**: Spinners return `<span>` elements which must be wrapped in `<text>` in OpenTUI!

**After:**

```tsx
<ClaudeSpinner color={theme.accent} />
```

### Location to modify:

`packages/opencode/src/cli/cmd/tui/routes/session/index.tsx:1314`

### Add to imports:

```tsx
import {
  Spinner,
  PulsingDot,
  ClaudeSpinner, // Add this
  BouncyDot, // Add this
  SpinningDot, // Add this
  // ... other imports
} from "@tui/ui/tool-animations"
```

## 🎯 Recommended Usage

| State           | Recommended Component         | Why                            |
| --------------- | ----------------------------- | ------------------------------ |
| Claude thinking | `ClaudeSpinner`               | Smooth, professional, hypnotic |
| Tool running    | `Spinner` or `PulsingDot`     | Clear, standard                |
| Streaming       | `WavyDots` or `StreamingDots` | Fluid motion                   |
| Processing      | `SpinningDot`                 | Visual rotation feedback       |
| Waiting         | `BouncyDot`                   | Playful, attention-grabbing    |
| Success         | `SuccessCheckmark`            | Clear completion               |
| Error           | `ErrorX`                      | Clear failure                  |

## 🚀 Creating Custom Animations

### Timeline-based (smooth):

```tsx
export function MyAnimation(props: { color?: RGBA }) {
  const { theme } = useTheme()
  const [value, setValue] = createSignal(0)

  const timeline = useTimeline({
    duration: 1000,
    loop: true,
  })

  const target = { value: 0, setValue }

  timeline.add(target, {
    value: 1.0,
    duration: 500,
    ease: "outBounce",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setValue(target.value),
  })

  // Use value() in your render
  return <span>...</span>
}
```

### Frame-based (discrete):

```tsx
export function MySpinner(props: { speed?: number }) {
  const frames = ["◐", "◓", "◑", "◒"]
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % frames.length)
  }, props.speed ?? 100)

  onCleanup(() => clearInterval(interval))

  return <span>{frames[frame()]}</span>
}
```

## 🎪 Demo Ideas

Want to see them in action? Modify the TextPart component to cycle through different animations!

```tsx
// In TextPart component
const [animIndex, setAnimIndex] = createSignal(0)
const animations = [<ClaudeSpinner />, <SpinningDot />, <BouncyDot />, <WavyDots />]

// Cycle every 3 seconds
setInterval(() => {
  setAnimIndex((prev) => (prev + 1) % animations.length)
}, 3000)
```

---

**Created by**: Claude Code Assistant 🤖
**Date**: November 15, 2025
**Purpose**: Replace the static `●` with dynamic, beautiful animations!
