import {
  SwarmApiError,
  SwarmAuthError,
  SwarmConflictError,
  SwarmForbiddenError,
  SwarmNotFoundError,
  SwarmTimeoutError,
} from './errors.js';
import type { RequestOptions, ResolvedSwarmClientConfig } from './types.js';

declare const Buffer: any;

export interface TransportResponse<T = unknown> {
  status: number;
  headers: Record<string, string>;
  data: T;
  rawText: string;
}

export class SwarmTransport {
  private config: ResolvedSwarmClientConfig;

  constructor(config: ResolvedSwarmClientConfig) {
    this.config = { ...config };
  }

  setConfig(partial: Partial<ResolvedSwarmClientConfig>): void {
    this.config = { ...this.config, ...partial };
  }

  getConfig(): ResolvedSwarmClientConfig {
    return { ...this.config };
  }

  async request<T = unknown>(path: string, options: RequestOptions = {}): Promise<TransportResponse<T>> {
    const timeoutMs = options.timeoutMs ?? this.config.timeoutMs;
    const method = (options.method ?? 'GET').toUpperCase();

    // Prepare headers
    const headers: Record<string, string> = {
      ...this.config.defaultHeaders,
      ...options.headers,
    };

    if (this.config.token && !headers['Authorization'] && !headers['authorization']) {
      headers['Authorization'] = `Bearer ${this.config.token}`;
    }

    let bodyStr: string | undefined;
    if (options.body !== undefined && options.body !== null) {
      if (typeof options.body === 'string') {
        bodyStr = options.body;
      } else {
        bodyStr = JSON.stringify(options.body);
        if (!headers['Content-Type'] && !headers['content-type']) {
          headers['Content-Type'] = 'application/json';
        }
      }
    }

    // If socketPath is configured, route via Node.js Unix domain socket
    if (this.config.socketPath) {
      return this.requestSocket<T>(path, method, headers, bodyStr, timeoutMs);
    }

    // Standard HTTP/HTTPS via fetch
    return this.requestFetch<T>(path, method, headers, bodyStr, timeoutMs, options.signal);
  }

  private async requestFetch<T>(
    path: string,
    method: string,
    headers: Record<string, string>,
    bodyStr: string | undefined,
    timeoutMs: number,
    externalSignal?: AbortSignal
  ): Promise<TransportResponse<T>> {
    const url = `${this.config.baseUrl.replace(/\/+$/, '')}${path.startsWith('/') ? path : `/${path}`}`;

    const controller = new AbortController();
    let timeoutId: ReturnType<typeof setTimeout> | undefined;

    if (timeoutMs > 0) {
      timeoutId = setTimeout(() => {
        controller.abort(new SwarmTimeoutError(`Request timed out after ${timeoutMs}ms`, timeoutMs));
      }, timeoutMs);
    }

    const abortHandler = () => controller.abort(externalSignal?.reason);
    if (externalSignal) {
      externalSignal.addEventListener('abort', abortHandler, { once: true });
    }

    try {
      const res = await fetch(url, {
        method,
        headers,
        body: bodyStr,
        signal: controller.signal,
      });

      const rawText = await res.text();
      const responseHeaders: Record<string, string> = {};
      res.headers.forEach((v, k) => {
        responseHeaders[k.toLowerCase()] = v;
      });

      let data: T;
      try {
        data = (rawText.length > 0 ? JSON.parse(rawText) : null) as T;
      } catch {
        data = rawText as unknown as T;
      }

      if (!res.ok) {
        this.handleErrorResponse(res.status, data, rawText);
      }

      return {
        status: res.status,
        headers: responseHeaders,
        data,
        rawText,
      };
    } catch (err) {
      if (err instanceof SwarmTimeoutError) {
        throw err;
      }
      if (err instanceof Error && err.name === 'AbortError') {
        if (externalSignal?.aborted) {
          throw new SwarmApiError('Request aborted by caller', { status: 0 });
        }
        throw new SwarmTimeoutError(`Request timed out after ${timeoutMs}ms`, timeoutMs);
      }
      if (err instanceof SwarmApiError) {
        throw err;
      }
      throw new SwarmApiError(err instanceof Error ? err.message : String(err), {
        status: 0,
        details: err,
      });
    } finally {
      if (timeoutId) clearTimeout(timeoutId);
      if (externalSignal) externalSignal.removeEventListener('abort', abortHandler);
    }
  }

