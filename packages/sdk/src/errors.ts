export interface SwarmApiErrorOptions {
  status: number;
  code?: string;
  details?: unknown;
  responseBody?: string;
}

export class SwarmError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SwarmError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmApiError extends SwarmError {
  readonly status: number;
  readonly code?: string;
  readonly details?: unknown;
  readonly responseBody?: string;

  constructor(message: string, options: SwarmApiErrorOptions) {
    super(message);
    this.name = 'SwarmApiError';
    this.status = options.status;
    this.code = options.code;
    this.details = options.details;
    this.responseBody = options.responseBody;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmAuthError extends SwarmApiError {
  constructor(message: string, options: Omit<SwarmApiErrorOptions, 'status'>) {
    super(message, { ...options, status: 401 });
    this.name = 'SwarmAuthError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmForbiddenError extends SwarmApiError {
  constructor(message: string, options: Omit<SwarmApiErrorOptions, 'status'>) {
    super(message, { ...options, status: 403 });
    this.name = 'SwarmForbiddenError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmNotFoundError extends SwarmApiError {
  constructor(message: string, options: Omit<SwarmApiErrorOptions, 'status'>) {
    super(message, { ...options, status: 404 });
    this.name = 'SwarmNotFoundError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmConflictError extends SwarmApiError {
  constructor(message: string, options: Omit<SwarmApiErrorOptions, 'status'>) {
    super(message, { ...options, status: 409 });
    this.name = 'SwarmConflictError';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * `workspaces.add` was refused with `409 workspace_not_visible`: the folder is
 * not visible to Swarm on this machine (a headless Swarm runs as its own user).
 * `hostChange` is the request that fixes it (`client.host.request`), to be
 * approved by the machine owner; `supported` is false when the machine has no
 * host-change helper, so no request can be applied there.
 */
export class SwarmWorkspaceNotVisibleError extends SwarmConflictError {
  readonly hostChange: { op: 'mount-workspace' | 'unmount-workspace'; path: string };
  readonly supported: boolean;

  constructor(message: string, options: Omit<SwarmApiErrorOptions, 'status'> & { hostChange: SwarmWorkspaceNotVisibleError['hostChange']; supported: boolean }) {
    const { hostChange, supported, ...rest } = options;
    super(message, { ...rest, code: 'workspace_not_visible' });
    this.name = 'SwarmWorkspaceNotVisibleError';
    this.hostChange = hostChange;
    this.supported = supported;
    Object.setPrototypeOf(this, new.target.prototype);
  }

  /** The `SwarmConflictError` for `workspace_not_visible`, retyped; any other error is returned as it is. */
  static from(err: unknown): unknown {
    if (!(err instanceof SwarmConflictError) || err.code !== 'workspace_not_visible' || err instanceof SwarmWorkspaceNotVisibleError) return err;
    const details = (err.details && typeof err.details === 'object' ? err.details : {}) as Record<string, unknown>;
    const fix = (details.host_change && typeof details.host_change === 'object' ? details.host_change : {}) as Record<string, unknown>;
    if (typeof fix.op !== 'string' || typeof fix.path !== 'string') return err;
    return new SwarmWorkspaceNotVisibleError(err.message, {
      details: err.details,
      responseBody: err.responseBody,
      hostChange: { op: fix.op as 'mount-workspace' | 'unmount-workspace', path: fix.path },
      supported: details.supported === true,
    });
  }
}

export class SwarmTimeoutError extends SwarmError {
  readonly timeoutMs: number;

  constructor(message: string, timeoutMs: number) {
    super(message);
    this.name = 'SwarmTimeoutError';
    this.timeoutMs = timeoutMs;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class SwarmValidationError extends SwarmError {
  readonly details?: unknown;

  constructor(message: string, details?: unknown) {
    super(message);
    this.name = 'SwarmValidationError';
    this.details = details;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

/**
 * `chat_reconnecting`: the connection to this conversation is being restored; retry shortly.
 * `chat_disconnected`: the connection is down and will not come back on its own; call `connect()`.
 * `chat_not_subscribed`: the conversation is not open in this client; `subscribe()` to it first.
 */
export type SwarmChatNotReadyCode = 'chat_reconnecting' | 'chat_disconnected' | 'chat_not_subscribed';

const chatNotReadyMessages: Record<SwarmChatNotReadyCode, string> = {
  chat_reconnecting: 'Not sent: reconnecting to Swarm, try again in a moment',
  chat_disconnected: 'Not sent: disconnected from Swarm, reconnect and try again',
  chat_not_subscribed: 'Not sent: this conversation is not open yet, open it and try again',
};

/**
 * A browser chat send refused before anything reached Swarm. The message is plain enough
 * to show people; apps branch on `code` (or `retryable`) instead of matching text.
 */
export class SwarmChatNotReadyError extends SwarmError {
  readonly code: SwarmChatNotReadyCode;
  /** True when retrying the same send shortly is expected to work without app action. */
  readonly retryable: boolean;

  constructor(code: SwarmChatNotReadyCode) {
    super(chatNotReadyMessages[code]);
    this.name = 'SwarmChatNotReadyError';
    this.code = code;
    this.retryable = code === 'chat_reconnecting';
    Object.setPrototypeOf(this, new.target.prototype);
  }
}
