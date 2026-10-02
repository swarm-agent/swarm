import { useCallback, useEffect, useRef, useState } from 'react'

type Recognition = {
  continuous: boolean
  interimResults: boolean
  lang: string
  onstart: (() => void) | null
  onend: (() => void) | null
  onerror: ((event: { error?: string; message?: string }) => void) | null
  onresult: ((event: { resultIndex: number; results: ArrayLike<{ isFinal: boolean; 0?: { transcript: string } }> }) => void) | null
  start: () => void
  stop: () => void
  abort: () => void
}
type SpeechWindow = Window & {
  SpeechRecognition?: new () => Recognition
  webkitSpeechRecognition?: new () => Recognition
}

function errorMessage(code?: string, message?: string) {
  switch (code) {
    case 'not-allowed':
    case 'service-not-allowed': return 'Microphone permission was denied. Allow microphone access in your browser and try again.'
    case 'audio-capture': return 'No microphone is available. Check your microphone connection and browser settings.'
    case 'no-speech': return 'No speech was detected. Try dictation again.'
    case 'network': return 'Browser speech recognition could not connect. Check your connection and try again.'
    default: return message || 'Browser speech recognition failed. Try again.'
  }
}

/**
 * Browser-only dictation; the draft store remains the sole committed text authority.
 * interimText is replaceable display-only speech, never a draft replacement.
 * toggle() stops capture but active stays true until the browser's final flush ends.
 * Consumers must prevent submission while active (including the stop/flush window),
 * then submit the updated draft. cancel() deliberately discards pending speech and
 * invalidates late events; it is cleanup, not a finalize-before-send operation.
 */
export function useOrchestratorDictation(key: string, disabled: boolean, append: (update: (text: string) => string) => void) {
  const current = useRef({ key, disabled, append })
  current.current = { key, disabled, append }
  const recognitionRef = useRef<Recognition | null>(null)
  const [active, setActive] = useState(false)
  const [listening, setListening] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [interimText, setInterimText] = useState('')

  const cancel = useCallback(() => {
    const recognition = recognitionRef.current
    recognitionRef.current = null
    if (recognition) {
      recognition.onstart = recognition.onend = recognition.onerror = recognition.onresult = null
      try { recognition.abort() } catch { /* Browser may already have ended capture. */ }
    }
    setActive(false)
    setListening(false)
    setInterimText('')
  }, [])

  useEffect(() => {
    setError(null)
    return cancel
  }, [key, cancel])
  useEffect(() => { if (disabled) cancel() }, [disabled, cancel])

  const toggle = () => {
    if (current.current.disabled) return
    const running = recognitionRef.current
    if (running) {
      // Keep accepting the browser's final flush until onend, unless send/scope
      // cleanup invalidates this exact instance first. Never restart automatically.
      try { running.stop() } catch { cancel(); setError('Browser speech recognition failed to stop. Try again.') }
      return
    }
    const speechWindow = window as SpeechWindow
    const Constructor = speechWindow.SpeechRecognition ?? speechWindow.webkitSpeechRecognition
    if (!Constructor) {
      setError('Speech recognition is not supported in this browser. Try a browser with microphone dictation support.')
      return
    }
    setError(null)
    const scope = current.current.key
    try {
      const recognition = new Constructor()
      recognitionRef.current = recognition
      const valid = () => recognitionRef.current === recognition && current.current.key === scope && !current.current.disabled
      const finalized = new Set<number>()
      recognition.continuous = true
      recognition.interimResults = true
      recognition.lang = navigator.language || 'en-US'
      recognition.onstart = () => { if (valid()) setListening(true) }
      recognition.onresult = event => {
        if (!valid()) return
        for (let index = event.resultIndex; index < event.results.length; index += 1) {
          const result = event.results[index]
          if (!result?.isFinal || finalized.has(index)) continue
          finalized.add(index)
          const addition = (result[0]?.transcript || '').replace(/\s+/g, ' ').trim()
          if (!addition) continue
          current.current.append(text => `${text}${text && !/\s$/.test(text) && !/^[,.;:!?]/.test(addition) ? ' ' : ''}${addition}`)
        }
        // The results list is the browser's current snapshot; interim entries can
        // be revised or removed, including entries before resultIndex.
        const pending: string[] = []
        for (let index = 0; index < event.results.length; index += 1) {
          const result = event.results[index]
          if (!result || result.isFinal || finalized.has(index)) continue
          const text = (result[0]?.transcript || '').replace(/\s+/g, ' ').trim()
          if (text) pending.push(text)
        }
        setInterimText(pending.join(' '))
      }
      recognition.onerror = event => {
        if (!valid()) return
        setError(errorMessage(event.error, event.message))
        cancel()
      }
      recognition.onend = () => { if (valid()) cancel() }
      setActive(true)
      recognition.start()
    } catch (cause) {
      cancel()
      setError(`Microphone dictation could not start. ${cause instanceof Error ? cause.message : 'Check browser microphone settings and try again.'}`)
    }
  }

  return { active, listening, error, toggle, cancel, interimText }
}
