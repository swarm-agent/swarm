import { RGBA, TextAttributes, TextareaRenderable } from "@opentui/core"
import { useTimeline, useKeyboard, useRenderer } from "@opentui/solid"
import { createSignal, createEffect, onCleanup, For, Show, Switch, Match, createMemo } from "solid-js"
import { useTheme } from "@tui/context/theme"
import type { JSX } from "@opentui/solid"
import { type SpinnerDef, getSpinner } from "./spinner-definitions"
import type { Permission } from "@/permission"
import { useDialog } from "./dialog"
import { Shimmer } from "./shimmer"

/**
 * Animated spinner for tool execution - expanding ring animation
 */
export function Spinner(props: { color?: RGBA; speed?: number; static?: boolean }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [frame, setFrame] = createSignal(0)

  // Static state - just show the center dot
  if (props.static) {
    return <span style={{ fg: baseColor }}>●</span>
  }

  // Expanding ring animation
  const frames = ["●", "◉", "◎", "○"]

  const timeline = useTimeline({
    duration: 1600,
    loop: true,
  })

  const target = {
    frame: 0,
    setFrame,
  }

  timeline!.add(target, {
    frame: frames.length - 1,
    duration: 800,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setFrame(Math.round(target.frame))
    },
  })

  return <span style={{ fg: baseColor }}>{frames[frame()]}</span>
}

/**
 * Pulsing dot indicator for active tools - cool swirling animation with expanding rings
 */
export function PulsingDot(props: { color?: RGBA; style?: "swirl" | "orbit" | "spark" | "wave" | "bloom" }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [opacity, setOpacity] = createSignal(0.5)
  const [frame, setFrame] = createSignal(0)

  // Different animation styles
  const framesByStyle = {
    swirl: ["─", "\\", "│", "/"], // Simple rotating lines
    orbit: ["◜", "◝", "◞", "◟"], // Orbiting quarter circles
    spark: ["✦", "✧", "✦", "✧"], // Sparkling effect
    wave: ["◠", "◡", "◠", "◡"], // Wave motion
    bloom: ["·", "∘", "○", "◎", "◉", "●", "◉", "◎", "○", "∘"], // Blooming flower effect
  }

  // Pick random style if not specified
  const styles = Object.keys(framesByStyle) as Array<keyof typeof framesByStyle>
  const randomStyle = styles[Math.floor(Math.random() * styles.length)]
  const style = props.style ?? randomStyle

  const frames = framesByStyle[style]

  // Pulsing opacity animation
  const timeline = useTimeline({
    duration: 1400,
    loop: true,
  })

  const target = {
    opacity: 0.5,
    setOpacity,
  }

  timeline!.add(target, {
    opacity: 1.0,
    duration: 700,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setOpacity(target.opacity)
    },
  })

  // Frame rotation speed varies by style
  const speed = style === "bloom" ? 120 : style === "spark" ? 300 : 150

  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % frames.length)
  }, speed)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{frames[frame()]}</span>
}

/**
 * Bouncy pulsing dot with elastic easing
 */
export function BouncyDot(props: { color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [scale, setScale] = createSignal(0.6)

  const timeline = useTimeline({
    duration: 1200,
    loop: true,
  })

  const target = {
    scale: 0.6,
    setScale,
  }

  timeline!.add(target, {
    scale: 1.0,
    duration: 600,
    ease: "outBounce",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setScale(target.scale)
    },
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, scale() * 255)

  return <span style={{ fg: color() }}>●</span>
}

/**
 * Spinning dot that cycles through different circle characters
 */
export function SpinningDot(props: { color?: RGBA; speed?: number }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const frames = ["●", "◐", "◓", "◑", "◒"]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.7)

  // Frame rotation
  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % frames.length)
  }, props.speed ?? 150)

  // Smooth opacity pulse
  const timeline = useTimeline({
    duration: 1000,
    loop: true,
  })

  const target = {
    opacity: 0.7,
    setOpacity,
  }

  timeline!.add(target, {
    opacity: 1.0,
    duration: 500,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setOpacity(target.opacity)
    },
  })

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{frames[frame()]}</span>
}

/**
 * Claude spinner - expanding ring animation with pulsing
 */
export function ClaudeSpinner(props: { color?: RGBA; speed?: number; static?: boolean }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [frame, setFrame] = createSignal(0)

  // Static state - just show the center dot
  if (props.static) {
    return <span style={{ fg: baseColor }}>●</span>
  }

  // Expanding ring animation
  const frames = ["●", "◉", "◎", "○"]

  const timeline = useTimeline({
    duration: 1600,
    loop: true,
  })

  const target = {
    frame: 0,
    setFrame,
  }

  timeline!.add(target, {
    frame: frames.length - 1,
    duration: 800,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setFrame(Math.round(target.frame))
    },
  })

  return <span style={{ fg: baseColor }}>{frames[frame()]}</span>
}

/**
 * Three dots that wave up and down
 */
export function WavyDots(props: { color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [time, setTime] = createSignal(0)

  const interval = setInterval(() => {
    setTime((prev) => prev + 0.1)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const dots = ["●", "●", "●"]
  const getOpacity = (index: number) => {
    const wave = Math.sin(time() + index * 0.8) * 0.3 + 0.7
    return wave
  }

  // Pre-calculate colors for each dot
  const dotColors = () =>
    dots.map((_, i) => {
      const opacity = getOpacity(i)
      return RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity * 255)
    })

  return (
    <text>
      <span style={{ fg: dotColors()[0] }}>●</span>
      <span style={{ fg: dotColors()[1] }}>●</span>
      <span style={{ fg: dotColors()[2] }}>●</span>
    </text>
  )
}

/**
 * Rotating arrow spinner
 */
export function RotatingArrow(props: { color?: RGBA; speed?: number }) {
  const { theme } = useTheme()
  const color = props.color ?? theme.accent
  const frames = ["↑", "↗", "→", "↘", "↓", "↙", "←", "↖"]
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % frames.length)
  }, props.speed ?? 120)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: color }}>{frames[frame()]}</span>
}

/**
 * Generic spinner that can render any SpinnerDef from spinner-definitions.ts
 * Supports all spinner modes: normal, cyber, neon, rgb, glitch
 */
export function GenericSpinner(props: { spinner: string | SpinnerDef; color?: RGBA }) {
  const { theme } = useTheme()

  // Get spinner definition
  const spinnerDef = typeof props.spinner === "string" ? getSpinner(props.spinner) : props.spinner

  const [frame, setFrame] = createSignal(0)

  // Color mapping for different modes
  const getModeColor = (mode: SpinnerDef["mode"]): RGBA => {
    if (props.color) return props.color

    switch (mode) {
      case "cyber":
        return RGBA.fromInts(0, 255, 255, 255) // Cyan
      case "neon":
        return RGBA.fromInts(255, 0, 255, 255) // Magenta
      case "rgb":
        return theme.accent // Will be animated in RainbowSpinner
      case "glitch":
        return RGBA.fromInts(255, 0, 0, 255) // Red
      case "normal":
      default:
        return theme.accent
    }
  }

  const baseColor = getModeColor(spinnerDef.mode)

  // Frame rotation
  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % spinnerDef.frames.length)
  }, spinnerDef.interval)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: baseColor }}>{spinnerDef.frames[frame()]}</span>
}

/**
 * Rainbow spinner - cycles through RGB colors for spinners with mode='rgb'
 */
export function RainbowSpinner(props: { spinner: string | SpinnerDef }) {
  const spinnerDef = typeof props.spinner === "string" ? getSpinner(props.spinner) : props.spinner

  const [frame, setFrame] = createSignal(0)
  const [hue, setHue] = createSignal(0)

  // Frame rotation
  const frameInterval = setInterval(() => {
    setFrame((prev) => (prev + 1) % spinnerDef.frames.length)
  }, spinnerDef.interval)

  // Hue rotation for rainbow effect (360 degrees in 3 seconds)
  const hueInterval = setInterval(() => {
    setHue((prev) => (prev + 2) % 360)
  }, 30)

  onCleanup(() => {
    clearInterval(frameInterval)
    clearInterval(hueInterval)
  })

  // Convert HSL to RGB
  const hslToRgb = (h: number, s: number, l: number): RGBA => {
    s /= 100
    l /= 100
    const k = (n: number) => (n + h / 30) % 12
    const a = s * Math.min(l, 1 - l)
    const f = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)))

    return RGBA.fromInts(Math.round(255 * f(0)), Math.round(255 * f(8)), Math.round(255 * f(4)), 255)
  }

  const color = () => hslToRgb(hue(), 100, 50)

  return <span style={{ fg: color() }}>{spinnerDef.frames[frame()]}</span>
}

/**
 * Pulsing text indicator for active tools
 */
export function PulsingText(props: { children: JSX.Element | string; color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.text
  const [opacity, setOpacity] = createSignal(0.5)

  const timeline = useTimeline({
    duration: 1400,
    loop: true,
  })

  const target = {
    opacity: 0.5,
    setOpacity,
  }

  timeline!.add(target, {
    opacity: 1.0,
    duration: 700,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setOpacity(target.opacity)
    },
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{props.children}</span>
}

/**
 * Cool combo effect - pulsing dot with text in a fancy wrapper
 */
export function PulsingCombo(props: {
  children: JSX.Element | string
  color?: RGBA
  style?: "brackets" | "arrows" | "dots" | "simple"
}) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const style = props.style ?? "simple"

  const wrappers = {
    brackets: { left: "[ ", right: " ]" },
    arrows: { left: "‹ ", right: " ›" },
    dots: { left: "• ", right: " •" },
    simple: { left: "", right: "" },
  }

  const wrapper = wrappers[style]

  return (
    <text>
      <PulsingText color={baseColor}>{wrapper.left}</PulsingText>
      <PulsingDot color={baseColor} style="bloom" /> <PulsingText color={baseColor}>{props.children}</PulsingText>
      <PulsingText color={baseColor}>{wrapper.right}</PulsingText>
    </text>
  )
}

/**
 * Breathing effect - text and dot pulse together with synchronized breathing
 */
export function BreathingCombo(props: { children: JSX.Element | string; color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [scale, setScale] = createSignal(0.6)

  const timeline = useTimeline({
    duration: 2000,
    loop: true,
  })

  const target = {
    scale: 0.6,
    setScale,
  }

  timeline!.add(target, {
    scale: 1.0,
    duration: 1000,
    ease: "inOutCirc",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setScale(target.scale)
    },
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, scale() * 255)

  return (
    <text>
      <PulsingDot color={color()} style="bloom" /> <span style={{ fg: color() }}>{props.children}</span>
    </text>
  )
}

/**
 * Trailing effect - multiple dots that trail behind with decreasing opacity
 */
export function TrailingCombo(props: { children: JSX.Element | string; color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    setFrame((prev) => (prev + 1) % 3)
  }, 200)

  onCleanup(() => clearInterval(interval))

  const dotOpacities = () => {
    const f = frame()
    return [
      f === 0 ? 1.0 : f === 1 ? 0.6 : 0.3,
      f === 1 ? 1.0 : f === 2 ? 0.6 : 0.3,
      f === 2 ? 1.0 : f === 0 ? 0.6 : 0.3,
    ]
  }

  return (
    <text>
      <For each={dotOpacities()}>
        {(opacity) => {
          const color = RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity * 255)
          return <span style={{ fg: color }}>●</span>
        }}
      </For>{" "}
      <PulsingText color={baseColor}>{props.children}</PulsingText>
    </text>
  )
}

/**
 * Pulsing pencil indicator for edit operations
 */
export function PulsingPencil(props: { color?: RGBA }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.accent
  const [opacity, setOpacity] = createSignal(0.5)

  const timeline = useTimeline({
    duration: 1400,
    loop: true,
  })

  const target = {
    opacity: 0.5,
    setOpacity,
  }

  timeline!.add(target, {
    opacity: 1.0,
    duration: 700,
    ease: "inOutQuad",
    alternate: true,
    loop: 2,
    onUpdate: () => {
      target.setOpacity(target.opacity)
    },
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>⟩</span>
}

/**
 * Progress bar for tool execution
 */
export function ProgressBar(props: {
  width?: number
  progress?: number // 0-1
  indeterminate?: boolean
  color?: RGBA
}) {
  const { theme } = useTheme()
  const width = props.width ?? 20
  const color = props.color ?? theme.accent
  const [position, setPosition] = createSignal(0)

  // Indeterminate animation
  createEffect(() => {
    if (props.indeterminate) {
      const interval = setInterval(() => {
        setPosition((prev) => (prev + 1) % (width + 5))
      }, 100)
      onCleanup(() => clearInterval(interval))
    }
  })

  const filled = () => {
    if (props.indeterminate) {
      const pos = position()
      return Array(width)
        .fill(0)
        .map((_, i) => {
          const distance = Math.abs(i - pos)
          return distance < 3 ? "█" : "░"
        })
        .join("")
    }
    const filled = Math.floor((props.progress ?? 0) * width)
    return "█".repeat(filled) + "░".repeat(width - filled)
  }

  return (
    <span style={{ fg: color }}>
      [{filled()}] {props.indeterminate ? "" : `${Math.floor((props.progress ?? 0) * 100)}%`}
    </span>
  )
}

/**
 * Streaming text effect with wave animation
 */
export function StreamingText(props: { text: string; color?: RGBA; speed?: number }) {
  const { theme } = useTheme()
  const baseColor = props.color ?? theme.text
  const characters = props.text.split("")
  const [time, setTime] = createSignal(0)

  const interval = setInterval(() => {
    setTime((prev) => prev + (props.speed ?? 100))
  }, props.speed ?? 100)

  onCleanup(() => clearInterval(interval))

  return (
    <text>
      <For each={characters}>
        {(ch, i) => {
          const phase = (time() + i() * 100) / 1000
          const opacity = 0.5 + Math.sin(phase) * 0.5
          const color = RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity * 255)
          return <span style={{ fg: color }}>{ch}</span>
        }}
      </For>
    </text>
  )
}

/**
 * Pulsing border effect for tool containers
 */
export function PulsingBorder(props: { children: JSX.Element; active: boolean }) {
  const { theme } = useTheme()
  const [intensity, setIntensity] = createSignal(0.3)

  createEffect(() => {
    if (props.active) {
      const timeline = useTimeline({
        duration: 1500,
        loop: true,
      })

      const target = {
        intensity: 0.3,
        setIntensity,
      }

      timeline!.add(target, {
        intensity: 1.0,
        duration: 750,
        ease: "inOutQuad",
        alternate: true,
        loop: 2,
        onUpdate: () => {
          target.setIntensity(target.intensity)
        },
      })
    }
  })

  const borderColor = () => {
    if (!props.active) return theme.border
    return RGBA.fromInts(theme.accent.r * 255, theme.accent.g * 255, theme.accent.b * 255, intensity() * 255)
  }

  return (
    <box border={["left"]} borderColor={borderColor()} paddingLeft={2}>
      {props.children}
    </box>
  )
}

/**
 * Success checkmark animation
 */
export function SuccessCheckmark(props: { delay?: number }) {
  const { theme } = useTheme()
  const [visible, setVisible] = createSignal(false)
  const [scale, setScale] = createSignal(0)

  setTimeout(() => {
    setVisible(true)
    const timeline = useTimeline({
      duration: 300,
    })

    const target = { scale: 0, setScale }
    timeline!.add(target, {
      scale: 1,
      duration: 300,
      ease: "outBack",
      onUpdate: () => target.setScale(target.scale),
    })
  }, props.delay ?? 0)

  const opacity = () => Math.min(scale(), 1)
  const color = RGBA.fromInts(theme.primary.r * 255, theme.primary.g * 255, theme.primary.b * 255, opacity() * 255)

  return visible() ? <span style={{ fg: color, bold: true }}>✓</span> : <></>
}

/**
 * Error X animation
 */
export function ErrorX(props: { delay?: number }) {
  const { theme } = useTheme()
  const [visible, setVisible] = createSignal(false)
  const [shake, setShake] = createSignal(0)

  setTimeout(() => {
    setVisible(true)
    const timeline = useTimeline({
      duration: 300,
    })

    const target = { shake: 0, setShake }
    timeline!.add(target, {
      shake: 1,
      duration: 300,
      ease: "outElastic",
      onUpdate: () => target.setShake(target.shake),
    })
  }, props.delay ?? 0)

  return visible() ? <span style={{ fg: theme.error, bold: true }}>✗</span> : <></>
}

/**
 * Tool status badge with animations
 */
export function ToolStatusBadge(props: { status: "pending" | "running" | "completed" | "error" }) {
  const { theme } = useTheme()

  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <text>
          <span style={{ fg: theme.textMuted }}>
            <Spinner /> pending
          </span>
        </text>
      </Match>
      <Match when={props.status === "running"}>
        <text>
          <span style={{ fg: theme.accent }}>
            <PulsingDot /> running
          </span>
        </text>
      </Match>
      <Match when={props.status === "completed"}>
        <text>
          <span style={{ fg: theme.primary }}>
            <SuccessCheckmark /> completed
          </span>
        </text>
      </Match>
      <Match when={props.status === "error"}>
        <text>
          <span style={{ fg: theme.error }}>
            <ErrorX /> error
          </span>
        </text>
      </Match>
    </Switch>
  )
}

