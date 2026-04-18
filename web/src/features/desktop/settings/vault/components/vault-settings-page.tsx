import { useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { Lock, Unlock, Key, Shield, Download, Upload, AlertCircle, CheckCircle2 } from "lucide-react";
import { Button } from "../../../../../components/ui/button";
import { Dialog, DialogBackdrop, DialogPanel } from "../../../../../components/ui/dialog";
import { Input } from "../../../../../components/ui/input";
import { useDesktopStore } from "../../../state/use-desktop-store";

function downloadBundle(bundle: Uint8Array, filename: string) {
  const blob = new Blob([Uint8Array.from(bundle)], {
    type: "application/octet-stream",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function timestampedBundleName() {
  const now = new Date();
  const parts = [
    now.getFullYear(),
    String(now.getMonth() + 1).padStart(2, "0"),
    String(now.getDate()).padStart(2, "0"),
  ];
  const time = [
    String(now.getHours()).padStart(2, "0"),
    String(now.getMinutes()).padStart(2, "0"),
    String(now.getSeconds()).padStart(2, "0"),
  ];
  return `swarm-credentials-${parts.join("")}-${time.join("")}.swarmvault`;
}

export function VaultSettingsPage() {
  const vault = useDesktopStore((state) => state.vault);
  const enableVault = useDesktopStore((state) => state.enableVault);
  const unlockVault = useDesktopStore((state) => state.unlockVault);
  const exportBundle = useDesktopStore((state) => state.exportVaultBundle);
  const importBundle = useDesktopStore((state) => state.importVaultBundle);
  const lockVault = useDesktopStore((state) => state.lockVault);
  const disableVault = useDesktopStore((state) => state.disableVault);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [transferBusy, setTransferBusy] = useState(false);
  const [exportDialogOpen, setExportDialogOpen] = useState(false);
  const [exportPassword, setExportPassword] = useState("");
  const [disableDialogOpen, setDisableDialogOpen] = useState(false);
  const [disablePassword, setDisablePassword] = useState("");
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [importPassword, setImportPassword] = useState("");
  const [pendingImportBundle, setPendingImportBundle] = useState<Uint8Array | null>(null);

  const closeExportDialog = () => {
    if (transferBusy) {
      return;
    }
    setExportDialogOpen(false);
    setExportPassword("");
  };

  const closeDisableDialog = () => {
    if (vault.loading) {
      return;
    }
    setDisableDialogOpen(false);
    setDisablePassword("");
  };

  const closeImportDialog = () => {
    if (transferBusy) {
      return;
    }
    setImportDialogOpen(false);
    setImportPassword("");
    setPendingImportBundle(null);
  };

  const submitEnable = async () => {
    if (!password.trim()) {
      setError("Vault password is required.");
      return;
    }
    if (password !== confirm) {
      setError("Vault passwords do not match.");
      return;
    }
    setError(null);
    try {
      await enableVault(password);
      setPassword("");
      setConfirm("");
      setStatus("Vault enabled.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to enable vault");
    }
  };

  const submitUnlock = async () => {
    if (!password.trim()) {
      setError("Vault password is required.");
      return;
    }
    setError(null);
    try {
      await unlockVault(password);
      setPassword("");
      setStatus("Vault unlocked.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to unlock vault");
    }
  };

  const submitExport = async () => {
    if (!exportPassword.trim()) {
      setError("Enter vault password to export.");
      return;
    }
    setTransferBusy(true);
    setError(null);
    try {
      const result = await exportBundle(exportPassword);
      const filename = timestampedBundleName();
      downloadBundle(result.bundle, filename);
      setExportPassword("");
      setExportDialogOpen(false);
      setStatus(
        `Exported ${result.exported} credential(s). Browser download started. Move the file if needed, then delete it after import.`,
      );
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to export vault bundle",
      );
    } finally {
      setTransferBusy(false);
    }
  };

  const submitDisable = async () => {
    if (!disablePassword.trim()) {
      setError("Vault password is required to disable the vault.");
      return;
    }
    setError(null);
    try {
      await disableVault(disablePassword);
      setDisablePassword("");
      setDisableDialogOpen(false);
      setStatus("Vault disabled.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to disable vault");
    }
  };

  const onPickImportFile = () => {
    if (transferBusy || vault.loading) {
      return;
    }
    fileInputRef.current?.click();
  };

  const onImportFileSelected = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) {
      return;
    }
    try {
      const raw = new Uint8Array(await file.arrayBuffer());
      if (raw.length === 0) {
        throw new Error("Selected bundle file is empty");
      }
      setPendingImportBundle(raw);
      setImportPassword("");
      setError(null);
      setImportDialogOpen(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to read import bundle");
    }
  };

  const submitImport = async () => {
    if (!pendingImportBundle) {
      setError("Choose a bundle to import.");
      return;
    }
    if (!importPassword.trim()) {
      setError("Enter password to import this bundle.");
      return;
    }
    setTransferBusy(true);
    setError(null);
    try {
      const result = await importBundle(importPassword, pendingImportBundle);
      setImportPassword("");
      setPendingImportBundle(null);
      setImportDialogOpen(false);
      setStatus(
        `Imported ${result.imported} credential(s). Vault ${result.vault.unlocked ? "unlocked" : "updated"}. You can delete the import file now.`,
      );
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to import vault bundle",
      );
    } finally {
      setTransferBusy(false);
    }
  };

  const handleExportPasswordKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void submitExport();
    }
  };

  const handleDisablePasswordKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void submitDisable();
    }
  };

  const handleImportPasswordKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void submitImport();
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Vault</h1>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Secure local storage for agent environment variables.</p>
        </div>
      </div>

      <div className="space-y-6">
        {vault.warning || status || error || vault.error ? (
          <div className="space-y-2">
            {vault.warning ? (
              <div className="flex items-center gap-2 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-4 py-3 text-xs text-[var(--app-warning)]">
                <AlertCircle className="h-4 w-4 shrink-0" />
                {vault.warning}
              </div>
            ) : null}
            {status ? (
              <div className="flex items-center gap-2 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-xs text-[var(--app-success)]">
                <CheckCircle2 className="h-4 w-4 shrink-0" />
                {status}
              </div>
            ) : null}
            {error || vault.error ? (
              <div className="flex items-center gap-2 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-xs text-[var(--app-danger)]">
                <AlertCircle className="h-4 w-4 shrink-0" />
                {error || vault.error}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className="overflow-hidden rounded-xl border border-[var(--app-border)] transition-colors duration-300">
          {/* Main Status Row */}
          <div className="flex items-center justify-between border-b border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4 transition-colors">
            <div className="flex items-center gap-3">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)]">
                {vault.enabled ? (
                  vault.unlocked ? <Unlock className="h-5 w-5 text-[var(--app-success)]" /> : <Lock className="h-5 w-5 text-[var(--app-warning)]" />
                ) : (
                  <Shield className="h-5 w-5 text-[var(--app-text-muted)]" />
                )}
              </div>
              <div>
                <div className="text-sm font-medium text-[var(--app-text)]">Local Vault</div>
                <div className="text-xs text-[var(--app-text-muted)]">
                  {vault.enabled
                    ? vault.unlocked
                      ? "Enabled and unlocked"
                      : "Enabled and locked"
                    : "Off"}
                </div>
              </div>
            </div>

            {vault.enabled && vault.unlocked ? (
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  className="h-8 rounded-md px-3 text-xs transition-colors hover:bg-[var(--app-surface-subtle)]"
                  onClick={() =>
                    void lockVault().catch((err: unknown) =>
                      setError(err instanceof Error ? err.message : "Failed to lock vault"),
                    )
                  }
                  disabled={vault.loading}
                >
                  <Lock className="mr-1.5 h-3 w-3" />
                  {vault.loading ? "Locking…" : "Lock"}
                </Button>
                <Button
                  variant="outline"
                  className="h-8 rounded-md border-[var(--app-danger-border)] px-3 text-xs text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"
                  onClick={() => {
                    setError(null);
                    setDisablePassword("");
                    setDisableDialogOpen(true);
                  }}
                  disabled={vault.loading}
                >
                  <Shield className="mr-1.5 h-3 w-3" />
                  {vault.loading ? "Disabling…" : "Disable"}
                </Button>
              </div>
            ) : null}
          </div>

          {!vault.enabled ? (
            <div className="bg-[var(--app-bg)] px-4 py-5">
              <div className="mb-4 text-sm font-medium text-[var(--app-text)]">Enable Vault</div>
              <div className="flex flex-col gap-3 sm:flex-row">
                <Input
                  type="password"
                  className="h-10 flex-1 bg-[var(--app-surface-subtle)] border border-[var(--app-border)] transition-colors focus:border-[var(--app-border-strong)]"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  placeholder="New vault password"
                />
                <Input
                  type="password"
                  className="h-10 flex-1 bg-[var(--app-surface-subtle)] border border-[var(--app-border)] transition-colors focus:border-[var(--app-border-strong)]"
                  value={confirm}
                  onChange={(event) => setConfirm(event.target.value)}
                  placeholder="Confirm password"
                />
                <Button
                  className="h-10 rounded-md border border-[var(--app-primary)] bg-transparent px-6 text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
                  onClick={() => void submitEnable()}
                  disabled={vault.loading}
                >
                  {vault.loading ? "Enabling…" : "Enable"}
                </Button>
              </div>
            </div>
          ) : null}

          {vault.enabled && !vault.unlocked ? (
            <div className="bg-[var(--app-bg)] px-4 py-5">
              <div className="mb-4 text-sm font-medium text-[var(--app-text)]">Unlock Vault</div>
              <div className="flex flex-col gap-3 sm:flex-row">
                <Input
                  type="password"
                  className="h-10 flex-1 bg-[var(--app-surface-subtle)] border border-[var(--app-border)] transition-colors focus:border-[var(--app-border-strong)]"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  placeholder="Vault password"
                />
                <Button
                  className="h-10 rounded-md border border-[var(--app-primary)] bg-transparent px-8 text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
                  onClick={() => void submitUnlock()}
                  disabled={vault.loading}
                >
                  {vault.loading ? "Unlocking…" : "Unlock"}
                </Button>
              </div>
            </div>
          ) : null}

          {/* Transfer Actions Row */}
          <div className="flex flex-col sm:flex-row sm:items-center justify-between border-t border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 transition-colors hover:bg-[var(--app-surface-subtle)]">
            <div className="flex items-center gap-2 mb-3 sm:mb-0">
              <Key className="h-4 w-4 text-[var(--app-text-muted)]" />
              <div className="text-sm font-medium text-[var(--app-text)]">Transfer Bundle</div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                className="h-8 rounded-md px-3 text-xs border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-subtle)] text-[var(--app-text)] transition-colors"
                onClick={() => {
                  setError(null);
                  setExportPassword("");
                  setExportDialogOpen(true);
                }}
                disabled={transferBusy || vault.loading || (vault.enabled && !vault.unlocked)}
              >
                <Download className="mr-1.5 h-3 w-3" />
                {transferBusy ? "Working…" : "Export"}
              </Button>
              <Button
                variant="outline"
                className="h-8 rounded-md px-3 text-xs border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-subtle)] text-[var(--app-text)] transition-colors"
                onClick={onPickImportFile}
                disabled={transferBusy || vault.loading}
              >
                <Upload className="mr-1.5 h-3 w-3" />
                {transferBusy ? "Working…" : "Import"}
              </Button>
              <input
                ref={fileInputRef}
                type="file"
                className="hidden"
                onChange={(event) => void onImportFileSelected(event)}
              />
            </div>
          </div>
        </div>
      </div>

      {exportDialogOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Export bundle">
          <DialogBackdrop onClick={closeExportDialog} />
          <DialogPanel className="max-w-md gap-6 rounded-3xl p-8 border-[var(--app-border)] bg-[var(--app-surface-elevated)]">
            <div className="flex flex-col items-center gap-4 text-center">
              <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[var(--app-surface-soft)] text-[var(--app-text-muted)] ring-1 ring-[var(--app-border)]">
                <Download className="h-6 w-6" />
              </div>
              <div className="space-y-2">
                <div className="text-xl font-semibold text-[var(--app-text)]">
                  Export Bundle
                </div>
                <p className="text-sm leading-relaxed text-[var(--app-text-muted)]">
                  Enter vault password to confirm export.
                </p>
              </div>
            </div>
            <Input
              type="password"
              autoFocus
              className="h-12 bg-[var(--app-surface-soft)] border border-[var(--app-border)]"
              value={exportPassword}
              onChange={(event) => setExportPassword(event.target.value)}
              onKeyDown={handleExportPasswordKeyDown}
              placeholder="Vault password"
            />
            <div className="flex flex-col gap-2">
              <Button
                type="button"
                className="h-12 w-full rounded-xl border border-[var(--app-primary)] bg-transparent text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
                onClick={() => void submitExport()}
                disabled={transferBusy}
              >
                {transferBusy ? "Exporting…" : "Confirm Export"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                className="h-10 w-full rounded-xl text-[var(--app-text)]"
                onClick={closeExportDialog}
                disabled={transferBusy}
              >
                Cancel
              </Button>
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}

      {disableDialogOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Disable vault">
          <DialogBackdrop onClick={closeDisableDialog} />
          <DialogPanel className="max-w-md gap-6 rounded-3xl p-8 border-[var(--app-border)] bg-[var(--app-surface-elevated)]">
            <div className="flex flex-col items-center gap-4 text-center">
              <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[var(--app-danger-bg)] text-[var(--app-danger)] ring-1 ring-[var(--app-danger-border)]">
                <Shield className="h-6 w-6" />
              </div>
              <div className="space-y-2">
                <div className="text-xl font-semibold text-[var(--app-text)]">
                  Disable Vault
                </div>
                <p className="text-sm leading-relaxed text-[var(--app-text-muted)]">
                  Enter your vault password to permanently disable the vault and store credentials unencrypted on this device.
                </p>
              </div>
            </div>
            <Input
              type="password"
              autoFocus
              className="h-12 bg-[var(--app-surface-soft)] border border-[var(--app-border)]"
              value={disablePassword}
              onChange={(event) => setDisablePassword(event.target.value)}
              onKeyDown={handleDisablePasswordKeyDown}
              placeholder="Vault password"
            />
            <div className="flex flex-col gap-2">
              <Button
                type="button"
                className="h-12 w-full rounded-xl border border-[var(--app-danger-border)] bg-transparent text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"
                onClick={() => void submitDisable()}
                disabled={vault.loading}
              >
                {vault.loading ? "Disabling…" : "Confirm Disable"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                className="h-10 w-full rounded-xl text-[var(--app-text)]"
                onClick={closeDisableDialog}
                disabled={vault.loading}
              >
                Cancel
              </Button>
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}

      <Dialog
        className={importDialogOpen && pendingImportBundle ? undefined : "hidden"}
        aria-hidden={!importDialogOpen || !pendingImportBundle}
        role="dialog"
        aria-modal="true"
        aria-label="Import bundle"
      >
        <DialogBackdrop onClick={closeImportDialog} />
        <DialogPanel className="max-w-md gap-6 rounded-3xl p-8 border-[var(--app-border)] bg-[var(--app-surface-elevated)]">
          <div className="flex flex-col items-center gap-4 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[var(--app-surface-soft)] text-[var(--app-text-muted)] ring-1 ring-[var(--app-border)]">
              <Upload className="h-6 w-6" />
            </div>
            <div className="space-y-2">
              <div className="text-xl font-semibold text-[var(--app-text)]">
                Import Bundle
              </div>
              <p className="text-sm leading-relaxed text-[var(--app-text-muted)]">
                Enter the password used to encrypt this bundle.
              </p>
            </div>
          </div>
          <Input
            type="password"
            autoFocus={importDialogOpen}
            className="h-12 bg-[var(--app-surface-soft)] border border-[var(--app-border)]"
            value={importPassword}
            onChange={(event) => setImportPassword(event.target.value)}
            onKeyDown={handleImportPasswordKeyDown}
            placeholder="Bundle password"
          />
          <div className="flex flex-col gap-2">
            <Button
              type="button"
              className="h-12 w-full rounded-xl border border-[var(--app-primary)] bg-transparent text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
              onClick={() => void submitImport()}
              disabled={transferBusy || !pendingImportBundle}
            >
              {transferBusy ? "Importing…" : "Confirm Import"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              className="h-10 w-full rounded-xl text-[var(--app-text)]"
              onClick={closeImportDialog}
              disabled={transferBusy}
            >
              Cancel
            </Button>
          </div>
        </DialogPanel>
      </Dialog>
    </div>
  );
}
