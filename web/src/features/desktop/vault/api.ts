import { requestJson } from "../../../app/api";
import { createDebugTimer } from '../../../lib/debug-log'
import {
  mapVaultStatus,
  type VaultImportResult,
  type VaultImportResultWire,
  type VaultStatus,
  type VaultStatusWire,
} from "./types";

function decodeVaultBundle(rawBundle: string | number[] | undefined): Uint8Array {
  if (typeof rawBundle === "string") {
    const encoded = rawBundle.trim();
    if (!encoded) {
      throw new Error("Vault export returned an empty bundle");
    }
    try {
      const binary = globalThis.atob(encoded);
      const decoded = Uint8Array.from(binary, (char) => char.charCodeAt(0));
      if (decoded.length === 0) {
        throw new Error("Vault export returned an empty bundle");
      }
      return decoded;
    } catch (error) {
      if (error instanceof Error && error.message === "Vault export returned an empty bundle") {
        throw error;
      }
      throw new Error("Vault export returned an invalid bundle payload");
    }
  }
  if (Array.isArray(rawBundle)) {
    const decoded = new Uint8Array(rawBundle);
    if (decoded.length === 0) {
      throw new Error("Vault export returned an empty bundle");
    }
    return decoded;
  }
  throw new Error("Vault export returned an invalid bundle payload");
}

export async function fetchVaultStatus(): Promise<VaultStatus> {
  const finish = createDebugTimer('desktop-vault-api', 'fetchVaultStatus')
  const status = mapVaultStatus(await requestJson<VaultStatusWire>("/v1/vault"));
  finish({ enabled: status.enabled, unlocked: status.unlocked, storageMode: status.storageMode })
  return status
}

export async function enableVault(password: string): Promise<VaultStatus> {
  return mapVaultStatus(
    await requestJson<VaultStatusWire>("/v1/vault/enable", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    }),
  );
}

export async function unlockVault(password: string): Promise<VaultStatus> {
  return mapVaultStatus(
    await requestJson<VaultStatusWire>("/v1/vault/unlock", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    }),
  );
}

export async function lockVault(): Promise<VaultStatus> {
  return mapVaultStatus(
    await requestJson<VaultStatusWire>("/v1/vault/lock", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    }),
  );
}

export async function disableVault(password: string): Promise<VaultStatus> {
  return mapVaultStatus(
    await requestJson<VaultStatusWire>("/v1/vault/disable", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    }),
  );
}

export async function exportVaultBundle(
  password: string,
  vaultPassword = "",
): Promise<{ exported: number; bundle: Uint8Array }> {
  const response = await requestJson<{ exported?: number; bundle?: string | number[] }>(
    "/v1/vault/export",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        password,
        vault_password: vaultPassword || undefined,
      }),
    },
  );

  return {
    exported: Number(response.exported ?? 0),
    bundle: decodeVaultBundle(response.bundle),
  };
}

export async function importVaultBundle(
  password: string,
  bundle: Uint8Array,
  vaultPassword = "",
): Promise<VaultImportResult> {
  const response = await requestJson<VaultImportResultWire>(
    "/v1/vault/import",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        password,
        vault_password: vaultPassword || undefined,
        bundle: Array.from(bundle),
      }),
    },
  );
  return {
    imported: Number(response.imported ?? 0),
    vault: mapVaultStatus(response.vault ?? {}),
  };
}