/**
 * Streaming dots indicator
 */
export function StreamingDots() {
  const { theme } = useTheme()
  const [count, setCount] = createSignal(0)

  const interval = setInterval(() => {
    setCount((prev) => (prev + 1) % 4)
  }, 400)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: theme.accent }}>{".".repeat(count())}</span>
}

/**
 * Elapsed time display for running tools
 */
export function ElapsedTime(props: { startTime: number; color?: RGBA }) {
  const { theme } = useTheme()
  const color = props.color ?? theme.textMuted
  const [elapsed, setElapsed] = createSignal(0)

  // Initialize with current elapsed time
  setElapsed(Math.max(0, Date.now() - props.startTime))

  const interval = setInterval(() => {
    setElapsed(Math.max(0, Date.now() - props.startTime))
  }, 100)

  onCleanup(() => clearInterval(interval))

  const formatTime = (ms: number) => {
    const safeMs = Math.max(0, ms)
    if (safeMs < 1000) return `${Math.floor(safeMs)}ms`
    if (safeMs < 60000) return `${(safeMs / 1000).toFixed(1)}s`
    const minutes = Math.floor(safeMs / 60000)
    const seconds = Math.floor((safeMs % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  const timeStr = () => formatTime(elapsed())

  return <span style={{ fg: color }}>{timeStr()}</span>
}

/**
 * Tool metadata display showing execution details
 */
export function ToolMetadata(props: {
  executionTime?: number
  metadata?: Record<string, any>
  input?: Record<string, any>
}) {
  const { theme } = useTheme()

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  const metadataEntries = () => {
    const entries: string[] = []

    // Add execution time
    if (props.executionTime !== undefined) {
      entries.push(`${formatTime(props.executionTime)}`)
    }

    // Add tool-specific metadata
    if (props.metadata) {
      // Match count (for Grep/Glob)
      if (props.metadata.count !== undefined) {
        entries.push(`${props.metadata.count} matches`)
      }
      if (props.metadata.matches !== undefined) {
        entries.push(`${props.metadata.matches} matches`)
      }

      // Exit code (for Bash)
      if (props.metadata.exit !== undefined) {
        entries.push(`exit ${props.metadata.exit}`)
      }

      // Truncation indicator
      if (props.metadata.truncated) {
        entries.push(`truncated`)
      }

      // Tool summary count (for Task)
      if (props.metadata.summary && Array.isArray(props.metadata.summary)) {
        const completed = props.metadata.summary.filter((t: any) => t.status === "completed").length
        const total = props.metadata.summary.length
        entries.push(`${completed}/${total} tools`)
      }
    }

    return entries
  }

  const entries = metadataEntries()
  if (entries.length === 0) return <></>

  return (
    <text>
      <span style={{ fg: theme.textMuted }}>{entries.join(" · ")}</span>
    </text>
  )
}

/**
 * Tool card wrapper with pulsing border animation
 */
export function ToolCard(props: {
  children: JSX.Element
  status: "pending" | "running" | "completed" | "error"
  inline?: boolean
}) {
  const { theme } = useTheme()
  const [intensity, setIntensity] = createSignal(0.4)

  // Pulsing animation for pending and running states
  createEffect(() => {
    if (props.status === "pending" || props.status === "running") {
      const timeline = useTimeline({
        duration: 2000,
        loop: true,
      })

      const target = {
        intensity: 0.4,
        setIntensity,
      }

      timeline!.add(target, {
        intensity: 1.0,
        duration: 1000,
        ease: "inOutQuad",
        alternate: true,
        loop: 2,
        onUpdate: () => {
          target.setIntensity(target.intensity)
        },
      })
    }
  })

  const borderColor = () => {
    switch (props.status) {
      case "pending":
        return RGBA.fromInts(theme.accent.r * 255, theme.accent.g * 255, theme.accent.b * 255, intensity() * 255)
      case "running":
        return RGBA.fromInts(theme.accent.r * 255, theme.accent.g * 255, theme.accent.b * 255, intensity() * 255)
      case "completed":
        return theme.primary
      case "error":
        return theme.error
      default:
        return theme.border
    }
  }

  return (
    <box
      // DISABLED: pulsing border - using text animations instead
      // border={["left", "right", "top", "bottom"]}
      // borderColor={borderColor()}
      paddingLeft={3}
      paddingRight={2}
      paddingTop={props.inline ? 0 : 1}
      paddingBottom={props.inline ? 0 : 1}
    >
      {props.children}
    </box>
  )
}

/**
 * Bash Animation - Breathing text with spinner for terminal commands
 * Based on custom swarm agent animation
 */

// Bash color palette
const BASH_COLORS = {
  red: {
    primary: RGBA.fromHex("#ff4444"),
    dim: RGBA.fromHex("#992929"),
    muted: RGBA.fromHex("#661a1a"),
    faint: RGBA.fromHex("#4d1515"),
  },
  text: {
    command: RGBA.fromHex("#888888"),
    output: RGBA.fromHex("#555555"),
    muted: RGBA.fromHex("#404040"),
  },
}

const BASH_SPINNER_FRAMES = ["⋮", "⋰", "⋯", "⋱"]
const BREATH_CYCLE_MS = 3000
const BREATH_STEPS = 12

// Breathing spinner for bash
export function BashSpinner(props: { color?: RGBA }) {
  const baseColor = props.color ?? BASH_COLORS.red.primary
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    setFrame((f) => (f + 1) % BASH_SPINNER_FRAMES.length)
  }, 150)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: baseColor }}>{BASH_SPINNER_FRAMES[frame()]}</span>
}

// Breathing "bash" text that pulses through color gradient
export function BreathingBash(props: { color?: RGBA }) {
  const baseColor = props.color ?? BASH_COLORS.red.primary
  const [phase, setPhase] = createSignal(0)
  const stepMs = BREATH_CYCLE_MS / BREATH_STEPS

  // Build gradient from base color with varying opacity
  const getGradientColor = (phase: number): RGBA => {
    const opacities = [0.3, 0.4, 0.4, 0.6, 0.6, 1.0, 1.0, 0.6, 0.6, 0.4, 0.4, 0.3]
    const opacity = opacities[phase % opacities.length]
    return RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity * 255)
  }

  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % BREATH_STEPS)
  }, stepMs)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: getGradientColor(phase()) }}>bash</span>
}

// Complete Bash pending animation - spinner + breathing text + command
export function BashPending(props: { command?: string; description?: string; color?: RGBA }) {
  const { theme } = useTheme()
  const color = props.color ?? BASH_COLORS.red.primary

  return (
    <box gap={0}>
      <text>
        <BashSpinner color={color} /> <BreathingBash color={color} />
        <Show when={props.command}>
          {" "}
          <span style={{ fg: BASH_COLORS.red.dim }}>$</span> <span style={{ fg: theme.text }}>{props.command}</span>
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
    </box>
  )
}

// Bash running animation - shows command and streaming output
export function BashRunning(props: {
  command?: string
  description?: string
  output?: string
  startTime?: number
  color?: RGBA
}) {
  const { theme } = useTheme()
  const color = props.color ?? BASH_COLORS.red.primary

  return (
    <box gap={1}>
      <text>
        <BashSpinner color={color} /> <BreathingBash color={color} /> <span style={{ fg: theme.textMuted }}>$</span>{" "}
        <span style={{ fg: theme.text }}>{props.command ?? "..."}</span>{" "}
        <Show when={props.startTime}>
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
      <Show when={props.output}>
        <box paddingLeft={2}>
          <text fg={BASH_COLORS.text.output}>{props.output}</text>
        </box>
      </Show>
    </box>
  )
}

// Bash resolved animation - shows success/failure with output
export function BashResolved(props: {
  success: boolean
  command?: string
  description?: string
  output?: string
  exitCode?: number
  executionTime?: number
  color?: RGBA
}) {
  const { theme } = useTheme()
  const color = props.color ?? (props.success ? theme.primary : theme.error)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={1}>
      <text>
        <span style={{ fg: color, bold: true }}>{props.success ? "✓" : "✗"}</span>{" "}
        <span style={{ fg: BASH_COLORS.red.dim }}>bash</span> <span style={{ fg: theme.textMuted }}>$</span>{" "}
        <span style={{ fg: theme.text }}>{props.command}</span>
        <Show when={!props.success && props.exitCode !== undefined}>
          <span style={{ fg: BASH_COLORS.red.muted }}> exit {props.exitCode}</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          <span style={{ fg: theme.textMuted }}> {formatTime(props.executionTime!)}</span>
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
      <Show when={props.output}>
        <box paddingLeft={2}>
          <text fg={BASH_COLORS.text.output}>{props.output}</text>
        </box>
      </Show>
    </box>
  )
}

// Complete Bash tool animation that handles all states
export function BashToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  command?: string
  description?: string
  output?: string
  streamingOutput?: string
  startTime?: number
  executionTime?: number
  exitCode?: number
  color?: RGBA
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <BashPending command={props.command} description={props.description} color={props.color} />
      </Match>
      <Match when={props.status === "running"}>
        <BashRunning
          command={props.command}
          description={props.description}
          output={props.streamingOutput}
          startTime={props.startTime}
          color={props.color}
        />
      </Match>
      <Match when={props.status === "completed"}>
        <BashResolved
          success={true}
          command={props.command}
          description={props.description}
          output={props.output}
          executionTime={props.executionTime}
          color={props.color}
        />
      </Match>
      <Match when={props.status === "error"}>
        <BashResolved
          success={false}
          command={props.command}
          description={props.description}
          output={props.output}
          exitCode={props.exitCode}
          executionTime={props.executionTime}
          color={props.color}
        />
      </Match>
    </Switch>
  )
}

// ============================================================================
// GIT ANIMATION - Special styling for git commands
// ============================================================================

// Git color palette (purple like git-committer agent)
const GIT_COLORS = {
  primary: RGBA.fromHex("#bb9af7"), // Purple - main git color
  dim: RGBA.fromHex("#7c6aab"),
  muted: RGBA.fromHex("#5d4f80"),
  commit: RGBA.fromHex("#a6e3a1"), // Green for commits
  push: RGBA.fromHex("#89b4fa"), // Blue for push
  add: RGBA.fromHex("#f9e2af"), // Yellow for staging
  text: {
    command: RGBA.fromHex("#888888"),
    output: RGBA.fromHex("#555555"),
  },
}

// Git icon from nerdfonts (same as sidebar Source Control)
const ICON_GIT = "\u{ea68}" // cod-source_control

// Git operation types
export type GitOpType = "commit" | "push" | "add" | "pull" | "other"

// Get color based on git operation type
function getGitColor(op: GitOpType): RGBA {
  switch (op) {
    case "commit":
      return GIT_COLORS.commit
    case "push":
      return GIT_COLORS.push
    case "add":
      return GIT_COLORS.add
    default:
      return GIT_COLORS.primary
  }
}

const GIT_SPINNER_FRAMES = ["◐", "◓", "◑", "◒"]

// Breathing spinner for git
export function GitSpinner(props: { color?: RGBA }) {
  const baseColor = props.color ?? GIT_COLORS.primary
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    setFrame((f) => (f + 1) % GIT_SPINNER_FRAMES.length)
  }, 120)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: baseColor }}>{GIT_SPINNER_FRAMES[frame()]}</span>
}

// Breathing "git" text that pulses through color gradient
export function BreathingGit(props: { color?: RGBA }) {
  const baseColor = props.color ?? GIT_COLORS.primary
  const [phase, setPhase] = createSignal(0)
  const stepMs = BREATH_CYCLE_MS / BREATH_STEPS

  const getGradientColor = (phase: number): RGBA => {
    const opacities = [0.3, 0.4, 0.4, 0.6, 0.6, 1.0, 1.0, 0.6, 0.6, 0.4, 0.4, 0.3]
    const opacity = opacities[phase % opacities.length]
    return RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity * 255)
  }

  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % BREATH_STEPS)
  }, stepMs)

  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: getGradientColor(phase()) }}>{ICON_GIT}</span>
}

// Git pending animation - spinner + breathing text + command
export function GitPending(props: { command?: string; description?: string; gitOp: GitOpType }) {
  const { theme } = useTheme()
  const color = getGitColor(props.gitOp)

  return (
    <box gap={0}>
      <text>
        <GitSpinner color={color} /> <BreathingGit color={color} />
        <Show when={props.command} fallback="">
          {" "}
          <span style={{ fg: GIT_COLORS.dim }}>$</span> <span style={{ fg: theme.text }}>{props.command}</span>
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
    </box>
  )
}

// Git running animation - shows command and streaming output
export function GitRunning(props: {
  command?: string
  description?: string
  output?: string
  startTime?: number
  gitOp: GitOpType
}) {
  const { theme } = useTheme()
  const color = getGitColor(props.gitOp)

  return (
    <box gap={1}>
      <text>
        <GitSpinner color={color} /> <BreathingGit color={color} /> <span style={{ fg: theme.textMuted }}>$</span>{" "}
        <span style={{ fg: theme.text }}>{props.command ?? "..."}</span>{" "}
        <Show when={props.startTime} fallback="">
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
      <Show when={props.output}>
        <box paddingLeft={2}>
          <text fg={GIT_COLORS.text.output}>{props.output}</text>
        </box>
      </Show>
    </box>
  )
}

// Git resolved animation - shows success/failure with output
export function GitResolved(props: {
  success: boolean
  command?: string
  description?: string
  output?: string
  exitCode?: number
  executionTime?: number
  gitOp: GitOpType
}) {
  const { theme } = useTheme()
  const color = props.success ? getGitColor(props.gitOp) : theme.error

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  // Get success message based on git operation
  const successIcon = () => {
    if (!props.success) return "✗"
    switch (props.gitOp) {
      case "commit":
        return "✓"
      case "push":
        return "󰄿" // md-chevron_double_up
      case "pull":
        return "󰄼" // md-chevron_double_down
      case "add":
        return "+"
      default:
        return "✓"
    }
  }

  return (
    <box gap={1}>
      <text>
        <span style={{ fg: color, bold: true }}>{successIcon()}</span>{" "}
        <span style={{ fg: GIT_COLORS.dim }}>{ICON_GIT}</span> <span style={{ fg: theme.textMuted }}>$</span>{" "}
        <span style={{ fg: theme.text }}>{props.command}</span>
        <Show when={!props.success && props.exitCode !== undefined} fallback="">
          <span style={{ fg: GIT_COLORS.muted }}> exit {props.exitCode}</span>
        </Show>
        <Show when={props.executionTime !== undefined} fallback="">
          <span style={{ fg: theme.textMuted }}> {formatTime(props.executionTime!)}</span>
        </Show>
      </text>
      <Show when={props.description}>
        <text fg={theme.textMuted}>{props.description}</text>
      </Show>
      <Show when={props.output}>
        <box paddingLeft={2}>
          <text fg={GIT_COLORS.text.output}>{props.output}</text>
        </box>
      </Show>
    </box>
  )
}

