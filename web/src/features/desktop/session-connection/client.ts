import { requestJson } from '../../../app/api'
import type {
  SessionConnectRequest,
  SessionConnectResponse,
  SessionStartRequest,
  SessionStartResponse,
} from './contract.generated'

export type SessionStartRequestInput = Omit<SessionStartRequest, 'client_id' | 'request_id'> & Partial<Pick<SessionStartRequest, 'client_id' | 'request_id'>>
import { createSessionConnectionRuntime, type SessionConnection, type SessionConnectionRuntimeOptions } from './runtime'

export interface SessionConnectionClientOptions extends SessionConnectionRuntimeOptions {
  clientId?: string
  requestJson?: typeof requestJson
}

export interface ConnectSessionInput {
  sessionId: string
  resumeToken?: string | null
  requestId?: string
}

export interface StartSessionInput {
  request: SessionStartRequestInput
}

export interface StartedSessionConnection {
  response: SessionStartResponse
  connection: SessionConnection
}

export class SessionConnectionClient {
  private readonly clientId: string
  private readonly request: typeof requestJson
  private readonly runtimeOptions: SessionConnectionRuntimeOptions

  constructor(options: SessionConnectionClientOptions = {}) {
    this.clientId = options.clientId?.trim() || `desktop:${crypto.randomUUID()}`
    this.request = options.requestJson ?? requestJson
    this.runtimeOptions = {
      openSocket: options.openSocket,
      setTimeout: options.setTimeout,
      clearTimeout: options.clearTimeout,
    }
  }

  async connect(input: ConnectSessionInput): Promise<SessionConnection> {
    const sessionId = input.sessionId.trim()
    if (!sessionId) throw new Error('Session connection requires sessionId')

    const request: SessionConnectRequest = {
      client_id: this.clientId,
      request_id: input.requestId?.trim() || crypto.randomUUID(),
      resume_token: input.resumeToken?.trim() || null,
    }
    const response = await this.request<SessionConnectResponse>(
      `/v3/sessions/${encodeURIComponent(sessionId)}:connect`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(request),
      },
    )
    return createSessionConnectionRuntime(response, this.runtimeOptions)
  }

  async start(input: StartSessionInput): Promise<StartedSessionConnection> {
    const response = await this.request<SessionStartResponse>('/v3/sessions:start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ...input.request,
        client_id: input.request.client_id?.trim() || this.clientId,
        request_id: input.request.request_id?.trim() || crypto.randomUUID(),
      }),
    })
    return {
      response,
      connection: createSessionConnectionRuntime(response, this.runtimeOptions),
    }
  }
}

let defaultSessionConnectionClient: SessionConnectionClient | undefined

export function sessionConnectionClient(): SessionConnectionClient {
  if (!defaultSessionConnectionClient) defaultSessionConnectionClient = new SessionConnectionClient()
  return defaultSessionConnectionClient
}

export function resetSessionConnectionClientForTests(client?: SessionConnectionClient): void {
  defaultSessionConnectionClient = client
}
