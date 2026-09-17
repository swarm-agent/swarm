import { useState } from 'react'
import { ShieldAlert, ShieldCheck, Settings2, Loader2, AlertTriangle } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { SessionUsageLimits, updateUsageLimits } from '../services/usage-api'

interface UsageLimitsCardProps {
  limits?: SessionUsageLimits
  onUpdated?: () => void
}

export function UsageLimitsCard({ limits, onUpdated }: UsageLimitsCardProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [limitInput, setLimitInput] = useState<string>(
    limits?.daily_cost_limit_usd ? String(limits.daily_cost_limit_usd) : '50.00',
  )
  const [enabled, setEnabled] = useState<boolean>(limits?.enabled ?? false)
  const [isSaving, setIsSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const todayCost = limits?.today_cost_usd ?? 0
  const limitCost = limits?.daily_cost_limit_usd ?? 0
  const isEnabled = limits?.enabled ?? false
  const isExceeded = limits?.limit_exceeded ?? false

  const pct = isEnabled && limitCost > 0 ? Math.min(100, (todayCost / limitCost) * 100) : 0
  const rawPct = isEnabled && limitCost > 0 ? (todayCost / limitCost) * 100 : 0
  const remaining = isEnabled && limitCost > 0 ? Math.max(0, limitCost - todayCost) : 0

  const handleStartEdit = () => {
    setLimitInput(limitCost > 0 ? String(limitCost) : '50.00')
    setEnabled(isEnabled)
    setSaveError(null)
    setIsEditing(true)
  }

  const handleSave = async () => {
    const parsedLimit = parseFloat(limitInput)
    if (isNaN(parsedLimit) || parsedLimit < 0) {
      setSaveError('Please enter a valid positive dollar amount')
      return
    }

    setIsSaving(true)
    setSaveError(null)
    try {
      await updateUsageLimits({
        daily_cost_limit_usd: parsedLimit,
        enabled,
      })
      setIsEditing(false)
      onUpdated?.()
    } catch (err: any) {
      setSaveError(err?.message || 'Failed to update usage limit')
    } finally {
      setIsSaving(false)
    }
  }

  return (
    <Card className="p-5 border border-[var(--app-border)] bg-[var(--app-panel)] text-[var(--app-text)] rounded-xl shadow-sm">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <div
            className={`flex h-10 w-10 items-center justify-center rounded-lg ${
              isExceeded
                ? 'bg-rose-500/15 text-rose-500'
                : isEnabled
                ? 'bg-emerald-500/15 text-emerald-500'
                : 'bg-[var(--app-element)] text-[var(--app-text-muted)]'
            }`}
          >
            {isExceeded ? (
              <ShieldAlert size={20} className="animate-pulse" />
            ) : isEnabled ? (
              <ShieldCheck size={20} />
            ) : (
              <Settings2 size={20} />
            )}
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold tracking-tight">Daily Spending Limit</h3>
              {isExceeded ? (
                <span className="inline-flex items-center gap-1 rounded-full bg-rose-500/20 px-2 py-0.5 text-xs font-semibold text-rose-400">
                  <AlertTriangle size={12} /> Limit Reached
                </span>
              ) : isEnabled ? (
                <span className="inline-flex items-center rounded-full bg-emerald-500/20 px-2 py-0.5 text-xs font-medium text-emerald-400">
                  Active
                </span>
              ) : (
                <span className="inline-flex items-center rounded-full bg-[var(--app-element)] px-2 py-0.5 text-xs font-medium text-[var(--app-text-muted)]">
                  Disabled
                </span>
              )}
            </div>
            <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
              Automatically stops running sessions and blocks new runs if daily spending exceeds your cap.
            </p>
          </div>
        </div>

        {!isEditing && (
          <Button
            variant="outline"
            size="sm"
            onClick={handleStartEdit}
            className="h-8 border-[var(--app-border)] text-xs shrink-0"
          >
            <Settings2 size={13} className="mr-1.5" />
            Configure Limit
          </Button>
        )}
      </div>

      {isEditing ? (
        <div className="mt-4 pt-4 border-t border-[var(--app-border)] space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label className="block text-xs font-medium text-[var(--app-text)] mb-1.5">
                Daily Budget / Cost Limit ($ USD)
              </label>
              <div className="relative">
                <span className="absolute left-3 top-1/2 -translate-y-1/2 text-xs text-[var(--app-text-muted)]">
                  $
                </span>
                <input
                  type="number"
                  step="0.01"
                  min="0"
                  value={limitInput}
                  onChange={(e) => setLimitInput(e.target.value)}
                  placeholder="50.00"
                  className="w-full rounded-md border border-[var(--app-border)] bg-[var(--app-bg)] pl-7 pr-3 py-1.5 text-xs text-[var(--app-text)] focus:border-indigo-500 focus:outline-none"
                />
              </div>
            </div>

            <div className="flex items-center sm:pt-6">
              <label className="flex items-center gap-2.5 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                  className="h-4 w-4 rounded border-[var(--app-border)] text-indigo-600 focus:ring-indigo-500"
                />
                <span className="text-xs font-medium text-[var(--app-text)]">
                  Enforce hard daily cutoff limit
                </span>
              </label>
            </div>
          </div>

          {saveError && (
            <p className="text-xs text-rose-500 bg-rose-500/10 p-2 rounded border border-rose-500/20">
              {saveError}
            </p>
          )}

          <div className="flex items-center justify-end gap-2 pt-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={isSaving}
              onClick={() => setIsEditing(false)}
              className="h-8 text-xs"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              disabled={isSaving}
              onClick={handleSave}
              className="h-8 text-xs bg-indigo-600 hover:bg-indigo-700 text-white"
            >
              {isSaving && <Loader2 size={13} className="mr-1.5 animate-spin" />}
              Save Limit
            </Button>
          </div>
        </div>
      ) : (
        <div className="mt-4 pt-4 border-t border-[var(--app-border)]">
          {isEnabled && limitCost > 0 ? (
            <div className="space-y-3">
              {/* Progress bar */}
              <div className="space-y-1.5">
                <div className="flex justify-between text-xs font-medium">
                  <span className="text-[var(--app-text)]">
                    ${todayCost.toFixed(4)} <span className="text-[var(--app-text-muted)] font-normal">spent today</span>
                  </span>
                  <span className={isExceeded ? 'text-rose-400 font-semibold' : 'text-[var(--app-text-muted)]'}>
                    ${limitCost.toFixed(2)} limit ({rawPct.toFixed(1)}%)
                  </span>
                </div>
                <div className="h-2 w-full overflow-hidden rounded-full bg-[var(--app-element)]">
                  <div
                    className={`h-full transition-all duration-300 rounded-full ${
                      isExceeded
                        ? 'bg-rose-500'
                        : pct >= 80
                        ? 'bg-amber-500'
                        : 'bg-emerald-500'
                    }`}
                    style={{ width: `${pct}%` }}
                  />
                </div>
              </div>

              {/* Stats badges */}
              <div className="grid grid-cols-3 gap-2 pt-1 text-xs text-center">
                <div className="rounded-lg bg-[var(--app-bg)]/60 p-2 border border-[var(--app-border)]/50">
                  <span className="text-[10px] uppercase tracking-wider text-[var(--app-text-muted)] block">Today's Cost</span>
                  <span className="font-semibold text-sm">${todayCost.toFixed(4)}</span>
                </div>
                <div className="rounded-lg bg-[var(--app-bg)]/60 p-2 border border-[var(--app-border)]/50">
                  <span className="text-[10px] uppercase tracking-wider text-[var(--app-text-muted)] block">Daily Limit</span>
                  <span className="font-semibold text-sm">${limitCost.toFixed(2)}</span>
                </div>
                <div className="rounded-lg bg-[var(--app-bg)]/60 p-2 border border-[var(--app-border)]/50">
                  <span className="text-[10px] uppercase tracking-wider text-[var(--app-text-muted)] block">Remaining</span>
                  <span className={`font-semibold text-sm ${isExceeded ? 'text-rose-400' : 'text-emerald-400'}`}>
                    ${remaining.toFixed(4)}
                  </span>
                </div>
              </div>
            </div>
          ) : (
            <div className="flex items-center justify-between text-xs text-[var(--app-text-muted)]">
              <span>No active daily spending limit is set. Sessions will run without cost caps.</span>
              <Button
                variant="ghost"
                size="sm"
                onClick={handleStartEdit}
                className="h-auto p-0 text-xs text-indigo-400 hover:text-indigo-300 underline"
              >
                Set a limit now
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  )
}