// Complete Git tool animation that handles all states
export function GitToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  command?: string
  description?: string
  output?: string
  streamingOutput?: string
  startTime?: number
  executionTime?: number
  exitCode?: number
  gitOp: GitOpType
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <GitPending command={props.command} description={props.description} gitOp={props.gitOp} />
      </Match>
      <Match when={props.status === "running"}>
        <GitRunning
          command={props.command}
          description={props.description}
          output={props.streamingOutput}
          startTime={props.startTime}
          gitOp={props.gitOp}
        />
      </Match>
      <Match when={props.status === "completed"}>
        <GitResolved
          success={true}
          command={props.command}
          description={props.description}
          output={props.output}
          executionTime={props.executionTime}
          gitOp={props.gitOp}
        />
      </Match>
      <Match when={props.status === "error"}>
        <GitResolved
          success={false}
          command={props.command}
          description={props.description}
          output={props.output}
          exitCode={props.exitCode}
          executionTime={props.executionTime}
          gitOp={props.gitOp}
        />
      </Match>
    </Switch>
  )
}

// ============================================================================
// ROUNDED SQUARE SPINNER - Claude/Swarm branded spinner with state animations
// ============================================================================

// The rounded square icon (󱓼) from Nerd Fonts - md-square_rounded_outline
const ROUNDED_SQUARE = "󱓼"

// State colors
const CLAUDE_COLORS = {
  idle: RGBA.fromHex("#6b7280"), // Gray - dormant, waiting
  streaming: RGBA.fromHex("#a6e3a1"), // Green - active, receiving data
  thinking: RGBA.fromHex("#cba6f7"), // Purple - processing, reasoning
}

// ============================================================================
// THINKING SPINNER - Gems with rainbow color wave
// ============================================================================

// Nerd Font gem icons
const GEM_RHOMBUS = "󰜋"       // Diamond/rhombus
const GEM_TRIANGLE = "󰇂"      // Triangle filled
const GEM_TRIANGLE_OUT = "󰜌"  // Triangle outline

// Rainbow palette - RGB values
const THINKING_PALETTE: [number, number, number][] = [
  [243, 139, 168], // Pink
  [250, 179, 135], // Peach
  [249, 226, 175], // Yellow
  [166, 227, 161], // Green
  [137, 220, 235], // Sky
  [203, 166, 247], // Mauve
]

/**
 * Rounded Square Spinner - Idle State
 * Very slow, barely perceptible breathing - zen dormant state
 * ~4 second cycle, opacity 0.3 -> 0.6
 */
export function RoundedSquareIdle(props: { color?: RGBA }) {
  const color = props.color ?? CLAUDE_COLORS.idle
  const [opacity, setOpacity] = createSignal(0.3)

  const timeline = useTimeline({
    duration: 4000,
    loop: true,
  })

  const target = { opacity: 0.3, setOpacity }
  timeline!.add(target, {
    opacity: 0.6,
    duration: 2000,
    ease: "inOutSine",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>{ROUNDED_SQUARE}</span>
}

/**
 * Rounded Square Spinner - Streaming State
 * Gentle zen breathing animation - calm and steady
 * ~2 second cycle, opacity 0.5 -> 1.0
 */
export function RoundedSquareStreaming(props: { color?: RGBA }) {
  const color = props.color ?? CLAUDE_COLORS.streaming
  const [opacity, setOpacity] = createSignal(0.5)

  const timeline = useTimeline({
    duration: 2000,
    loop: true,
  })

  const target = { opacity: 0.5, setOpacity }
  timeline!.add(target, {
    opacity: 1.0,
    duration: 1000,
    ease: "inOutSine",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>{ROUNDED_SQUARE}</span>
}

/**
 * Rounded Square Spinner - Thinking State
 * Bolder pulsing with slight "bounce" - more active/intense
 * ~1.5 second cycle, opacity 0.6 -> 1.0, outBack easing for punch
 */
export function RoundedSquareThinking(props: { color?: RGBA }) {
  const color = props.color ?? CLAUDE_COLORS.thinking
  const [opacity, setOpacity] = createSignal(0.6)

  const timeline = useTimeline({
    duration: 1500,
    loop: true,
  })

  const target = { opacity: 0.6, setOpacity }
  timeline!.add(target, {
    opacity: 1.0,
    duration: 750,
    ease: "outBack",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>{ROUNDED_SQUARE}</span>
}

// ============================================================================
// HEXAGON OUTLINE SPINNER - For thinking/extended mode
// ============================================================================

const HEXAGON_OUTLINE_ICON = "" // oct-stack

/**
 * Hexagon Outline Spinner - Idle State
 * Same animation as RoundedSquareIdle but with hexagon outline icon
 */
export function HexagonOutlineIdle(props: { color?: RGBA }) {
  const color = props.color ?? CLAUDE_COLORS.idle
  const [opacity, setOpacity] = createSignal(0.3)

  const timeline = useTimeline({
    duration: 4000,
    loop: true,
  })

  const target = { opacity: 0.3, setOpacity }
  timeline!.add(target, {
    opacity: 0.6,
    duration: 2000,
    ease: "inOutSine",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>{HEXAGON_OUTLINE_ICON}</span>
}

/**
 * Hexagon Outline Spinner - Streaming State
 * Same animation as RoundedSquareStreaming but with hexagon outline icon
 */
export function HexagonOutlineStreaming(props: { color?: RGBA }) {
  const color = props.color ?? CLAUDE_COLORS.streaming
  const [opacity, setOpacity] = createSignal(0.5)

  const timeline = useTimeline({
    duration: 2000,
    loop: true,
  })

  const target = { opacity: 0.5, setOpacity }
  timeline!.add(target, {
    opacity: 1.0,
    duration: 1000,
    ease: "inOutSine",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>{HEXAGON_OUTLINE_ICON}</span>
}

/**
 * AgentStreamingWave - Smooth braille shimmer bar with agent color
 * Uses Shimmer component for smooth wave effect across braille blocks
 */
export function AgentStreamingWave(props: { color: RGBA; thinking?: boolean }) {
  // Smooth braille wave - always inherits agent color (7 blocks = 35% bigger than 5)
  return <Shimmer text="⣿⣿⣿⣿⣿⣿⣿" color={props.color} />
}

// ThinkingMorphSpinner removed - functionality merged into ThinkingSpinner

/**
 * Rounded Square Spinner - Universal component with state prop
 */
export function RoundedSquareSpinner(props: { state: "idle" | "streaming" | "thinking"; color?: RGBA }) {
  return (
    <Switch>
      <Match when={props.state === "idle"}>
        <RoundedSquareIdle color={props.color} />
      </Match>
      <Match when={props.state === "streaming"}>
        <RoundedSquareStreaming color={props.color} />
      </Match>
      <Match when={props.state === "thinking"}>
        <RoundedSquareThinking color={props.color} />
      </Match>
    </Switch>
  )
}

/**
 * Swarm Spinner - Three rounded squares (R, G, B) breathing together
 * Each slightly offset in phase for a wave effect
 */
export function SwarmSpinner(props: { state?: "idle" | "streaming" | "thinking" }) {
  const state = props.state ?? "streaming"
  const colors = [
    RGBA.fromHex("#f38ba8"), // Red/Pink
    RGBA.fromHex("#a6e3a1"), // Green
    RGBA.fromHex("#89b4fa"), // Blue
  ]

  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  // Different timing based on state
  const duration = state === "idle" ? 4000 : state === "streaming" ? 2000 : 1500
  const minOpacity = state === "idle" ? 0.3 : state === "streaming" ? 0.5 : 0.6

  // Create staggered breathing effect
  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = colors.map((_, i) => {
      const phase = ((now / duration) * Math.PI * 2 + i * 0.7) % (Math.PI * 2)
      return minOpacity + (1 - minOpacity) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color0 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacities()[0] * 255)
  const color1 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacities()[1] * 255)
  const color2 = () => RGBA.fromInts(colors[2].r * 255, colors[2].g * 255, colors[2].b * 255, opacities()[2] * 255)

  return (
    <text>
      <span style={{ fg: color0() }}>{ROUNDED_SQUARE}</span>
      <span style={{ fg: color1() }}>{ROUNDED_SQUARE}</span>
      <span style={{ fg: color2() }}>{ROUNDED_SQUARE}</span>
    </text>
  )
}

// ============================================================================
// BACKGROUND AGENT SPINNERS
// Animated indicators for background agent status
// ============================================================================

const PULSE_ICON = "󰐰" // md-pulse

/**
 * BackgroundAgentSpinner - Three pulse icons with wave breathing
 * Uses cyan/teal/blue colors for activity/heartbeat feel
 */
export function BackgroundAgentSpinner(props: { state?: "idle" | "active" | "busy" }) {
  const state = props.state ?? "active"
  const colors = [
    RGBA.fromHex("#89dceb"), // Cyan
    RGBA.fromHex("#94e2d5"), // Teal
    RGBA.fromHex("#89b4fa"), // Blue
  ]

  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  const duration = state === "idle" ? 4000 : state === "active" ? 2000 : 1200
  const minOpacity = state === "idle" ? 0.3 : state === "active" ? 0.5 : 0.6

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = colors.map((_, i) => {
      const phase = ((now / duration) * Math.PI * 2 + i * 0.7) % (Math.PI * 2)
      return minOpacity + (1 - minOpacity) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color0 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacities()[0] * 255)
  const color1 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacities()[1] * 255)
  const color2 = () => RGBA.fromInts(colors[2].r * 255, colors[2].g * 255, colors[2].b * 255, opacities()[2] * 255)

  return (
    <text>
      <span style={{ fg: color0() }}>{PULSE_ICON}</span>
      <span style={{ fg: color1() }}>{PULSE_ICON}</span>
      <span style={{ fg: color2() }}>{PULSE_ICON}</span>
    </text>
  )
}

// ============================================================================
// EXTENDED THINKING ANIMATIONS
// Used for the thinking toggle indicator and reasoning block headers
// ============================================================================

const FLARE = "󰵲" // md-flare - starburst shape for thinking indicator
const HEXAGON_ICON = "" // oct-stack

/**
 * ThinkingIndicator - Status bar indicator showing extended thinking is enabled
 * Static hexagon outline icon in purple
 */
export function ThinkingIndicator() {
  const color = RGBA.fromHex("#cba6f7") // Purple
  return <span style={{ fg: color }}>{HEXAGON_OUTLINE_ICON}</span>
}

/**
 * ThinkingSpinner - Gems with rainbow color wave
 * Rhombus and triangles morphing with smooth color cycling
 * - streaming: animated rainbow wave + icon morphing
 * - resolved: solid purple, settled
 */
export function ThinkingSpinner(props: { state: "streaming" | "resolved" }) {
  const resolvedColor = RGBA.fromHex("#cba6f7")
  const [time, setTime] = createSignal(Date.now())

  createEffect(() => {
    if (props.state === "resolved") return

    const interval = setInterval(() => {
      setTime(Date.now())
    }, 40)

    onCleanup(() => clearInterval(interval))
  })

  // Interpolate color through palette with offset for wave effect
  const getColor = (offset: number) => {
    if (props.state === "resolved") return resolvedColor
    const t = (time() / 150 + offset) % THINKING_PALETTE.length
    const i = Math.floor(t)
    const f = t - i
    const n = (i + 1) % THINKING_PALETTE.length
    const r = THINKING_PALETTE[i][0] + (THINKING_PALETTE[n][0] - THINKING_PALETTE[i][0]) * f
    const g = THINKING_PALETTE[i][1] + (THINKING_PALETTE[n][1] - THINKING_PALETTE[i][1]) * f
    const b = THINKING_PALETTE[i][2] + (THINKING_PALETTE[n][2] - THINKING_PALETTE[i][2]) * f
    return RGBA.fromInts(Math.round(r), Math.round(g), Math.round(b), 255)
  }

  // Morph between gem icons (400ms like plan mode)
  const gemIcons = [GEM_RHOMBUS, GEM_TRIANGLE, GEM_RHOMBUS, GEM_TRIANGLE_OUT]
  const getIcon = (offset: number) => {
    if (props.state === "resolved") return GEM_RHOMBUS
    const idx = Math.floor((time() / 400 + offset) % gemIcons.length)
    return gemIcons[idx]
  }

  return (
    <>
      <span style={{ fg: getColor(0) }}>{getIcon(0)}</span>
      <span style={{ fg: getColor(1) }}>{getIcon(1)}</span>
      <span style={{ fg: getColor(2) }}>{getIcon(2)}</span>
    </>
  )
}

/**
 * Plan Mode Spinner - Three squares building up with wave animation
 * Used when Claude is in plan mode / exit plan mode
 */
export function PlanModeSpinner(props: { state?: "thinking" | "ready" | "approved" }) {
  const state = props.state ?? "thinking"
  const [phase, setPhase] = createSignal(0)
  const [buildUp, setBuildUp] = createSignal(0) // 0, 1, 2, 3 squares visible

  // Colors
  const colors = {
    thinking: RGBA.fromHex("#cba6f7"), // Purple
    ready: RGBA.fromHex("#a6e3a1"), // Green
    approved: RGBA.fromHex("#89b4fa"), // Cyan/Blue
  }

  const baseColor = colors[state]

  // Build up animation for "thinking" state
  createEffect(() => {
    if (state === "thinking") {
      const buildInterval = setInterval(() => {
        setBuildUp((b) => (b + 1) % 4) // 0 -> 1 -> 2 -> 3 -> 0
      }, 400)
      onCleanup(() => clearInterval(buildInterval))
    } else {
      setBuildUp(3) // Show all squares for ready/approved
    }
  })

  // Wave animation for opacity
  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % 100)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const getOpacity = (index: number) => {
    if (state === "approved") return 1.0 // Solid for approved
    const visible = index < buildUp() || state !== "thinking"
    if (!visible) return 0.15
    const wave = Math.sin((phase() / 100) * Math.PI * 2 + index * 0.8)
    return 0.4 + (wave + 1) * 0.3 // Range: 0.4 - 1.0
  }

  const color0 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, getOpacity(0) * 255)
  const color1 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, getOpacity(1) * 255)
  const color2 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, getOpacity(2) * 255)

  return (
    <text>
      <span style={{ fg: color0() }}>{ROUNDED_SQUARE}</span>
      <span style={{ fg: color1() }}>{ROUNDED_SQUARE}</span>
      <span style={{ fg: color2() }}>{ROUNDED_SQUARE}</span>
    </text>
  )
}

/**
 * Plan Mode Showcase - Shows all plan spinner states
 */
export function PlanModeShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={1}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Plan Mode Spinner States:
      </text>

      <box flexDirection="row" gap={4}>
        <box flexDirection="column" alignItems="center">
          <text>
            <PlanModeSpinner state="thinking" />
          </text>
          <text style={{ fg: theme.textMuted }}>thinking</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <PlanModeSpinner state="ready" />
          </text>
          <text style={{ fg: theme.textMuted }}>ready</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <PlanModeSpinner state="approved" />
          </text>
          <text style={{ fg: theme.textMuted }}>approved</text>
        </box>
      </box>
    </box>
  )
}

/**
 * Spinner Showcase - Shows all rounded square states side by side
 */
export function SpinnerShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={1}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Rounded Square Spinner States:
      </text>

      <box flexDirection="row" gap={4}>
        <box flexDirection="column" alignItems="center">
          <RoundedSquareIdle />
          <text style={{ fg: theme.textMuted }}>idle</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <RoundedSquareStreaming />
          <text style={{ fg: theme.textMuted }}>streaming</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <RoundedSquareThinking />
          <text style={{ fg: theme.textMuted }}>thinking</text>
        </box>
      </box>

      <box height={1} />

      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Swarm Spinner (RGB):
      </text>
      <box flexDirection="row" gap={4}>
        <box flexDirection="column" alignItems="center">
          <SwarmSpinner state="idle" />
          <text style={{ fg: theme.textMuted }}>idle</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <SwarmSpinner state="streaming" />
          <text style={{ fg: theme.textMuted }}>streaming</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <SwarmSpinner state="thinking" />
          <text style={{ fg: theme.textMuted }}>thinking</text>
        </box>
      </box>
    </box>
  )
}

// ============================================================================
// TYPE A: STATIC BREATHING EDIT/WRITE ANIMATIONS - Nerdfont icons with pulse
// Uses pastel colors from Catppuccin palette
// ============================================================================

const EDIT_WRITE_COLORS = {
  pink: RGBA.fromHex("#f38ba8"),
  green: RGBA.fromHex("#a6e3a1"),
  blue: RGBA.fromHex("#89b4fa"),
  purple: RGBA.fromHex("#cba6f7"),
  peach: RGBA.fromHex("#fab387"),
  yellow: RGBA.fromHex("#f9e2af"),
  teal: RGBA.fromHex("#94e2d5"),
}

// Nerdfont icons
const RHOMBUS = "󰜋" // md-rhombus
const RHOMBUS_OUTLINE = "󰜌" // md-rhombus_outline
const HEXAGON = "󰋘" // md-hexagon
const HEXAGON_OUTLINE = "󰋙" // md-hexagon_outline
const SQUARE_FILLED = "󱓻" // md-square_rounded
const SQUARE_OUTLINE = "󱓼" // md-square_rounded_outline
const DELTA = "󰇂" // md-delta
const STAR_FOUR = "󰫢" // md-star_four_points

/**
 * Edit Rhombus Pulse - Single diamond with color-cycling breathing
 * Pulses through purple → pink → blue with opacity wave
 */
export function EditRhombusPulse(props: { color?: RGBA }) {
  const colors = [EDIT_WRITE_COLORS.purple, EDIT_WRITE_COLORS.pink, EDIT_WRITE_COLORS.blue]
  const [colorIndex, setColorIndex] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.5)

  // Color cycling
  const colorInterval = setInterval(() => {
    setColorIndex((i) => (i + 1) % colors.length)
  }, 2000)

  // Breathing opacity
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1500) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.4 + (Math.sin(phase) + 1) * 0.3)
  }, 50)

  onCleanup(() => {
    clearInterval(colorInterval)
    clearInterval(breathInterval)
  })

  const color = () => {
    const base = props.color ?? colors[colorIndex()]
    return RGBA.fromInts(base.r * 255, base.g * 255, base.b * 255, opacity() * 255)
  }

  return (
    <text>
      <span style={{ fg: color() }}>{RHOMBUS}</span>
    </text>
  )
}

