export interface VaultStatusWire {
  enabled?: boolean;
  unlocked?: boolean;
  unlock_required?: boolean;
  storage_mode?: string;
  warning?: string;
}

export interface VaultStatus {
  enabled: boolean;
  unlocked: boolean;
  unlockRequired: boolean;
  storageMode: string;
  warning: string;
}

export interface VaultImportResultWire {
  imported?: number;
  vault?: VaultStatusWire;
}

export interface VaultImportResult {
  imported: number;
  vault: VaultStatus;
}

export function mapVaultStatus(status: VaultStatusWire): VaultStatus {
  return {
    enabled: Boolean(status.enabled),
    unlocked: Boolean(status.unlocked),
    unlockRequired: Boolean(status.unlock_required),
    storageMode: String(status.storage_mode ?? "").trim(),
    warning: String(status.warning ?? "").trim(),
  };
}
