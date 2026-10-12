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