/**
 * Write Triple Hexagon - Three hexagons breathing in wave pattern
 * Like SwarmSpinner but with hexagons and write-focused colors
 */
export function WriteTripleHexagon(props: { colors?: RGBA[] }) {
  const colors = props.colors ?? [EDIT_WRITE_COLORS.green, EDIT_WRITE_COLORS.teal, EDIT_WRITE_COLORS.blue]
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = colors.map((_, i) => {
      const phase = ((now / 1800) * Math.PI * 2 + i * 0.8) % (Math.PI * 2)
      return 0.4 + (1 - 0.4) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const c0 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(colors[2].r * 255, colors[2].g * 255, colors[2].b * 255, opacities()[2] * 255)

  return (
    <text>
      <span style={{ fg: c0() }}>{HEXAGON}</span>
      <span style={{ fg: c1() }}>{HEXAGON}</span>
      <span style={{ fg: c2() }}>{HEXAGON}</span>
    </text>
  )
}

/**
 * Edit Dual Square - Two squares that alternate breathing
 * Filled and outline squares pulse in opposition
 */
export function EditDualSquare(props: { colors?: [RGBA, RGBA] }) {
  const colors = props.colors ?? [EDIT_WRITE_COLORS.purple, EDIT_WRITE_COLORS.teal]
  const [phase, setPhase] = createSignal(0)

  const interval = setInterval(() => {
    const now = Date.now()
    setPhase(((now / 1200) * Math.PI * 2) % (Math.PI * 2))
  }, 50)

  onCleanup(() => clearInterval(interval))

  const opacity1 = () => 0.4 + (Math.sin(phase()) + 1) * 0.3
  const opacity2 = () => 0.4 + (Math.sin(phase() + Math.PI) + 1) * 0.3

  const color1 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacity1() * 255)
  const color2 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacity2() * 255)

  return (
    <text>
      <span style={{ fg: color1() }}>{SQUARE_FILLED}</span>
      <span style={{ fg: color2() }}>{SQUARE_OUTLINE}</span>
    </text>
  )
}

/**
 * Write Delta Wave - Three deltas breathing in sequence
 * Creates a flowing wave effect with triangles
 */
