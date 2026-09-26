import type { StorageDriver } from '../types.js';

export interface S3DriverConfig {
  bucket: string;
  endpoint?: string;
  region?: string;
  accessKeyId?: string;
  secretAccessKey?: string;
  sessionToken?: string;
  forcePathStyle?: boolean;
}

export class S3StorageDriver implements StorageDriver {
  private bucket: string;
  private endpoint: string;
  private region: string;
  private accessKeyId?: string;
  private secretAccessKey?: string;
  private sessionToken?: string;
  private forcePathStyle: boolean;

  constructor(config: S3DriverConfig) {
    this.bucket = config.bucket;
    this.region = config.region || 'us-east-1';
    this.endpoint = (config.endpoint || `https://s3.${this.region}.amazonaws.com`).replace(/\/+$/, '');
    this.accessKeyId = config.accessKeyId;
    this.secretAccessKey = config.secretAccessKey;
    this.sessionToken = config.sessionToken;
    this.forcePathStyle = config.forcePathStyle ?? true;
  }

  private getUrl(key: string): URL {
    const cleanKey = key.replace(/^\/+/, '');
    if (this.forcePathStyle) {
      return new URL(`${this.endpoint}/${this.bucket}/${cleanKey}`);
    }
    const endpointUrl = new URL(this.endpoint);
    return new URL(`${endpointUrl.protocol}//${this.bucket}.${endpointUrl.host}/${cleanKey}`);
  }

  private async sha256Hex(data: string | Uint8Array): Promise<string> {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const hashBuffer = await crypto.subtle.digest('SHA-256', bytes as any);
    return Array.from(new Uint8Array(hashBuffer))
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
  }

  private async hmacSha256(key: Uint8Array, data: string | Uint8Array): Promise<Uint8Array> {
    const cryptoKey = await crypto.subtle.importKey(
      'raw',
      key as any,
      { name: 'HMAC', hash: 'SHA-256' },
      false,
      ['sign']
    );
    const dataBytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const signature = await crypto.subtle.sign('HMAC', cryptoKey, dataBytes as any);
    return new Uint8Array(signature);
  }

  private async getSigningKey(dateStamp: string): Promise<Uint8Array> {
    const kSecret = new TextEncoder().encode(`AWS4${this.secretAccessKey || ''}`);
    const kDate = await this.hmacSha256(kSecret, dateStamp);
    const kRegion = await this.hmacSha256(kDate, this.region);
    const kService = await this.hmacSha256(kRegion, 's3');
    return this.hmacSha256(kService, 'aws4_request');
  }

  private async signRequest(
    method: string,
    url: URL,
    headers: Record<string, string>,
    payloadHash: string
  ): Promise<Record<string, string>> {
    if (!this.accessKeyId || !this.secretAccessKey) {
      return headers;
    }

    const now = new Date();
    const amzDate = now.toISOString().replace(/[:-]|\.\d{3}/g, '');
    const dateStamp = amzDate.substring(0, 8);

    const signedHeadersList = ['host', 'x-amz-content-sha256', 'x-amz-date'];
    const reqHeaders: Record<string, string> = {
      ...headers,
      host: url.host,
      'x-amz-date': amzDate,
      'x-amz-content-sha256': payloadHash,
    };

    if (this.sessionToken) {
      reqHeaders['x-amz-security-token'] = this.sessionToken;
      signedHeadersList.push('x-amz-security-token');
    }

    signedHeadersList.sort();
    const signedHeadersStr = signedHeadersList.join(';');

    const canonicalHeaders = signedHeadersList
      .map((h) => `${h}:${reqHeaders[h].trim()}\n`)
      .join('');

    const canonicalUri = url.pathname;
    const searchParams = new URLSearchParams(url.search);
    searchParams.sort();
    const canonicalQueryString = searchParams.toString();

    const canonicalRequest = [
      method,
      canonicalUri,
      canonicalQueryString,
      canonicalHeaders,
      signedHeadersStr,
      payloadHash,
    ].join('\n');

    const algorithm = 'AWS4-HMAC-SHA256';
    const credentialScope = `${dateStamp}/${this.region}/s3/aws4_request`;
    const hashedCanonicalRequest = await this.sha256Hex(canonicalRequest);
    const stringToSign = [
      algorithm,
      amzDate,
      credentialScope,
      hashedCanonicalRequest,
    ].join('\n');

    const signingKey = await this.getSigningKey(dateStamp);
    const signatureBytes = await this.hmacSha256(signingKey, stringToSign);
    const signature = Array.from(signatureBytes)
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');

    reqHeaders['Authorization'] = `${algorithm} Credential=${this.accessKeyId}/${credentialScope}, SignedHeaders=${signedHeadersStr}, Signature=${signature}`;

    return reqHeaders;
  }

