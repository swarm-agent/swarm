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