export function WriteDeltaWave(props: { color?: RGBA }) {
  const baseColor = props.color ?? EDIT_WRITE_COLORS.green
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = [0, 1, 2].map((i) => {
      const phase = ((now / 1500) * Math.PI * 2 + i * 0.6) % (Math.PI * 2)
      return 0.3 + (1 - 0.3) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const c0 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[2] * 255)

  return (
    <text>
      <span style={{ fg: c0() }}>{DELTA}</span>
      <span style={{ fg: c1() }}>{DELTA}</span>
      <span style={{ fg: c2() }}>{DELTA}</span>
    </text>
  )
}

/**
 * Edit Star Burst - Four-point star with intense pulsing
 * More energetic breathing for active editing
 */
export function EditStarBurst(props: { color?: RGBA }) {
  const baseColor = props.color ?? EDIT_WRITE_COLORS.peach
  const [opacity, setOpacity] = createSignal(0.5)
  const [icon, setIcon] = createSignal(STAR_FOUR)

  // Faster breathing for more energy
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 800) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 30)

  // Alternate between star variants
  const iconInterval = setInterval(() => {
    setIcon((i) => (i === STAR_FOUR ? "󰫣" : STAR_FOUR)) // star_four_points_outline
  }, 600)

  onCleanup(() => {
    clearInterval(breathInterval)
    clearInterval(iconInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return (
    <text>
      <span style={{ fg: color() }}>{icon()}</span>
    </text>
  )
}

/**
 * Write Rhombus Chain - Three rhombus shapes breathing in chain
 * Smooth wave effect with diamonds
 */
export function WriteRhombusChain(props: { colors?: RGBA[] }) {
  const colors = props.colors ?? [EDIT_WRITE_COLORS.pink, EDIT_WRITE_COLORS.peach, EDIT_WRITE_COLORS.yellow]
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = colors.map((_, i) => {
      const phase = ((now / 2000) * Math.PI * 2 + i * 0.7) % (Math.PI * 2)
      return 0.35 + (1 - 0.35) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const c0 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(colors[2].r * 255, colors[2].g * 255, colors[2].b * 255, opacities()[2] * 255)

  return (
    <text>
      <span style={{ fg: c0() }}>{RHOMBUS}</span>
      <span style={{ fg: c1() }}>{RHOMBUS}</span>
      <span style={{ fg: c2() }}>{RHOMBUS}</span>
    </text>
  )
}

/**
 * Edit Delta Morph Chain - Three shapes that slowly morph through delta → rhombus → square
 * Each shape is offset in phase creating a slow wave of transformation
 */
export function EditDeltaMorphChain(props: { color?: RGBA }) {
  const baseColor = props.color ?? EDIT_WRITE_COLORS.purple
  const shapes = [DELTA, RHOMBUS_OUTLINE, RHOMBUS, SQUARE_OUTLINE, SQUARE_FILLED]
  const [indices, setIndices] = createSignal([0, 1, 2])
  const [opacities, setOpacities] = createSignal([0.7, 0.7, 0.7])

  // Slow shape morphing - each icon cycles through shapes with offset
  const morphInterval = setInterval(() => {
    setIndices((prev) =>
      prev.map((idx, i) => {
        const offset = i * 1.2
        const time = Date.now() / 1200 + offset
        return Math.floor(time) % shapes.length
      }),
    )
  }, 100)

  // Gentle breathing
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const newOpacities = [0, 1, 2].map((i) => {
      const phase = ((now / 2500) * Math.PI * 2 + i * 0.5) % (Math.PI * 2)
      return 0.5 + (Math.sin(phase) + 1) * 0.25
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const c0 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities()[2] * 255)

  return (
    <>
      <span style={{ fg: c0() }}>{shapes[indices()[0]]}</span>
      <span style={{ fg: c1() }}>{shapes[indices()[1]]}</span>
      <span style={{ fg: c2() }}>{shapes[indices()[2]]}</span>
    </>
  )
}

/**
 * Edit/Write Animation Showcase - Shows the best animation types
 */
export function EditWriteShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={1}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Edit/Write Animations
      </text>

      <box flexDirection="row" gap={3}>
        <box flexDirection="column" alignItems="center">
          <WriteTripleHexagon />
          <text style={{ fg: theme.textMuted }}>triple hex</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <WriteRhombusChain />
          <text style={{ fg: theme.textMuted }}>rhombus chain</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <EditDeltaMorphChain />
          <text style={{ fg: theme.textMuted }}>delta morph</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <GenericSpinner spinner="write_hexagon_slide" />
          </text>
          <text style={{ fg: theme.textMuted }}>hex slide</text>
        </box>
      </box>
    </box>
  )
}

/**
 * Matrix-style falling characters effect
 */
export function MatrixRain(props: { width?: number; height?: number }) {
  const { theme } = useTheme()
  const width = props.width ?? 20
  const height = props.height ?? 5
  const [drops, setDrops] = createSignal<number[]>(Array(width).fill(0))

  const interval = setInterval(() => {
    setDrops((prev) =>
      prev.map((y) => {
        if (y > height && Math.random() > 0.975) return 0
        return y + 1
      }),
    )
  }, 100)

  onCleanup(() => clearInterval(interval))

  const chars = "01"

  return (
    <box>
      <For each={Array(height).fill(0)}>
        {(_, row) => (
          <text>
            <For each={drops()}>
              {(drop, col) => {
                const isActive = drop === row()
                const opacity = isActive ? 1 : Math.max(0, 1 - (row() - drop) * 0.3)
                const color = RGBA.fromInts(
                  theme.success.r * 255,
                  theme.success.g * 255,
                  theme.success.b * 255,
                  opacity * 255,
                )
                const char = chars[Math.floor(Math.random() * chars.length)]
                return <span style={{ fg: color }}>{isActive && drop > 0 ? char : " "}</span>
              }}
            </For>
          </text>
        )}
      </For>
    </box>
  )
}

// ============================================================================
// WEBFETCH DOWNLOAD ANIMATION - Wave motion with Catppuccin color cycling
// Full flow: Pending → Running → Resolved (like Bash animations)
// ============================================================================

// Catppuccin Mocha pastel colors - cycles through these palettes (cool tones only)
const WEBFETCH_COLORS = {
  // Primary palettes that cycle - all cool/pastel tones, no pink/red
  palettes: [
    [RGBA.fromHex("#89b4fa"), RGBA.fromHex("#74c7ec"), RGBA.fromHex("#89dceb")], // Blue → Sky → Sapphire
    [RGBA.fromHex("#94e2d5"), RGBA.fromHex("#89dceb"), RGBA.fromHex("#89b4fa")], // Teal → Sapphire → Blue
    [RGBA.fromHex("#a6e3a1"), RGBA.fromHex("#94e2d5"), RGBA.fromHex("#74c7ec")], // Green → Teal → Sky
    [RGBA.fromHex("#cba6f7"), RGBA.fromHex("#89b4fa"), RGBA.fromHex("#94e2d5")], // Mauve → Blue → Teal
    [RGBA.fromHex("#74c7ec"), RGBA.fromHex("#a6e3a1"), RGBA.fromHex("#89b4fa")], // Sky → Green → Blue
  ],
  // Static colors
  bracket: RGBA.fromHex("#585b70"), // Surface2
  dim: RGBA.fromHex("#45475a"), // Surface1
  success: RGBA.fromHex("#a6e3a1"), // Green
  error: RGBA.fromHex("#f38ba8"), // Red/Pink (only for errors)
}

// Block characters for smooth wave fill
const WAVE_BLOCKS = ["░", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"]

/**
 * WebfetchSpinner - Three download arrows breathing in wave like SwarmSpinner
 * Used for pending/running states
 */
export function WebfetchSpinner(props: { paletteIndex?: number }) {
  const palette = WEBFETCH_COLORS.palettes[props.paletteIndex ?? 0]
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  // Wave breathing effect - staggered like SwarmSpinner
  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = palette.map((_, i) => {
      const phase = ((now / 1800) * Math.PI * 2 + i * 0.7) % (Math.PI * 2)
      return 0.4 + (1 - 0.4) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const c0 = () => RGBA.fromInts(palette[0].r * 255, palette[0].g * 255, palette[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(palette[1].r * 255, palette[1].g * 255, palette[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(palette[2].r * 255, palette[2].g * 255, palette[2].b * 255, opacities()[2] * 255)

  return (
    <>
      <span style={{ fg: c0() }}>↓</span>
      <span style={{ fg: c1() }}>↓</span>
      <span style={{ fg: c2() }}>↓</span>
    </>
  )
}

/**
 * BreathingWebfetch - "webfetch" text with breathing opacity like BreathingBash
 */
export function BreathingWebfetch(props: { color?: RGBA }) {
  const baseColor = props.color ?? WEBFETCH_COLORS.palettes[0][0]
  const [phase, setPhase] = createSignal(0)

  const opacities = [0.3, 0.4, 0.5, 0.6, 0.8, 1.0, 1.0, 0.8, 0.6, 0.5, 0.4, 0.3]

  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % opacities.length)
  }, 200)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacities[phase()] * 255)

  return <span style={{ fg: color() }}>webfetch</span>
}

/**
 * WebfetchProgressBar - Wave-filled progress bar with color interpolation
 */
export function WebfetchProgressBar(props: { width?: number; paletteIndex?: number }) {
  const width = props.width ?? 8
  const totalSteps = width * WAVE_BLOCKS.length
  const [step, setStep] = createSignal(0)
  const [currentPalette, setCurrentPalette] = createSignal(props.paletteIndex ?? 0)

  // Progress animation - fills then cycles palette
  const interval = setInterval(() => {
    setStep((s) => {
      const next = s + 1
      if (next >= totalSteps) {
        setCurrentPalette((p) => (p + 1) % WEBFETCH_COLORS.palettes.length)
        return 0
      }
      return next
    })
  }, 35)

  onCleanup(() => clearInterval(interval))

  // Interpolate color through current palette based on progress
  const getColor = () => {
    const palette = WEBFETCH_COLORS.palettes[currentPalette()]
    const progress = step() / totalSteps
    const idx = Math.floor(progress * (palette.length - 1))
    const t = progress * (palette.length - 1) - idx
    const c1 = palette[Math.min(idx, palette.length - 1)]
    const c2 = palette[Math.min(idx + 1, palette.length - 1)]

    return RGBA.fromInts(
      Math.round(c1.r * 255 * (1 - t) + c2.r * 255 * t),
      Math.round(c1.g * 255 * (1 - t) + c2.g * 255 * t),
      Math.round(c1.b * 255 * (1 - t) + c2.b * 255 * t),
      255,
    )
  }

  // Build progress bar with wave blocks
  const bar = () => {
    const fullChars = Math.floor(step() / WAVE_BLOCKS.length)
    const partialIdx = step() % WAVE_BLOCKS.length
    const emptyChars = width - fullChars - (partialIdx > 0 ? 1 : 0)

    let result = "█".repeat(fullChars)
    if (partialIdx > 0 && fullChars < width) result += WAVE_BLOCKS[partialIdx]
    result += "░".repeat(Math.max(0, emptyChars))
    return result
  }

  return (
    <>
      <span style={{ fg: WEBFETCH_COLORS.bracket }}>[</span>
      <span style={{ fg: getColor() }}>{bar()}</span>
      <span style={{ fg: WEBFETCH_COLORS.bracket }}>]</span>
    </>
  )
}

/**
 * WebfetchPending - Spinner + breathing text + URL (like BashPending)
 */
export function WebfetchPending(props: { url?: string }) {
  const { theme } = useTheme()
  const [paletteIndex, setPaletteIndex] = createSignal(0)

  // Cycle palette slowly
  const interval = setInterval(() => {
    setPaletteIndex((p) => (p + 1) % WEBFETCH_COLORS.palettes.length)
  }, 3000)

  onCleanup(() => clearInterval(interval))

  const palette = () => WEBFETCH_COLORS.palettes[paletteIndex()]

  return (
    <box gap={0}>
      <text>
        <WebfetchSpinner paletteIndex={paletteIndex()} /> <BreathingWebfetch color={palette()[0]} />{" "}
        <span style={{ fg: theme.textMuted }}>{props.url ?? "..."}</span>
      </text>
    </box>
  )
}

/**
 * WebfetchRunning - Progress bar + breathing text + URL + elapsed time (like BashRunning)
 */
export function WebfetchRunning(props: { url?: string; startTime?: number }) {
  const { theme } = useTheme()
  const [paletteIndex, setPaletteIndex] = createSignal(0)

  // Cycle palette on each progress bar fill
  const interval = setInterval(() => {
    setPaletteIndex((p) => (p + 1) % WEBFETCH_COLORS.palettes.length)
  }, 2500)

  onCleanup(() => clearInterval(interval))

  const palette = () => WEBFETCH_COLORS.palettes[paletteIndex()]

  return (
    <box gap={0}>
      <text>
        <WebfetchProgressBar paletteIndex={paletteIndex()} /> <BreathingWebfetch color={palette()[1]} />{" "}
        <span style={{ fg: theme.text }}>{props.url ?? "..."}</span>{" "}
        <Show when={props.startTime}>
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * WebfetchResolved - Success/failure with URL, content type, size, and time (like BashResolved)
 */
export function WebfetchResolved(props: {
  success: boolean
  url?: string
  executionTime?: number
  bytesReceived?: number
  contentType?: string
}) {
  const { theme } = useTheme()
  const color = props.success ? WEBFETCH_COLORS.success : WEBFETCH_COLORS.error

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  const formatBytes = (bytes: number) => {
    if (bytes < 1024) return `${bytes}B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)}KB`
    return `${(bytes / (1024 * 1024)).toFixed(1)}MB`
  }

  // Extract short content type (e.g., "text/html" from "text/html; charset=utf-8")
  const shortContentType = () => {
    if (!props.contentType) return undefined
    const ct = props.contentType.split(";")[0].trim()
    // Shorten common types
    if (ct === "text/html") return "html"
    if (ct === "text/plain") return "text"
    if (ct === "application/json") return "json"
    if (ct === "text/markdown") return "md"
    return ct
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: color, bold: true }}>{props.success ? "✓" : "✗"}</span>{" "}
        <span style={{ fg: WEBFETCH_COLORS.dim }}>webfetch</span> <span style={{ fg: theme.text }}>{props.url}</span>
        <Show when={shortContentType()}>
          <span style={{ fg: WEBFETCH_COLORS.palettes[0][1] }}> {shortContentType()}</span>
        </Show>
        <Show when={props.bytesReceived !== undefined}>
          <span style={{ fg: theme.textMuted }}> {formatBytes(props.bytesReceived!)}</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          <span style={{ fg: theme.textMuted }}> {formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * WebfetchToolAnimation - Complete flow handler (like BashToolAnimation)
 */
export function WebfetchToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  url?: string
  startTime?: number
  executionTime?: number
  bytesReceived?: number
  contentType?: string
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <WebfetchPending url={props.url} />
      </Match>
      <Match when={props.status === "running"}>
        <WebfetchRunning url={props.url} startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <WebfetchResolved
          success={true}
          url={props.url}
          executionTime={props.executionTime}
          bytesReceived={props.bytesReceived}
          contentType={props.contentType}
        />
      </Match>
      <Match when={props.status === "error"}>
        <WebfetchResolved success={false} url={props.url} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

/**
 * WebfetchShowcase - Shows all webfetch animation states
 */
export function WebfetchShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={1}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Webfetch Animations:
      </text>

      <box flexDirection="row" gap={3}>
        <box flexDirection="column">
          <text style={{ fg: theme.textMuted }}>spinner:</text>
          <text>
            <WebfetchSpinner />
          </text>
        </box>

        <box flexDirection="column">
          <text style={{ fg: theme.textMuted }}>progress:</text>
          <text>
            <WebfetchProgressBar />
          </text>
        </box>
      </box>

      <text style={{ fg: theme.textMuted }}>↑ cycles through 5 Catppuccin palettes with wave motion</text>
    </box>
  )
}

// ============================================================================
// ASK-USER ANIMATION - Interactive question tool with progress visualization
// Full flow: Pending → Running → Resolved
// ============================================================================

// Chat/Message icons - these look great for ask-user
const MSG = "󰍡" // md-message
const MSG_QUESTION = "󱜺" // md-message_question
const MSG_PROCESSING = "󰍦" // md-message_processing
const MSG_REPLY = "󰍧" // md-message_reply
const MSG_FLASH = "󱖩" // md-message_flash
const MSG_FAST = "󱧌" // md-message_fast
const CHAT = "󰭹" // md-chat
const CHAT_QUESTION = "󱜸" // md-chat_question
const CHAT_PROCESSING = "󰭻" // md-chat_processing
const COMMENT_QUESTION = "󰠗" // md-comment_question
const FORUM = "󰊌" // md-forum (multiple bubbles)
const THOUGHT_BUBBLE = "󰟶" // md-thought_bubble

// Question marks - ALL THE ?s!
const FA_QUESTION = "" // fa-question (simple ?)
const HELP_CIRCLE = "󰋗" // md-help_circle
const HELP_RHOMBUS = "󰮥" // md-help_rhombus (? in diamond)
const HEAD_QUESTION = "󱍊" // md-head_question (head with ?)
const LIGHTBULB_QUESTION = "󱧣" // md-lightbulb_question
const ACCOUNT_QUESTION = "󰭙" // md-account_question
const CIRCLE_QUESTION = "" // fa-circle_question
const COD_QUESTION = "" // cod-question

// For progress
const CIRCLE_EMPTY = "󰄰"
const CIRCLE_FILLED = "󰄳"
const CIRCLE_SLICES = ["󰪞", "󰪟", "󰪠", "󰪡", "󰪢", "󰪣", "󰪤", "󰪥"]

// Catppuccin colors for ask-user
const ASKUSER_COLORS = {
  palettes: [
    [RGBA.fromHex("#cba6f7"), RGBA.fromHex("#b4befe"), RGBA.fromHex("#89b4fa")], // Mauve → Lavender → Blue
    [RGBA.fromHex("#f9e2af"), RGBA.fromHex("#fab387"), RGBA.fromHex("#cba6f7")], // Yellow → Peach → Mauve
    [RGBA.fromHex("#94e2d5"), RGBA.fromHex("#89dceb"), RGBA.fromHex("#b4befe")], // Teal → Sapphire → Lavender
  ],
  bracket: RGBA.fromHex("#585b70"),
  dim: RGBA.fromHex("#45475a"),
  success: RGBA.fromHex("#a6e3a1"),
  error: RGBA.fromHex("#f38ba8"),
  waiting: RGBA.fromHex("#f9e2af"),
}

/**
 * AskUserTrain - Chat bubble traveling left to right with bubble trail
 * Morphs through different chat icons as it travels
 */
export function AskUserTrain(props: { width?: number; style?: "dots" | "bubbles" | "fade" }) {
  const width = props.width ?? 6
  const style = props.style ?? "bubbles"
  const [position, setPosition] = createSignal(0)
  const [iconIdx, setIconIdx] = createSignal(0)
  const palette = ASKUSER_COLORS.palettes[0]
  const icons = [MSG_QUESTION, CHAT_QUESTION, COMMENT_QUESTION, THOUGHT_BUBBLE]
  const trailIcons = [MSG, CHAT, MSG_PROCESSING, CHAT_PROCESSING]

  const interval = setInterval(() => {
    setPosition((p) => {
      const next = (p + 1) % (width * 2)
      if (next === 0) setIconIdx((c) => (c + 1) % icons.length)
      return next
    })
  }, 100)

  onCleanup(() => clearInterval(interval))

  const pos = () => (position() < width ? position() : width * 2 - position() - 1)

  const chars = () => {
    const result: Array<{ char: string; opacity: number; colorIdx: number }> = []
    const p = pos()
    for (let i = 0; i < width; i++) {
      if (i === p) {
        result.push({ char: icons[iconIdx()], opacity: 1, colorIdx: 0 })
      } else if (i === p - 1 && style === "bubbles") {
        result.push({ char: trailIcons[iconIdx()], opacity: 0.7, colorIdx: 1 })
      } else if (i === p - 2 && style === "bubbles") {
        result.push({ char: trailIcons[(iconIdx() + 1) % trailIcons.length], opacity: 0.4, colorIdx: 2 })
      } else if (i === p - 1 && style === "dots") {
        result.push({ char: "●", opacity: 0.6, colorIdx: 1 })
      } else if (i === p - 2 && style === "dots") {
        result.push({ char: "·", opacity: 0.3, colorIdx: 2 })
      } else if (i === p - 1 && style === "fade") {
        result.push({ char: icons[iconIdx()], opacity: 0.5, colorIdx: 1 })
      } else if (i === p - 2 && style === "fade") {
        result.push({ char: icons[iconIdx()], opacity: 0.2, colorIdx: 2 })
      } else {
        result.push({ char: " ", opacity: 0, colorIdx: 0 })
      }
    }
    return result
  }

  return (
    <>
      {chars().map((c) => {
        const color = RGBA.fromInts(
          palette[c.colorIdx].r * 255,
          palette[c.colorIdx].g * 255,
          palette[c.colorIdx].b * 255,
          c.opacity * 255,
        )
        return <span style={{ fg: color }}>{c.char}</span>
      })}
    </>
  )
}

/**
 * AskUserBubbleBounce - Two bubbles bouncing back and forth, passing each other
 */
export function AskUserBubbleBounce(props: { width?: number }) {
  const width = props.width ?? 5
  const [pos1, setPos1] = createSignal(0)
  const [pos2, setPos2] = createSignal(width - 1)
  const palette = ASKUSER_COLORS.palettes[0]
  const icons1 = [MSG_QUESTION, CHAT_QUESTION]
  const icons2 = [COMMENT_QUESTION, THOUGHT_BUBBLE]
  const [frame, setFrame] = createSignal(0)

  const interval = setInterval(() => {
    const now = Date.now()
    const cycle = Math.floor(now / 150) % (width * 2)
    setPos1(cycle < width ? cycle : width * 2 - cycle - 1)
    setPos2(cycle < width ? width - 1 - cycle : cycle - width)
    setFrame(Math.floor(now / 600) % 2)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const chars = () => {
    const result: Array<{ char: string; colorIdx: number; opacity: number }> = []
    const p1 = pos1()
    const p2 = pos2()
    for (let i = 0; i < width; i++) {
      if (i === p1 && i === p2) {
        result.push({ char: FORUM, colorIdx: 0, opacity: 1 }) // Collision = forum (multiple)
      } else if (i === p1) {
        result.push({ char: icons1[frame()], colorIdx: 0, opacity: 1 })
      } else if (i === p2) {
        result.push({ char: icons2[frame()], colorIdx: 2, opacity: 1 })
      } else {
        result.push({ char: " ", colorIdx: 0, opacity: 0 })
      }
    }
    return result
  }

  return (
    <>
      {chars().map((c) => {
        const color = RGBA.fromInts(
          palette[c.colorIdx].r * 255,
          palette[c.colorIdx].g * 255,
          palette[c.colorIdx].b * 255,
          c.opacity * 255,
        )
        return <span style={{ fg: color }}>{c.char}</span>
      })}
    </>
  )
}

/**
 * AskUserPingPong - Question mark bouncing between two chat bubbles
 */
export function AskUserPingPong(props: { width?: number }) {
  const width = props.width ?? 5
  const [ballPos, setBallPos] = createSignal(1)
  const [direction, setDirection] = createSignal(1)
  const palette = ASKUSER_COLORS.palettes[0]

  const interval = setInterval(() => {
    setBallPos((p) => {
      const next = p + direction()
      if (next >= width - 1) {
        setDirection(-1)
        return width - 2
      }
      if (next <= 0) {
        setDirection(1)
        return 1
      }
      return next
    })
  }, 120)

  onCleanup(() => clearInterval(interval))

  const chars = () => {
    const result: Array<{ char: string; colorIdx: number; opacity: number }> = []
    const ball = ballPos()
    for (let i = 0; i < width; i++) {
      if (i === 0) {
        result.push({ char: MSG_QUESTION, colorIdx: 0, opacity: ball === 1 ? 1 : 0.5 })
      } else if (i === width - 1) {
        result.push({ char: CHAT_QUESTION, colorIdx: 2, opacity: ball === width - 2 ? 1 : 0.5 })
      } else if (i === ball) {
        result.push({ char: FA_QUESTION, colorIdx: 1, opacity: 1 })
      } else {
        result.push({ char: "·", colorIdx: 1, opacity: 0.2 })
      }
    }
    return result
  }

  return (
    <>
      {chars().map((c) => {
        const color = RGBA.fromInts(
          palette[c.colorIdx].r * 255,
          palette[c.colorIdx].g * 255,
          palette[c.colorIdx].b * 255,
          c.opacity * 255,
        )
        return <span style={{ fg: color }}>{c.char}</span>
      })}
    </>
  )
}

/**
 * AskUserRipple - Bubbles expanding outward from center like ripples
 */
export function AskUserRipple(props: { width?: number }) {
  const width = props.width ?? 5
  const center = Math.floor(width / 2)
  const [phase, setPhase] = createSignal(0)
  const palette = ASKUSER_COLORS.palettes[0]
  const icons = [FA_QUESTION, MSG_QUESTION, CHAT_QUESTION, COMMENT_QUESTION, THOUGHT_BUBBLE]

  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % (center + 2))
  }, 180)

  onCleanup(() => clearInterval(interval))

  const chars = () => {
    const result: Array<{ char: string; colorIdx: number; opacity: number }> = []
    const p = phase()
    for (let i = 0; i < width; i++) {
      const dist = Math.abs(i - center)
      if (dist === p && p <= center) {
        result.push({ char: icons[dist % icons.length], colorIdx: dist % 3, opacity: 1 - dist * 0.15 })
      } else if (dist === p - 1 && p > 0) {
        result.push({ char: icons[dist % icons.length], colorIdx: dist % 3, opacity: 0.4 })
      } else if (i === center && p === 0) {
        result.push({ char: HELP_CIRCLE, colorIdx: 0, opacity: 1 })
      } else {
        result.push({ char: " ", colorIdx: 0, opacity: 0 })
      }
    }
    return result
  }

  return (
    <>
      {chars().map((c) => {
        const color = RGBA.fromInts(
          palette[c.colorIdx].r * 255,
          palette[c.colorIdx].g * 255,
          palette[c.colorIdx].b * 255,
          c.opacity * 255,
        )
        return <span style={{ fg: color }}>{c.char}</span>
      })}
    </>
  )
}

/**
 * AskUserTypewriter - Bubbles appearing one by one like typing
 */
export function AskUserTypewriter(props: { width?: number }) {
  const width = props.width ?? 4
  const [count, setCount] = createSignal(0)
  const [blink, setBlink] = createSignal(true)
  const palette = ASKUSER_COLORS.palettes[0]
  const icons = [MSG_QUESTION, CHAT_QUESTION, COMMENT_QUESTION, THOUGHT_BUBBLE]

  const interval = setInterval(() => {
    const now = Date.now()
    setCount(Math.floor(now / 400) % (width + 2))
    setBlink(Math.floor(now / 300) % 2 === 0)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const chars = () => {
    const result: Array<{ char: string; colorIdx: number; opacity: number }> = []
    const c = count()
    for (let i = 0; i < width; i++) {
      if (i < c && i < width) {
        result.push({ char: icons[i % icons.length], colorIdx: i % 3, opacity: 1 })
      } else if (i === c && c < width) {
        result.push({ char: "▌", colorIdx: 0, opacity: blink() ? 1 : 0.3 })
      } else {
        result.push({ char: " ", colorIdx: 0, opacity: 0 })
      }
    }
    return result
  }

  return (
    <>
      {chars().map((c) => {
        const color = RGBA.fromInts(
          palette[c.colorIdx].r * 255,
          palette[c.colorIdx].g * 255,
          palette[c.colorIdx].b * 255,
          c.opacity * 255,
        )
        return <span style={{ fg: color }}>{c.char}</span>
      })}
    </>
  )
}

/**
 * AskUserPulse - Pulsing message icons with expanding effect
 */
export function AskUserPulse() {
  const icons = [MSG_QUESTION, FA_QUESTION, CHAT_QUESTION, FA_QUESTION]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.5)
  const palette = ASKUSER_COLORS.palettes[0]

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 600) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + ((Math.sin(phase) + 1) / 2) * 0.5)
    setFrame(Math.floor(now / 400) % icons.length)
  }, 40)

  onCleanup(() => clearInterval(interval))

  const color = () => {
    const base = palette[frame() % palette.length]
    return RGBA.fromInts(base.r * 255, base.g * 255, base.b * 255, opacity() * 255)
  }

  return <span style={{ fg: color() }}>{icons[frame()]}</span>
}

/**
 * AskUserWave - Three icons doing wave animation
 */
export function AskUserWave() {
  const icons = [MSG_QUESTION, CHAT_QUESTION, COMMENT_QUESTION]
  const palette = ASKUSER_COLORS.palettes[0]
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = icons.map((_, i) => {
      const phase = ((now / 800) * Math.PI * 2 + i * 1.2) % (Math.PI * 2)
      return 0.3 + ((Math.sin(phase) + 1) / 2) * 0.7
    })
    setOpacities(newOpacities)
  }, 40)

  onCleanup(() => clearInterval(interval))

  const c0 = () => RGBA.fromInts(palette[0].r * 255, palette[0].g * 255, palette[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(palette[1].r * 255, palette[1].g * 255, palette[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(palette[2].r * 255, palette[2].g * 255, palette[2].b * 255, opacities()[2] * 255)

  return (
    <>
      <span style={{ fg: c0() }}>{icons[0]}</span>
      <span style={{ fg: c1() }}>{icons[1]}</span>
      <span style={{ fg: c2() }}>{icons[2]}</span>
    </>
  )
}

/**
 * AskUserBubbles - Morphing between different chat bubble styles
 */
export function AskUserBubbles() {
  const sequence = [MSG, MSG_QUESTION, CHAT, CHAT_QUESTION, MSG_PROCESSING, CHAT_PROCESSING, FORUM, THOUGHT_BUBBLE]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(1)
  const palette = ASKUSER_COLORS.palettes[0]

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = (now / 350) % sequence.length
    const currentFrame = Math.floor(phase)
    const transition = phase - currentFrame
    // Quick fade between frames
    const fadeOpacity =
      transition < 0.15 ? 0.6 + transition * 2.67 : transition > 0.85 ? 1 - (transition - 0.85) * 2.67 : 1
    setFrame(currentFrame)
    setOpacity(fadeOpacity)
  }, 30)

  onCleanup(() => clearInterval(interval))

  const color = () => {
    const base = palette[frame() % palette.length]
    return RGBA.fromInts(base.r * 255, base.g * 255, base.b * 255, opacity() * 255)
  }

  return <span style={{ fg: color() }}>{sequence[frame()]}</span>
}

/**
 * AskUserSpinner - Three chat icons breathing in wave pattern with morphing
 */
export function AskUserSpinner(props: { paletteIndex?: number }) {
  const palette = ASKUSER_COLORS.palettes[props.paletteIndex ?? 0]
  const icons = [MSG_QUESTION, CHAT_QUESTION, COMMENT_QUESTION]
  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])
  const [iconFrame, setIconFrame] = createSignal(0)

  const interval = setInterval(() => {
    const now = Date.now()
    const newOpacities = icons.map((_, i) => {
      const phase = ((now / 1000) * Math.PI * 2 + i * 0.8) % (Math.PI * 2)
      return 0.4 + ((Math.sin(phase) + 1) / 2) * 0.6
    })
    setOpacities(newOpacities)
    setIconFrame(Math.floor(now / 800) % 3)
  }, 40)

  onCleanup(() => clearInterval(interval))

  const getIcon = (i: number) => icons[(iconFrame() + i) % icons.length]

  const c0 = () => RGBA.fromInts(palette[0].r * 255, palette[0].g * 255, palette[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(palette[1].r * 255, palette[1].g * 255, palette[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(palette[2].r * 255, palette[2].g * 255, palette[2].b * 255, opacities()[2] * 255)

  return (
    <>
      <span style={{ fg: c0() }}>{getIcon(0)}</span>
      <span style={{ fg: c1() }}>{getIcon(1)}</span>
      <span style={{ fg: c2() }}>{getIcon(2)}</span>
    </>
  )
}

/**
 * AskUserBounce - Bouncing question mark between chat bubbles
 */
export function AskUserBounce(props: { color?: RGBA }) {
  const baseColor = props.color ?? ASKUSER_COLORS.waiting
  const frames = [FA_QUESTION, MSG_QUESTION, FA_QUESTION, CHAT_QUESTION, FA_QUESTION, COMMENT_QUESTION]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.6)

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 600) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + Math.abs(Math.sin(phase)) * 0.5)
    setFrame(Math.floor(now / 300) % frames.length)
  }, 40)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{frames[frame()]}</span>
}