  async read(key: string): Promise<Uint8Array | null> {
    const url = this.getUrl(key);
    const payloadHash = await this.sha256Hex('');
    const headers = await this.signRequest('GET', url, {}, payloadHash);

    const res = await fetch(url.toString(), { method: 'GET', headers });
    if (res.status === 404) {
      return null;
    }
    if (!res.ok) {
      throw new Error(`S3 read error ${res.status}: ${await res.text()}`);
    }
    const buf = await res.arrayBuffer();
    return new Uint8Array(buf);
  }

  async readString(key: string): Promise<string | null> {
    const bytes = await this.read(key);
    return bytes ? new TextDecoder().decode(bytes) : null;
  }

  async write(key: string, data: Uint8Array | string, contentType = 'application/octet-stream'): Promise<void> {
    const url = this.getUrl(key);
    const body = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const payloadHash = await this.sha256Hex(body);
    const headers = await this.signRequest(
      'PUT',
      url,
      { 'content-type': contentType },
      payloadHash
    );

    const res = await fetch(url.toString(), {
      method: 'PUT',
      headers,
      body: body as any,
    });
    if (!res.ok) {
      throw new Error(`S3 write error ${res.status}: ${await res.text()}`);
    }
  }

  async delete(key: string): Promise<void> {
    const url = this.getUrl(key);
    const payloadHash = await this.sha256Hex('');
    const headers = await this.signRequest('DELETE', url, {}, payloadHash);

    const res = await fetch(url.toString(), { method: 'DELETE', headers });
    if (!res.ok && res.status !== 404) {
      throw new Error(`S3 delete error ${res.status}: ${await res.text()}`);
    }
  }

  async list(prefix: string): Promise<string[]> {
    const cleanPrefix = prefix.replace(/^\/+/, '');
    const url = this.forcePathStyle
      ? new URL(`${this.endpoint}/${this.bucket}?list-type=2&prefix=${encodeURIComponent(cleanPrefix)}`)
      : new URL(`${new URL(this.endpoint).protocol}//${this.bucket}.${new URL(this.endpoint).host}?list-type=2&prefix=${encodeURIComponent(cleanPrefix)}`);

    const payloadHash = await this.sha256Hex('');
    const headers = await this.signRequest('GET', url, {}, payloadHash);

    const res = await fetch(url.toString(), { method: 'GET', headers });
    if (!res.ok) {
      throw new Error(`S3 list error ${res.status}: ${await res.text()}`);
    }

    const xml = await res.text();
    const keys: string[] = [];
    const keyMatches = xml.matchAll(/<Key>(.*?)<\/Key>/g);
    for (const match of keyMatches) {
      if (match[1]) {
        keys.push(match[1]);
      }
    }
    return keys;
  }

  async exists(key: string): Promise<boolean> {
    const url = this.getUrl(key);
    const payloadHash = await this.sha256Hex('');
    const headers = await this.signRequest('HEAD', url, {}, payloadHash);

    const res = await fetch(url.toString(), { method: 'HEAD', headers });
    if (res.status === 200) return true;
    if (res.status === 404) return false;
    throw new Error(`S3 exists error ${res.status}`);
  }
}