  private async requestSocket<T>(
    path: string,
    method: string,
    headers: Record<string, string>,
    bodyStr: string | undefined,
    timeoutMs: number
  ): Promise<TransportResponse<T>> {
    let httpModule: any;
    try {
      // Dynamic import using variable to avoid static compile-time module resolution
      const mod = 'node:http';
      httpModule = await import(mod);
    } catch {
      throw new SwarmApiError('Unix domain socket transport requires Node.js runtime', { status: 0 });
    }

    return new Promise((resolve, reject) => {
      const formattedPath = path.startsWith('/') ? path : `/${path}`;
      const reqHeaders = { ...headers };
      if (bodyStr !== undefined) {
        const byteLength =
          typeof Buffer !== 'undefined'
            ? Buffer.byteLength(bodyStr)
            : new TextEncoder().encode(bodyStr).length;
        reqHeaders['Content-Length'] = byteLength.toString();
      }

      const req = httpModule.request(
        {
          socketPath: this.config.socketPath,
          path: formattedPath,
          method,
          headers: reqHeaders,
          timeout: timeoutMs > 0 ? timeoutMs : undefined,
        },
        (res: any) => {
          const chunks: any[] = [];
          res.on('data', (chunk: any) => chunks.push(chunk));
          res.on('end', () => {
            let rawText = '';
            if (typeof Buffer !== 'undefined') {
              rawText = Buffer.concat(chunks).toString('utf8');
            } else {
              rawText = chunks.map((c) => String(c)).join('');
            }

            const responseHeaders: Record<string, string> = {};
            for (const [k, v] of Object.entries(res.headers || {})) {
              if (v) responseHeaders[k.toLowerCase()] = Array.isArray(v) ? v.join(', ') : String(v);
            }

            let data: T;
            try {
              data = (rawText.length > 0 ? JSON.parse(rawText) : null) as T;
            } catch {
              data = rawText as unknown as T;
            }

            const status = res.statusCode || 200;
            if (status >= 400) {
              try {
                this.handleErrorResponse(status, data, rawText);
              } catch (apiErr) {
                return reject(apiErr);
              }
            }

            resolve({
              status,
              headers: responseHeaders,
              data,
              rawText,
            });
          });
        }
      );

      if (timeoutMs > 0) {
        req.on('timeout', () => {
          req.destroy(new SwarmTimeoutError(`Socket request timed out after ${timeoutMs}ms`, timeoutMs));
        });
      }

      req.on('error', (err: any) => {
        if (err instanceof SwarmTimeoutError) {
          return reject(err);
        }
        reject(new SwarmApiError(`Socket error: ${err?.message || String(err)}`, { status: 0, details: err }));
      });

      if (bodyStr !== undefined) {
        req.write(bodyStr);
      }
      req.end();
    });
  }

  private handleErrorResponse(status: number, data: unknown, rawText: string): never {
    let message = `API request failed with status ${status}`;
    let code: string | undefined;
    let details: unknown = undefined;

    if (data && typeof data === 'object') {
      const obj = data as Record<string, unknown>;
      if (typeof obj.error === 'string' && obj.error) {
        message = obj.error;
      } else if (typeof obj.message === 'string' && obj.message) {
        message = obj.message;
      }
      if (typeof obj.code === 'string') {
        code = obj.code;
      }
      details = obj.details ?? obj;
    } else if (rawText && rawText.length < 200) {
      message = rawText;
    }

    const options = {
      code,
      details,
      responseBody: rawText,
    };

    switch (status) {
      case 401:
        throw new SwarmAuthError(message, options);
      case 403:
        throw new SwarmForbiddenError(message, options);
      case 404:
        throw new SwarmNotFoundError(message, options);
      case 409:
        throw new SwarmConflictError(message, options);
      default:
        throw new SwarmApiError(message, { ...options, status });
    }
  }
}
