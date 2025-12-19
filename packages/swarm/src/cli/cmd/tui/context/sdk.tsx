import { createOpencodeClient, type Event } from "@swarm-ai/sdk"
import { createSimpleContext } from "./helper"
import { createGlobalEmitter } from "@solid-primitives/event-bus"
import { onCleanup } from "solid-js"

export const { use: useSDK, provider: SDKProvider } = createSimpleContext({
  name: "SDK",
  init: (props: { url: string }) => {
    const abort = new AbortController()
    const sdk = createOpencodeClient({
      baseUrl: props.url,
      signal: abort.signal,
      fetch: (req) => {
        // @ts-ignore
        req.timeout = false
        return fetch(req)
      },
    })

    const emitter = createGlobalEmitter<{
      [key in Event["type"]]: Extract<Event, { type: key }>
    }>()

    // SSE reconnection logic
    let reconnectAttempts = 0
    const maxReconnectDelay = 30000 // 30 seconds max
    const baseDelay = 1000 // 1 second base

    async function subscribeWithReconnect() {
      while (!abort.signal.aborted) {
        try {
          const events = await sdk.event.subscribe()
          reconnectAttempts = 0 // Reset on successful connection
          
          for await (const event of events.stream) {
            if (abort.signal.aborted) break
            console.log("event", event.type)
            emitter.emit(event.type, event)
          }
          
          // Stream ended normally (server closed connection)
          if (!abort.signal.aborted) {
            console.log("SSE stream ended, reconnecting...")
          }
        } catch (err) {
          if (abort.signal.aborted) break
          console.error("SSE connection error, reconnecting...", err)
        }
        
        // Exponential backoff with jitter
        if (!abort.signal.aborted) {
          const delay = Math.min(baseDelay * Math.pow(2, reconnectAttempts) + Math.random() * 1000, maxReconnectDelay)
          reconnectAttempts++
          await new Promise((resolve) => setTimeout(resolve, delay))
        }
      }
    }

    subscribeWithReconnect()

    onCleanup(() => {
      abort.abort()
    })

    return { client: sdk, event: emitter }
  },
})
