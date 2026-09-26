import type { StorageDriver } from '../types.js';

export class MemoryStorageDriver implements StorageDriver {
  private store = new Map<string, Uint8Array>();
  private textEncoder = new TextEncoder();
  private textDecoder = new TextDecoder();

  async read(key: string): Promise<Uint8Array | null> {
    const val = this.store.get(key);
    return val ? new Uint8Array(val) : null;
  }

  async readString(key: string): Promise<string | null> {
    const bytes = await this.read(key);
    return bytes ? this.textDecoder.decode(bytes) : null;
  }

  async write(key: string, data: Uint8Array | string): Promise<void> {
    const bytes = typeof data === 'string' ? this.textEncoder.encode(data) : new Uint8Array(data);
    this.store.set(key, bytes);
  }

  async delete(key: string): Promise<void> {
    this.store.delete(key);
  }

  async list(prefix: string): Promise<string[]> {
    const keys: string[] = [];
    for (const key of this.store.keys()) {
      if (key.startsWith(prefix)) {
        keys.push(key);
      }
    }
    return keys.sort();
  }

  async exists(key: string): Promise<boolean> {
    return this.store.has(key);
  }

  clear(): void {
    this.store.clear();
  }
}