/**
 * AskUserPie - Animated pie chart showing progress through 8 slice frames
 * Color transitions from yellow (waiting) to green (progress)
 */
export function AskUserPie(props: { progress?: number }) {
  const [frame, setFrame] = createSignal(0)
  const [hue, setHue] = createSignal(0)

  // Frame animation - cycles through pie slices
  const interval = setInterval(() => {
    setFrame((f) => (f + 1) % CIRCLE_SLICES.length)
    setHue((h) => (h + 2) % 360)
  }, 150)

  onCleanup(() => clearInterval(interval))

  // Interpolate between yellow (waiting) and teal (progress)
  const color = () => {
    const progress = frame() / (CIRCLE_SLICES.length - 1)
    const yellow = ASKUSER_COLORS.waiting
    const teal = ASKUSER_COLORS.palettes[2][0]
    return RGBA.fromInts(
      Math.round(yellow.r * 255 * (1 - progress) + teal.r * 255 * progress),
      Math.round(yellow.g * 255 * (1 - progress) + teal.g * 255 * progress),
      Math.round(yellow.b * 255 * (1 - progress) + teal.b * 255 * progress),
      255,
    )
  }

  return <span style={{ fg: color() }}>{CIRCLE_SLICES[frame()]}</span>
}

/**
 * AskUserProgressDots - Shows answered vs total questions with filled/empty circles
 * Answered questions show as filled green, pending show as muted with wave animation
 */
export function AskUserProgressDots(props: { answered: number; total: number }) {
  const { theme } = useTheme()
  const [phase, setPhase] = createSignal(0)

  // Wave animation for pending dots
  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % 100)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const getEmptyOpacity = (index: number) => {
    const wave = Math.sin((phase() / 100) * Math.PI * 2 + index * 0.5)
    return 0.3 + (wave + 1) * 0.2 // Range: 0.3 - 0.7
  }

  // Build the dots array
  const dots = () => {
    const result: Array<{ filled: boolean; index: number }> = []
    for (let i = 0; i < props.total; i++) {
      result.push({ filled: i < props.answered, index: i })
    }
    return result
  }

  const filledColor = ASKUSER_COLORS.success
  const emptyColor = (index: number) =>
    RGBA.fromInts(
      theme.textMuted.r * 255,
      theme.textMuted.g * 255,
      theme.textMuted.b * 255,
      getEmptyOpacity(index) * 255,
    )

  return (
    <>
      {dots().map((dot) =>
        dot.filled ? (
          <span style={{ fg: filledColor }}>{CIRCLE_FILLED}</span>
        ) : (
          <span style={{ fg: emptyColor(dot.index) }}>{CIRCLE_EMPTY}</span>
        ),
      )}
    </>
  )
}

// Pastel lavender color for ask-user tool
const ASKING_PASTEL = RGBA.fromHex("#b4befe") // Catppuccin Lavender - soft, calming

/**
 * MorphingText - Text with breathing opacity animation
 * Used for "Asking" text that breathes alongside the icon
 */
export function MorphingText(props: { text: string; color?: RGBA }) {
  const baseColor = props.color ?? ASKING_PASTEL
  const [opacity, setOpacity] = createSignal(0.7)

  // Gentle breathing - syncs with AskUserMorph's 2s cycle
  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 2000) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{props.text}</span>
}

// Backwards compatibility alias
export function BreathingAskUser(props: { color?: RGBA }) {
  return <MorphingText text="Asking" color={props.color} />
}

/**
 * AskUserPending - Single morph icon + morphing "Asking" text
 * Clean, minimal - just the animated icon and text
 */
export function AskUserPending() {
  return (
    <box gap={0}>
      <text>
        <AskUserMorph color={ASKING_PASTEL} /> <MorphingText text="Asking" color={ASKING_PASTEL} />
      </text>
    </box>
  )
}

/**
 * AskUserRunning - Single morph icon + morphing "Asking" text + elapsed time
 * No title shown during running - only at the end
 */
export function AskUserRunning(props: { startTime?: number }) {
  const { theme } = useTheme()

  return (
    <box gap={0}>
      <text>
        <AskUserMorph color={ASKING_PASTEL} /> <MorphingText text="Asking" color={ASKING_PASTEL} />
        <Show when={props.startTime}>
          {" "}
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * AskUserResolved - Completed or error state with checkmark/X and metadata
 * Shows: ✓/✗ Asking title • answered/total • time
 */
export function AskUserResolved(props: {
  success: boolean
  title?: string
  executionTime?: number
  questionCount?: number
  answeredCount?: number
}) {
  const { theme } = useTheme()
  const icon = props.success ? "✓" : "✗"
  const iconColor = props.success ? ASKUSER_COLORS.success : ASKUSER_COLORS.error
  // Dimmed pastel for resolved state
  const dimmedPastel = RGBA.fromInts(ASKING_PASTEL.r * 255, ASKING_PASTEL.g * 255, ASKING_PASTEL.b * 255, 153)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: iconColor, bold: true }}>{icon}</span> <span style={{ fg: dimmedPastel }}>Asking</span>{" "}
        <span style={{ fg: dimmedPastel }}>{props.title}</span>
        <Show when={props.success && props.questionCount !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span>{" "}
          <span style={{ fg: ASKUSER_COLORS.success }}>
            {props.answeredCount ?? 0}/{props.questionCount}
          </span>
        </Show>
        <Show when={!props.success}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span> <span style={{ fg: ASKUSER_COLORS.error }}>cancelled</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * AskUserToolAnimation - Complete state machine for ask-user tool
 */
export function AskUserToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  title?: string
  startTime?: number
  executionTime?: number
  questionCount?: number
  answeredCount?: number
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <AskUserPending />
      </Match>
      <Match when={props.status === "running"}>
        <AskUserRunning startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <AskUserResolved
          success={true}
          title={props.title}
          executionTime={props.executionTime}
          questionCount={props.questionCount}
          answeredCount={props.answeredCount}
        />
      </Match>
      <Match when={props.status === "error"}>
        <AskUserResolved success={false} title={props.title} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

// ============================================================================
// ITERATION 1: AskUserMorph - Single icon morphing slowly through shapes
// Ultra minimal - one icon transitions smoothly like a slow breathing creature
// ============================================================================

/**
 * AskUserMorph - Single icon that slowly morphs through chat bubble shapes
 * Think: a slow breathing jellyfish of communication
 * ~3 second full cycle through all shapes
 */
export function AskUserMorph(props: { color?: RGBA }) {
  const baseColor = props.color ?? ASKUSER_COLORS.palettes[0][0]
  const shapes = [MSG, MSG_QUESTION, CHAT, CHAT_QUESTION, MSG_PROCESSING, THOUGHT_BUBBLE]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.7)

  // Slow morph - 500ms per shape = 3s full cycle
  const morphInterval = setInterval(() => {
    setFrame((f) => (f + 1) % shapes.length)
  }, 500)

  // Gentle breathing opacity
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 2000) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{shapes[frame()]}</span>
}

// ============================================================================
// ITERATION 2: AskUserQuestions - THREE BOUNCING QUESTION MARKS
// All ? icons - they're all similar in shape so morphing looks smooth!
// Wave breathing + question mark morphing = SICK
// ============================================================================

/**
 * AskUserQuestions - Three question marks bouncing with wave breathing
 * Each ? morphs through different question icon styles
 * All shapes are ? variants so transitions are SMOOTH
 * Like SwarmSpinner but with QUESTIONS
 */
export function AskUserQuestions(props: { state?: "idle" | "active" | "waiting" }) {
  const state = props.state ?? "active"

  // Purple palette - curiosity, questioning, dialogue
  const colors = [
    RGBA.fromHex("#cba6f7"), // Mauve
    RGBA.fromHex("#b4befe"), // Lavender
    RGBA.fromHex("#89b4fa"), // Blue
  ]

  // ALL the question marks - similar shapes for smooth morphing!
  const questions = [
    FA_QUESTION, //  simple ?
    HELP_CIRCLE, // 󰋗 ? in circle
    HELP_RHOMBUS, // 󰮥 ? in diamond
    COD_QUESTION, //  codicon ?
    CIRCLE_QUESTION, //  fa circle ?
  ]

  const [opacities, setOpacities] = createSignal([0.5, 0.5, 0.5])
  const [shapeIdx, setShapeIdx] = createSignal(0)

  // Timing based on state
  const duration = state === "idle" ? 3000 : state === "active" ? 1800 : 2400
  const minOpacity = state === "idle" ? 0.3 : state === "active" ? 0.4 : 0.35
  const morphSpeed = state === "idle" ? 700 : state === "active" ? 450 : 550

  // Wave breathing - staggered EXACTLY like SwarmSpinner
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const newOpacities = colors.map((_, i) => {
      const phase = ((now / duration) * Math.PI * 2 + i * 0.7) % (Math.PI * 2)
      return minOpacity + (1 - minOpacity) * ((Math.sin(phase) + 1) / 2)
    })
    setOpacities(newOpacities)
  }, 50)

  // Morph through question marks
  const morphInterval = setInterval(() => {
    setShapeIdx((idx) => (idx + 1) % questions.length)
  }, morphSpeed)

  onCleanup(() => {
    clearInterval(breathInterval)
    clearInterval(morphInterval)
  })

  // Each icon offset by 1 for staggered morphing
  const getQuestion = (offset: number) => questions[(shapeIdx() + offset) % questions.length]

  const c0 = () => RGBA.fromInts(colors[0].r * 255, colors[0].g * 255, colors[0].b * 255, opacities()[0] * 255)
  const c1 = () => RGBA.fromInts(colors[1].r * 255, colors[1].g * 255, colors[1].b * 255, opacities()[1] * 255)
  const c2 = () => RGBA.fromInts(colors[2].r * 255, colors[2].g * 255, colors[2].b * 255, opacities()[2] * 255)

  return (
    <>
      <span style={{ fg: c0() }}>{getQuestion(0)}</span>
      <span style={{ fg: c1() }}>{getQuestion(1)}</span>
      <span style={{ fg: c2() }}>{getQuestion(2)}</span>
    </>
  )
}

// Aliases
export const AskUserMorph3 = AskUserQuestions
export const AskUserMorphGlide = AskUserQuestions
export const AskUserGlide = AskUserQuestions

/**
 * AskUserShowcase - Demo showing the animations
 */
export function AskUserShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={1}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Ask-User Animations:
      </text>

      <box flexDirection="row" gap={4}>
        <box flexDirection="column" alignItems="center">
          <text>
            <AskUserMorph />
          </text>
          <text style={{ fg: theme.textMuted }}>morph x1</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <AskUserQuestions />
          </text>
          <text style={{ fg: theme.textMuted }}>???</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <AskUserQuestions state="idle" />
          </text>
          <text style={{ fg: theme.textMuted }}>idle</text>
        </box>

        <box flexDirection="column" alignItems="center">
          <text>
            <AskUserQuestions state="waiting" />
          </text>
          <text style={{ fg: theme.textMuted }}>waiting</text>
        </box>
      </box>
    </box>
  )
}

// ============================================================================
// READ TOOL ANIMATION
// Single icon morphing through file/eye/scan states with breathing opacity
// Color: Teal #94e2d5 (Catppuccin Teal) - calm, focused, "reading" vibe
// ============================================================================

// Read-related icons
const FILE_DOC = "󰈙" // md-file_document
const FILE_EYE = "󰷊" // md-file_eye
const EYE = "󰈈" // md-eye
const EYE_OUTLINE = "󰛐" // md-eye_outline
const LINE_SCAN = "󰘤" // md-line_scan
const MAGNIFY_SCAN = "󱉶" // md-magnify_scan

// Teal color palette for read tool
const READ_TEAL = RGBA.fromHex("#94e2d5") // Catppuccin Teal

// Read success/error colors
const READ_COLORS = {
  success: RGBA.fromHex("#a6e3a1"), // Green
  error: RGBA.fromHex("#f38ba8"), // Red/pink
  // Elegant file path colors
  directory: RGBA.fromHex("#89b4fa"), // Blue for directories
  filename: RGBA.fromHex("#cdd6f4"), // Light text for filename
}

// File extension colors by language/type
const EXT_COLORS: Record<string, string> = {
  // JavaScript/TypeScript
  ".js": "#f7df1e", // JS Yellow
  ".jsx": "#61dafb", // React Cyan
  ".ts": "#3178c6", // TS Blue
  ".tsx": "#61dafb", // React Cyan
  ".mjs": "#f7df1e", // JS Yellow
  ".cjs": "#f7df1e", // JS Yellow
  // Web
  ".html": "#e34c26", // HTML Orange
  ".css": "#264de4", // CSS Blue
  ".scss": "#cc6699", // Sass Pink
  ".sass": "#cc6699", // Sass Pink
  ".less": "#1d365d", // Less Blue
  ".vue": "#4fc08d", // Vue Green
  ".svelte": "#ff3e00", // Svelte Orange
  // Data
  ".json": "#cbcb41", // JSON Yellow
  ".yaml": "#cb171e", // YAML Red
  ".yml": "#cb171e", // YAML Red
  ".xml": "#e37933", // XML Orange
  ".toml": "#9c4121", // TOML Brown
  // Backend
  ".py": "#3572a5", // Python Blue
  ".rb": "#cc342d", // Ruby Red
  ".go": "#00add8", // Go Cyan
  ".rs": "#dea584", // Rust Orange
  ".java": "#b07219", // Java Brown
  ".kt": "#a97bff", // Kotlin Purple
  ".swift": "#f05138", // Swift Orange
  ".php": "#777bb4", // PHP Purple
  ".c": "#555555", // C Grey
  ".cpp": "#f34b7d", // C++ Pink
  ".h": "#555555", // Header Grey
  // Shell/Config
  ".sh": "#89e051", // Shell Green
  ".bash": "#89e051", // Bash Green
  ".zsh": "#89e051", // Zsh Green
  ".fish": "#89e051", // Fish Green
  ".env": "#ecd53f", // Env Yellow
  // Docs
  ".md": "#083fa1", // Markdown Blue
  ".mdx": "#fcb32c", // MDX Orange
  ".txt": "#89898980", // Text Muted
  // Other
  ".sql": "#e38c00", // SQL Orange
  ".graphql": "#e10098", // GraphQL Pink
  ".prisma": "#2d3748", // Prisma Dark
  ".docker": "#2496ed", // Docker Blue
  ".dockerfile": "#2496ed", // Docker Blue
}

// Get color for file extension
function getExtColor(ext: string, dimmed: boolean): RGBA {
  const hex = EXT_COLORS[ext.toLowerCase()] || "#a6adc8" // Default muted
  const base = RGBA.fromHex(hex)
  const opacity = dimmed ? 0.7 : 1.0
  return RGBA.fromInts(base.r * 255, base.g * 255, base.b * 255, opacity * 255)
}

/**
 * StyledFilePath - Renders filepath with elegant syntax coloring
 * Directory parts in blue, filename in light text, extension colored by type
 */
export function StyledFilePath(props: { path: string; dimmed?: boolean }) {
  const parts = props.path.split("/")
  const filename = parts.pop() || ""
  const directory = parts.length > 0 ? parts.join("/") + "/" : ""

  // Split filename into name and extension
  const lastDot = filename.lastIndexOf(".")
  const name = lastDot > 0 ? filename.slice(0, lastDot) : filename
  const ext = lastDot > 0 ? filename.slice(lastDot) : ""

  // Apply dimming if needed (for resolved state)
  const opacity = props.dimmed ? 0.7 : 1.0
  const dirColor = RGBA.fromInts(
    READ_COLORS.directory.r * 255,
    READ_COLORS.directory.g * 255,
    READ_COLORS.directory.b * 255,
    opacity * 255,
  )
  const nameColor = RGBA.fromInts(
    READ_COLORS.filename.r * 255,
    READ_COLORS.filename.g * 255,
    READ_COLORS.filename.b * 255,
    opacity * 255,
  )
  const extColor = getExtColor(ext, props.dimmed ?? false)

  return (
    <>
      <Show when={directory}>
        <span style={{ fg: dirColor }}>{directory}</span>
      </Show>
      <span style={{ fg: nameColor }}>{name}</span>
      <Show when={ext}>
        <span style={{ fg: extColor }}>{ext}</span>
      </Show>
    </>
  )
}

/**
 * ReadMorph - Single icon that morphs through reading/scanning states
 * Sequence: file → file-eye → eye → scan → magnify-scan → repeat
 * Creates visual story of "opening file, scanning content"
 */
export function ReadMorph(props: { color?: RGBA }) {
  const baseColor = props.color ?? READ_TEAL
  // Morphing sequence tells the story: file → eye on file → eye → scanning → deep scan
  const shapes = [FILE_DOC, FILE_EYE, EYE, LINE_SCAN, MAGNIFY_SCAN, EYE_OUTLINE]
  const [frame, setFrame] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.7)

  // Morph through shapes - 400ms per shape for smooth reading feel
  const morphInterval = setInterval(() => {
    setFrame((f) => (f + 1) % shapes.length)
  }, 400)

  // Gentle breathing opacity synced to 2s cycle
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 2000) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{shapes[frame()]}</span>
}

/**
 * ReadText - Breathing "Read" text that syncs with ReadMorph
 */
export function ReadText(props: { color?: RGBA }) {
  const baseColor = props.color ?? READ_TEAL
  const [opacity, setOpacity] = createSignal(0.7)

  // Gentle breathing - syncs with ReadMorph's 2s cycle
  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 2000) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>Read</span>
}

/**
 * ReadPending - Single morph icon + breathing "Read" text
 * Clean, minimal - just the animated icon and text
 */
export function ReadPending() {
  return (
    <box gap={0}>
      <text>
        <ReadMorph color={READ_TEAL} /> <ReadText color={READ_TEAL} />
      </text>
    </box>
  )
}

/**
 * ReadRunning - Single morph icon + breathing "Read" text + filepath + elapsed time
 */
export function ReadRunning(props: { filePath?: string; startTime?: number }) {
  const { theme } = useTheme()

  return (
    <box gap={0}>
      <text>
        <ReadMorph color={READ_TEAL} /> <ReadText color={READ_TEAL} />
        <Show when={props.filePath}>
          {" "}
          <StyledFilePath path={props.filePath!} />
        </Show>
        <Show when={props.startTime}>
          {" "}
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * ReadResolved - Completed or error state with checkmark/X and metadata
 * Shows: ✓/✗ Read filepath • lines • time
 */
export function ReadResolved(props: {
  success: boolean
  filePath?: string
  executionTime?: number
  lineCount?: number
}) {
  const { theme } = useTheme()
  const icon = props.success ? "✓" : "✗"
  const iconColor = props.success ? READ_COLORS.success : READ_COLORS.error
  // Dimmed teal for resolved state
  const dimmedTeal = RGBA.fromInts(READ_TEAL.r * 255, READ_TEAL.g * 255, READ_TEAL.b * 255, 153)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: iconColor, bold: true }}>{icon}</span> <span style={{ fg: dimmedTeal }}>Read</span>
        <Show when={props.filePath}>
          {" "}
          <StyledFilePath path={props.filePath!} dimmed={true} />
        </Show>
        <Show when={props.lineCount !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span>{" "}
          <span style={{ fg: theme.textMuted }}>{props.lineCount} lines</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * ReadToolAnimation - Complete state machine for read tool
 */
export function ReadToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  filePath?: string
  startTime?: number
  executionTime?: number
  lineCount?: number
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <ReadPending />
      </Match>
      <Match when={props.status === "running"}>
        <ReadRunning filePath={props.filePath} startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <ReadResolved
          success={true}
          filePath={props.filePath}
          executionTime={props.executionTime}
          lineCount={props.lineCount}
        />
      </Match>
      <Match when={props.status === "error"}>
        <ReadResolved success={false} filePath={props.filePath} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

// ============================================================================
// GLOB TOOL ANIMATION - Purple theme with star pattern morphing
// Pattern: asterisk → starburst → sparkle with breathing
// ============================================================================

// Glob color - Catppuccin Mauve (purple)
const GLOB_PURPLE = RGBA.fromHex("#cba6f7")

const GLOB_COLORS = {
  primary: GLOB_PURPLE,
  success: RGBA.fromHex("#a6e3a1"),
  error: RGBA.fromHex("#f38ba8"),
  dim: RGBA.fromHex("#7c6f9f"), // Dimmed purple
}

// Glob icons - star/asterisk morphing sequence
const GLOB_ICONS = ["✱", "✳", "✴", "✵", "✶", "✷"]

/**
 * GlobMorph - Single icon morphing through star patterns with breathing
 */
export function GlobMorph(props: { color?: RGBA }) {
  const baseColor = props.color ?? GLOB_PURPLE
  const [iconIndex, setIconIndex] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.6)

  // Icon morphing - cycle through star patterns
  const morphInterval = setInterval(() => {
    setIconIndex((i) => (i + 1) % GLOB_ICONS.length)
  }, 300)

  // Breathing opacity
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1800) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{GLOB_ICONS[iconIndex()]}</span>
}

/**
 * GlobText - Breathing "Glob" text synchronized with icon
 */
export function GlobText(props: { color?: RGBA }) {
  const baseColor = props.color ?? GLOB_PURPLE
  const [opacity, setOpacity] = createSignal(0.6)

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1800) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>Glob</span>
}

/**
 * GlobPending - Initial state showing just spinner + breathing text
 */
export function GlobPending() {
  return (
    <box gap={0}>
      <text>
        <GlobMorph color={GLOB_PURPLE} /> <GlobText color={GLOB_PURPLE} />{" "}
        <span style={{ fg: GLOB_COLORS.dim }}>...</span>
      </text>
    </box>
  )
}

/**
 * GlobRunning - Active search state with pattern and path
 */
export function GlobRunning(props: { pattern?: string; path?: string; startTime?: number }) {
  const { theme } = useTheme()

  return (
    <box gap={0}>
      <text>
        <GlobMorph color={GLOB_PURPLE} /> <GlobText color={GLOB_PURPLE} />
        <Show when={props.pattern}>
          {" "}
          <span style={{ fg: GLOB_PURPLE }}>"{props.pattern}"</span>
        </Show>
        <Show when={props.path}>
          {" "}
          <span style={{ fg: theme.textMuted }}>in</span> <StyledFilePath path={props.path!} />
        </Show>
        <Show when={props.startTime}>
          {" "}
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * GlobResolved - Completed state with match count
 */
export function GlobResolved(props: {
  success: boolean
  pattern?: string
  path?: string
  matchCount?: number
  executionTime?: number
}) {
  const { theme } = useTheme()
  const icon = props.success ? "✓" : "✗"
  const iconColor = props.success ? GLOB_COLORS.success : GLOB_COLORS.error
  const dimmedPurple = RGBA.fromInts(GLOB_PURPLE.r * 255, GLOB_PURPLE.g * 255, GLOB_PURPLE.b * 255, 153)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: iconColor, bold: true }}>{icon}</span> <span style={{ fg: dimmedPurple }}>Glob</span>
        <Show when={props.pattern}>
          {" "}
          <span style={{ fg: dimmedPurple }}>"{props.pattern}"</span>
        </Show>
        <Show when={props.path}>
          {" "}
          <span style={{ fg: theme.textMuted }}>in</span> <StyledFilePath path={props.path!} dimmed={true} />
        </Show>
        <Show when={props.matchCount !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span>{" "}
          <span style={{ fg: props.success ? theme.primary : theme.textMuted }}>{props.matchCount} matches</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * GlobToolAnimation - Complete state machine for glob tool
 */
export function GlobToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  pattern?: string
  path?: string
  startTime?: number
  executionTime?: number
  matchCount?: number
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <GlobPending />
      </Match>
      <Match when={props.status === "running"}>
        <GlobRunning pattern={props.pattern} path={props.path} startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <GlobResolved
          success={true}
          pattern={props.pattern}
          path={props.path}
          matchCount={props.matchCount}
          executionTime={props.executionTime}
        />
      </Match>
      <Match when={props.status === "error"}>
        <GlobResolved success={false} pattern={props.pattern} path={props.path} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

// ============================================================================
// GREP TOOL ANIMATION - Pink theme with search/magnify morphing
// Pattern: magnify glass → eye → scan patterns
// ============================================================================

// Grep color - Catppuccin Pink
const GREP_PINK = RGBA.fromHex("#f38ba8")

const GREP_COLORS = {
  primary: GREP_PINK,
  success: RGBA.fromHex("#a6e3a1"),
  error: RGBA.fromHex("#f38ba8"),
  dim: RGBA.fromHex("#a66d7a"), // Dimmed pink
}

// Grep icons - search/magnify morphing sequence
const GREP_ICONS = ["󰍉", "󰈞", "󱎸", "󰺮", "󰱼", "󰍉"]

/**
 * GrepMorph - Single icon morphing through search patterns with breathing
 */
export function GrepMorph(props: { color?: RGBA }) {
  const baseColor = props.color ?? GREP_PINK
  const [iconIndex, setIconIndex] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.6)

  // Icon morphing
  const morphInterval = setInterval(() => {
    setIconIndex((i) => (i + 1) % GREP_ICONS.length)
  }, 280)

  // Breathing opacity
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1600) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{GREP_ICONS[iconIndex()]}</span>
}

/**
 * GrepText - Breathing "Grep" text synchronized with icon
 */
export function GrepText(props: { color?: RGBA }) {
  const baseColor = props.color ?? GREP_PINK
  const [opacity, setOpacity] = createSignal(0.6)

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1600) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>Grep</span>
}

/**
 * GrepPending - Initial state
 */
export function GrepPending() {
  return (
    <box gap={0}>
      <text>
        <GrepMorph color={GREP_PINK} /> <GrepText color={GREP_PINK} /> <span style={{ fg: GREP_COLORS.dim }}>...</span>
      </text>
    </box>
  )
}

/**
 * GrepRunning - Active search state with pattern and path
 */
export function GrepRunning(props: { pattern?: string; path?: string; include?: string; startTime?: number }) {
  const { theme } = useTheme()

  return (
    <box gap={0}>
      <text>
        <GrepMorph color={GREP_PINK} /> <GrepText color={GREP_PINK} />
        <Show when={props.pattern}>
          {" "}
          <span style={{ fg: theme.text }}>"{props.pattern}"</span>
        </Show>
        <Show when={props.path}>
          {" "}
          <span style={{ fg: theme.textMuted }}>in</span> <StyledFilePath path={props.path!} />
        </Show>
        <Show when={props.include}>
          {" "}
          <span style={{ fg: GREP_PINK }}>({props.include})</span>
        </Show>
        <Show when={props.startTime}>
          {" "}
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * GrepResolved - Completed state with match count
 */
export function GrepResolved(props: {
  success: boolean
  pattern?: string
  path?: string
  matchCount?: number
  executionTime?: number
}) {
  const { theme } = useTheme()
  const icon = props.success ? "✓" : "✗"
  const iconColor = props.success ? GREP_COLORS.success : GREP_COLORS.error
  const dimmedPink = RGBA.fromInts(GREP_PINK.r * 255, GREP_PINK.g * 255, GREP_PINK.b * 255, 153)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: iconColor, bold: true }}>{icon}</span> <span style={{ fg: dimmedPink }}>Grep</span>
        <Show when={props.pattern}>
          {" "}
          <span style={{ fg: theme.textMuted }}>"{props.pattern}"</span>
        </Show>
        <Show when={props.path}>
          {" "}
          <span style={{ fg: theme.textMuted }}>in</span> <StyledFilePath path={props.path!} dimmed={true} />
        </Show>
        <Show when={props.matchCount !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span>{" "}
          <span style={{ fg: props.success ? theme.primary : theme.textMuted }}>{props.matchCount} matches</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * GrepToolAnimation - Complete state machine for grep tool
 */
export function GrepToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  pattern?: string
  path?: string
  include?: string
  startTime?: number
  executionTime?: number
  matchCount?: number
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <GrepPending />
      </Match>
      <Match when={props.status === "running"}>
        <GrepRunning pattern={props.pattern} path={props.path} include={props.include} startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <GrepResolved
          success={true}
          pattern={props.pattern}
          path={props.path}
          matchCount={props.matchCount}
          executionTime={props.executionTime}
        />
      </Match>
      <Match when={props.status === "error"}>
        <GrepResolved success={false} pattern={props.pattern} path={props.path} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

// ============================================================================
// LIST TOOL ANIMATION - Cyan theme with folder/directory morphing
// Pattern: folder → tree → list patterns
// ============================================================================

// List color - Catppuccin Sky (cyan)
const LIST_CYAN = RGBA.fromHex("#89dceb")

const LIST_COLORS = {
  primary: LIST_CYAN,
  success: RGBA.fromHex("#a6e3a1"),
  error: RGBA.fromHex("#f38ba8"),
  dim: RGBA.fromHex("#5fa3b3"), // Dimmed cyan
}

// List icons - folder/directory morphing sequence
const LIST_ICONS = ["󰉋", "󰉖", "󰷏", "󰉓", "󰉌", "󰉋"]

/**
 * ListMorph - Single icon morphing through folder patterns with breathing
 */
export function ListMorph(props: { color?: RGBA }) {
  const baseColor = props.color ?? LIST_CYAN
  const [iconIndex, setIconIndex] = createSignal(0)
  const [opacity, setOpacity] = createSignal(0.6)

  // Icon morphing
  const morphInterval = setInterval(() => {
    setIconIndex((i) => (i + 1) % LIST_ICONS.length)
  }, 320)

  // Breathing opacity
  const breathInterval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1700) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => {
    clearInterval(morphInterval)
    clearInterval(breathInterval)
  })

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>{LIST_ICONS[iconIndex()]}</span>
}

/**
 * ListText - Breathing "List" text synchronized with icon
 */
export function ListText(props: { color?: RGBA }) {
  const baseColor = props.color ?? LIST_CYAN
  const [opacity, setOpacity] = createSignal(0.6)

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1700) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.5 + (Math.sin(phase) + 1) * 0.25)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(baseColor.r * 255, baseColor.g * 255, baseColor.b * 255, opacity() * 255)

  return <span style={{ fg: color() }}>List</span>
}

/**
 * ListPending - Initial state
 */
export function ListPending() {
  return (
    <box gap={0}>
      <text>
        <ListMorph color={LIST_CYAN} /> <ListText color={LIST_CYAN} /> <span style={{ fg: LIST_COLORS.dim }}>...</span>
      </text>
    </box>
  )
}

/**
 * ListRunning - Active listing with path
 */
export function ListRunning(props: { path?: string; startTime?: number }) {
  const { theme } = useTheme()

  return (
    <box gap={0}>
      <text>
        <ListMorph color={LIST_CYAN} /> <ListText color={LIST_CYAN} />
        <Show when={props.path}>
          {" "}
          <StyledFilePath path={props.path!} />
        </Show>
        <Show when={props.startTime}>
          {" "}
          <ElapsedTime startTime={props.startTime!} color={theme.textMuted} />
        </Show>
      </text>
    </box>
  )
}

/**
 * ListResolved - Completed state with item count
 */
export function ListResolved(props: { success: boolean; path?: string; itemCount?: number; executionTime?: number }) {
  const { theme } = useTheme()
  const icon = props.success ? "✓" : "✗"
  const iconColor = props.success ? LIST_COLORS.success : LIST_COLORS.error
  const dimmedCyan = RGBA.fromInts(LIST_CYAN.r * 255, LIST_CYAN.g * 255, LIST_CYAN.b * 255, 153)

  const formatTime = (ms: number) => {
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    const minutes = Math.floor(ms / 60000)
    const seconds = Math.floor((ms % 60000) / 1000)
    return `${minutes}m ${seconds}s`
  }

  return (
    <box gap={0}>
      <text>
        <span style={{ fg: iconColor, bold: true }}>{icon}</span> <span style={{ fg: dimmedCyan }}>List</span>
        <Show when={props.path}>
          {" "}
          <StyledFilePath path={props.path!} dimmed={true} />
        </Show>
        <Show when={props.itemCount !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>•</span>{" "}
          <span style={{ fg: props.success ? theme.primary : theme.textMuted }}>{props.itemCount} items</span>
        </Show>
        <Show when={props.executionTime !== undefined}>
          {" "}
          <span style={{ fg: theme.textMuted }}>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
    </box>
  )
}

/**
 * ListToolAnimation - Complete state machine for list tool
 */
export function ListToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  path?: string
  startTime?: number
  executionTime?: number
  itemCount?: number
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <ListPending />
      </Match>
      <Match when={props.status === "running"}>
        <ListRunning path={props.path} startTime={props.startTime} />
      </Match>
      <Match when={props.status === "completed"}>
        <ListResolved
          success={true}
          path={props.path}
          itemCount={props.itemCount}
          executionTime={props.executionTime}
        />
      </Match>
      <Match when={props.status === "error"}>
        <ListResolved success={false} path={props.path} executionTime={props.executionTime} />
      </Match>
    </Switch>
  )
}

// ============================================================================
// TOOL ANIMATION SHOWCASE - Display all tool animations for demo/testing
// ============================================================================

/**
 * ToolAnimationShowcase - Shows all tool animations in their different states
 * Perfect for home.tsx or testing
 */
export function ToolAnimationShowcase() {
  const { theme } = useTheme()

  return (
    <box flexDirection="column" gap={2}>
      <text fg={theme.text} attributes={TextAttributes.BOLD}>
        Tool Animations Showcase
      </text>

      {/* Read Tool */}
      <box flexDirection="column" gap={0}>
        <text fg={theme.textMuted}>Read Tool:</text>
        <box paddingLeft={2} flexDirection="column" gap={0}>
          <ReadPending />
          <ReadRunning filePath="/src/components/Button.tsx" startTime={Date.now() - 1500} />
          <ReadResolved success={true} filePath="/src/components/Button.tsx" lineCount={42} executionTime={234} />
        </box>
      </box>

      {/* Glob Tool */}
      <box flexDirection="column" gap={0}>
        <text fg={theme.textMuted}>Glob Tool:</text>
        <box paddingLeft={2} flexDirection="column" gap={0}>
          <GlobPending />
          <GlobRunning pattern="**/*.tsx" path="/src" startTime={Date.now() - 800} />
          <GlobResolved success={true} pattern="**/*.tsx" path="/src" matchCount={47} executionTime={156} />
        </box>
      </box>

      {/* Grep Tool */}
      <box flexDirection="column" gap={0}>
        <text fg={theme.textMuted}>Grep Tool:</text>
        <box paddingLeft={2} flexDirection="column" gap={0}>
          <GrepPending />
          <GrepRunning pattern="function.*export" path="/src" startTime={Date.now() - 1200} />
          <GrepResolved success={true} pattern="function.*export" path="/src" matchCount={23} executionTime={312} />
        </box>
      </box>

      {/* List Tool */}
      <box flexDirection="column" gap={0}>
        <text fg={theme.textMuted}>List Tool:</text>
        <box paddingLeft={2} flexDirection="column" gap={0}>
          <ListPending />
          <ListRunning path="/src/components" startTime={Date.now() - 500} />
          <ListResolved success={true} path="/src/components" itemCount={18} executionTime={89} />
        </box>
      </box>

      {/* Bash Tool */}
      <box flexDirection="column" gap={0}>
        <text fg={theme.textMuted}>Bash Tool:</text>
        <box paddingLeft={2} flexDirection="column" gap={0}>
          <BashPending command="npm install" description="Installing dependencies" />
          <BashResolved success={true} command="npm install" executionTime={4523} />
        </box>
      </box>
    </box>
  )
}

// ============================================================================
// INLINE PERMISSION COMPONENT - Compact permission UI for simple requests
// ============================================================================

/**
 * Pulsing warning indicator for inline permissions
 * Draws attention without being too intrusive
 */
function InlinePermissionIndicator(props: { color?: RGBA }) {
  const { theme } = useTheme()
  const color = props.color ?? theme.warning
  const [opacity, setOpacity] = createSignal(0.6)

  const timeline = useTimeline({
    duration: 1200,
    loop: true,
  })

  const target = { opacity: 0.6, setOpacity }
  timeline!.add(target, {
    opacity: 1.0,
    duration: 600,
    ease: "inOutSine",
    alternate: true,
    loop: 2,
    onUpdate: () => target.setOpacity(target.opacity),
  })

  const c = () => RGBA.fromInts(color.r * 255, color.g * 255, color.b * 255, opacity() * 255)
  return <span style={{ fg: c() }}>⚠</span>
}

/**
 * InlinePermission - Compact permission request UI that fits inside a tool card
 * Used for simple permissions like external_directory access, webfetch, etc.
 *
 * Shows: ⚠ [title] [↵ once] [a always] [d deny] [i msg]
 */
export function InlinePermission(props: {
  permission: Permission.Info
  onRespond: (response: Permission.Response) => void
  focused?: boolean
}) {
  const { theme } = useTheme()
  const renderer = useRenderer()
  const dialog = useDialog()
  const [inputMode, setInputMode] = createSignal(false)

  let textarea: TextareaRenderable
  let previousFocus: any = null // Track what was focused before entering input mode

  // Always show the full command - no truncation
  const title = () => props.permission.title

  // Handle keyboard when focused
  useKeyboard((evt) => {
    if (!props.focused) return
    // Don't capture keys when a modal dialog is showing (e.g., permission dialog for edits)
    if (dialog.stack.length > 0) return

    // Input mode - let textarea handle typing, we handle Enter/Escape
    if (inputMode()) {
      if (evt.name === "return") {
        const msg = textarea?.plainText?.trim() || ""
        if (msg) {
          props.onRespond({ type: "reject", message: msg })
        } else {
          props.onRespond("reject")
        }
        setInputMode(false)
        // Restore focus to previous element
        previousFocus?.focus?.()
        previousFocus = null
        evt.preventDefault()
        return
      }
      if (evt.name === "escape") {
        setInputMode(false)
        // Restore focus to previous element
        previousFocus?.focus?.()
        previousFocus = null
        evt.preventDefault()
        return
      }
      // Let textarea handle other keys
      return
    }

    // Normal mode
    if (evt.name === "return") {
      props.onRespond("once")
      evt.preventDefault()
    }
    if (evt.name === "a") {
      props.onRespond("always")
      evt.preventDefault()
    }
    if (evt.name === "d") {
      props.onRespond("reject")
      evt.preventDefault()
    }
    if (evt.name === "i") {
      setInputMode(true)
      evt.preventDefault()
      // Save and blur current focus before focusing our textarea
      queueMicrotask(() => {
        previousFocus = renderer.currentFocusedRenderable
        previousFocus?.blur?.()
        if (textarea && !textarea.isDestroyed) {
          textarea.focus()
        }
      })
    }
  })

  return (
    <box flexDirection="column" gap={0} paddingTop={1}>
      {/* Permission request line */}
      <box flexDirection="row" gap={1}>
        <text>
          <InlinePermissionIndicator />
          <span style={{ fg: theme.warning }}> {title()}</span>
        </text>
      </box>

      {/* Input mode - show textarea for rejection message */}
      <Show when={inputMode()}>
        <box flexDirection="column" gap={0} paddingLeft={2}>
          <text fg={theme.warning}>Rejection message:</text>
          <textarea
            ref={(r: TextareaRenderable) => (textarea = r)}
            placeholder="Enter rejection reason..."
            keyBindings={[]}
            minHeight={1}
            maxHeight={3}
          />
          <box flexDirection="row" gap={2}>
            <text>
              <span style={{ fg: theme.primary, attributes: TextAttributes.BOLD }}>↵</span>
              <span style={{ fg: theme.textMuted }}> send</span>
            </text>
            <text>
              <span style={{ fg: theme.textMuted, attributes: TextAttributes.BOLD }}>esc</span>
              <span style={{ fg: theme.textMuted }}> cancel</span>
            </text>
          </box>
        </box>
      </Show>

      {/* Normal mode - action hints */}
      <Show when={!inputMode()}>
        <box flexDirection="row" gap={2} paddingLeft={2}>
          <text>
            <span style={{ fg: theme.primary, attributes: TextAttributes.BOLD }}>↵</span>
            <span style={{ fg: theme.textMuted }}> once</span>
          </text>
          <text>
            <span style={{ fg: theme.success, attributes: TextAttributes.BOLD }}>a</span>
            <span style={{ fg: theme.textMuted }}> always</span>
          </text>
          <text>
            <span style={{ fg: theme.error, attributes: TextAttributes.BOLD }}>d</span>
            <span style={{ fg: theme.textMuted }}> deny</span>
          </text>
          <text>
            <span style={{ fg: theme.warning, attributes: TextAttributes.BOLD }}>i</span>
            <span style={{ fg: theme.textMuted }}> msg</span>
          </text>
        </box>
      </Show>
    </box>
  )
}
