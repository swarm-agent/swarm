import { useEffect, useMemo, useState, type ChangeEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Plus, Settings2, Trash2 } from "lucide-react";
import { requestJson } from "../../../../../app/api";
import { Dialog, DialogBackdrop, DialogPanel } from "../../../../../components/ui/dialog";
import { ModalCloseButton } from "../../../../../components/ui/modal-close-button";
import { cn } from "../../../../../lib/cn";
import {
  modelOptionsQueryOptions,
  agentStateQueryOptions,
  agentSettingsStateQueryOptions,
  agentToolContractQueryOptions,
} from "../../../../queries/query-options";
import {
  resetAgentDefaults,
  restoreAgentDefaults,
} from "../../../../desktop/chat/queries/chat-queries";
import { refreshAgentModelMutationCaches } from "../../../chat/queries/agent-preference-mutations";
import type {
  AgentProfileRecord,
  AgentStateRecord,
  ModelOptionRecord,
  AgentToolContractRuntimeRecord,
  AgentToolContractToolRecord,
  AgentToolInventoryPresetRecord,
  ProviderDefaultsPreviewRecord,
} from "../../../chat/types/chat";
import {
  AGENT_TOOL_PRESET_OPTIONS,
  CUSTOM_AGENT_TOOL_PRESET_ID,
  agentToolPresetByID,
} from "../../../chat/services/agent-tool-presets";
import { displayModelName, modelServiceTierOptions, normalizeModelServiceTier, supportsModelServiceTier } from "../../../chat/services/model-options";

interface AgentFormState {
  name: string;
  mode: string;
  description: string;
  provider: string;
  model: string;
  thinking: string;
  modelMode: "single" | "split";
  planProvider: string;
  planModel: string;
  planThinking: string;
  planServiceTier: string;
  autoProvider: string;
  autoModel: string;
  autoThinking: string;
  autoServiceTier: string;
  prompt: string;
  runtimeMode: "plan_auto" | "read" | "readwrite" | "";
  executionSetting: "read" | "readwrite" | "";
  exitPlanModeEnabled: boolean;
  toolContractPreset: string;
  toolContractInheritPolicy: boolean;
  toolContractTools: Record<string, AgentToolContractToolRecord>;
  enabled: boolean;
}

interface UtilityAIFormState {
  provider: string;
  model: string;
  thinking: string;
}

const CUSTOM_AGENT_TOOL_PRESET = AGENT_TOOL_PRESET_OPTIONS.find(
  (preset) => preset.id === CUSTOM_AGENT_TOOL_PRESET_ID,
)!;

type ToolAccessTone = "allow" | "block";

interface ToolAccessList {
  allowed: string[];
  blocked: string[];
}

function sortedUnique(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort(
    (left, right) => left.localeCompare(right),
  );
}

function sameStringSet(left: string[], right: string[]): boolean {
  const leftSet = new Set(left.map((value) => value.trim()).filter(Boolean));
  const rightSet = new Set(right.map((value) => value.trim()).filter(Boolean));
  if (leftSet.size !== rightSet.size) {
    return false;
  }
  for (const value of leftSet) {
    if (!rightSet.has(value)) {
      return false;
    }
  }
  return true;
}

function displayListLabel(values: string[], fallback: string): string {
  return values.length ? values.join(", ") : fallback;
}

const NEW_AGENT_KEY = "__new__";
const THINKING_OPTIONS = [
  { value: "", label: "Default" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X-High" },
];


const UTILITY_THINKING_OPTIONS = [
  { value: "off", label: "Off" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X-High" },
];

function presetLabel(preset: { id: string; label: string }): string {
  return preset.label.trim() && preset.label !== preset.id
    ? `${preset.label} (${preset.id})`
    : preset.id;
}

function mergedPresetOptions(
  runtime: AgentToolContractRuntimeRecord | undefined,
  presets: AgentToolInventoryPresetRecord[] | undefined,
  activePreset: string,
): AgentToolInventoryPresetRecord[] {
  const byID = new Map<string, AgentToolInventoryPresetRecord>();
  for (const preset of AGENT_TOOL_PRESET_OPTIONS) {
    byID.set(preset.id, preset);
  }
  for (const preset of presets ?? []) {
    if (preset.id.trim()) {
      byID.set(preset.id.trim(), preset);
    }
  }
  const runtimePreset = runtime?.resolved?.rawPreset || runtime?.rawToolContract?.preset || "";
  for (const presetID of [activePreset, runtimePreset]) {
    const trimmed = presetID.trim();
    if (trimmed && !byID.has(trimmed)) {
      byID.set(trimmed, {
        id: trimmed,
        label: trimmed,
        description: "Custom preset from saved runtime contract.",
        enabledTools: [],
        disabledByDefault: [],
        bashPrefixes: [],
      });
    }
  }
  return Array.from(byID.values()).sort((left, right) => {
    if (left.id === CUSTOM_AGENT_TOOL_PRESET_ID) return 1;
    if (right.id === CUSTOM_AGENT_TOOL_PRESET_ID) return -1;
    return left.label.localeCompare(right.label);
  });
}

function toolAccessForPreset(
  preset: AgentToolInventoryPresetRecord | null | undefined,
): ToolAccessList {
  return {
    allowed: sortedUnique(preset?.enabledTools ?? []),
    blocked: sortedUnique(preset?.disabledByDefault ?? []),
  };
}

function effectiveToolAccess(
  preset: AgentToolInventoryPresetRecord | null | undefined,
  overrides: AgentFormState["toolContractTools"],
  fallbackNames: string[] = [],
): ToolAccessList {
  const allowed = new Set(toolAccessForPreset(preset).allowed);
  const blocked = new Set(toolAccessForPreset(preset).blocked);
  for (const name of fallbackNames) {
    const toolName = name.trim();
    if (!toolName || allowed.has(toolName) || blocked.has(toolName)) {
      continue;
    }
    blocked.add(toolName);
  }
  for (const [name, config] of Object.entries(overrides)) {
    const toolName = name.trim();
    if (!toolName || typeof config.enabled !== "boolean") {
      continue;
    }
    if (config.enabled) {
      allowed.add(toolName);
      blocked.delete(toolName);
    } else {
      blocked.add(toolName);
      allowed.delete(toolName);
    }
  }
  return {
    allowed: sortedUnique(Array.from(allowed)),
    blocked: sortedUnique(Array.from(blocked)),
  };
}

function matchedToolPresetID(
  access: ToolAccessList,
  presets: AgentToolInventoryPresetRecord[],
): string {
  const matched = presets.find((preset) => {
    if (preset.id === CUSTOM_AGENT_TOOL_PRESET_ID) {
      return false;
    }
    return (
      sameStringSet(preset.enabledTools, access.allowed) &&
      sameStringSet(preset.disabledByDefault, access.blocked)
    );
  });
  return matched?.id ?? CUSTOM_AGENT_TOOL_PRESET_ID;
}

function customToolsFromAccess(access: ToolAccessList): AgentFormState["toolContractTools"] {
  const tools: AgentFormState["toolContractTools"] = {};
  for (const name of access.allowed) {
    tools[name] = { enabled: true, bashPrefixes: [] };
  }
  for (const name of access.blocked) {
    tools[name] = { enabled: false, bashPrefixes: [] };
  }
  return tools;
}

function resolvedToolAccess(
  runtime: AgentToolContractRuntimeRecord | undefined,
): ToolAccessList | null {
  if (!runtime?.resolved) {
    return null;
  }
  return {
    allowed: sortedUnique(runtime.resolved.availableTools),
    blocked: sortedUnique(runtime.resolved.unavailableTools),
  };
}

function sameToolConfig(
  left: AgentToolContractToolRecord | undefined,
  right: AgentToolContractToolRecord | undefined,
): boolean {
  if (!left && !right) return true;
  if (!left || !right) return false;
  return left.enabled === right.enabled && sameStringSet(left.bashPrefixes, right.bashPrefixes);
}

function sameToolContractTools(
  left: AgentFormState["toolContractTools"],
  right: AgentFormState["toolContractTools"],
): boolean {
  const names = new Set([...Object.keys(left), ...Object.keys(right)]);
  for (const name of names) {
    if (!sameToolConfig(left[name], right[name])) {
      return false;
    }
  }
  return true;
}

function formMatchesSavedToolContract(
  form: AgentFormState,
  profile: AgentProfileRecord | null,
): boolean {
  if (!profile) {
    return false;
  }
  const saved = profileToForm(profile);
  return (
    form.toolContractPreset.trim() === saved.toolContractPreset.trim() &&
    form.toolContractInheritPolicy === saved.toolContractInheritPolicy &&
    sameToolContractTools(form.toolContractTools, saved.toolContractTools)
  );
}

const READ_ONLY_TOOL_NAMES = new Set([
  "ask_user",
  "exit_plan_mode",
  "agentic_search",
  "list",
  "manage_agent",
  "manage_flow",
  "manage_image",
  "manage_integrations",
  "manage_skill",
  "manage_theme",
  "manage_todos",
  "manage_worktree",
  "plan_manage",
  "read",
  "search",
  "skill_use",
  "webfetch",
  "websearch",
]);

const MUTATING_TOOL_NAMES = new Set(["write", "edit", "bash", "task", "git_add", "git_commit"]);

const DIRECT_READ_BASELINE: ToolAccessList = {
  allowed: sortedUnique(Array.from(READ_ONLY_TOOL_NAMES).filter((name) => name !== "exit_plan_mode")),
  blocked: sortedUnique(["write", "edit", "bash", "task", "exit_plan_mode"]),
};

const DIRECT_READWRITE_BASELINE: ToolAccessList = {
  allowed: sortedUnique([
    "ask_user",
    "edit",
    "list",
    "plan_manage",
    "read",
    "search",
    "skill_use",
    "webfetch",
    "websearch",
    "write",
  ]),
  blocked: sortedUnique(["bash", "task", "exit_plan_mode"]),
};

function runtimeModeForToolAccess(access: ToolAccessList): "read" | "readwrite" {
  for (const tool of access.allowed) {
    if (MUTATING_TOOL_NAMES.has(tool) || !READ_ONLY_TOOL_NAMES.has(tool)) {
      return "readwrite";
    }
  }
  return "read";
}

function toolAccessForExecutionModeChange(
  currentAccess: ToolAccessList,
  mode: AgentFormState["runtimeMode"],
  runtimeToolNames: string[],
): ToolAccessList {
  if (mode === "plan_auto") {
    return {
      allowed: sortedUnique([...currentAccess.allowed, "exit_plan_mode"]),
      blocked: sortedUnique(currentAccess.blocked.filter((name) => name !== "exit_plan_mode")),
    };
  }

  const baseline = mode === "read" ? DIRECT_READ_BASELINE : DIRECT_READWRITE_BASELINE;
  const allowed = new Set(baseline.allowed);
  const blocked = new Set(baseline.blocked);
  for (const name of runtimeToolNames) {
    if (!allowed.has(name)) {
      blocked.add(name);
    }
  }
  return {
    allowed: sortedUnique(Array.from(allowed)),
    blocked: sortedUnique(Array.from(blocked).filter((name) => !allowed.has(name))),
  };
}

function runtimeModeLabel(mode: AgentFormState["runtimeMode"]): string {
  switch (mode) {
    case "plan_auto":
      return "Plan approval";
    case "read":
      return "Direct read-only";
    case "readwrite":
      return "Direct read/write";
    default:
      return "Unset";
  }
}

function normalizeExecutionMode(form: AgentFormState): AgentFormState["runtimeMode"] {
  if (form.exitPlanModeEnabled || form.runtimeMode === "plan_auto") {
    return "plan_auto";
  }
  return form.runtimeMode || form.executionSetting || "readwrite";
}

function toolAccessForExecutionMode(
  access: ToolAccessList,
  mode: AgentFormState["runtimeMode"],
): ToolAccessList {
  if (mode === "plan_auto") {
    return access;
  }
  return {
    allowed: sortedUnique(access.allowed.filter((name) => name !== "exit_plan_mode")),
    blocked: sortedUnique([...access.blocked, "exit_plan_mode"]),
  };
}

function ToolAccessRow({
  label,
  count,
  items,
  emptyText,
  tone,
  onItemClick,
  disabled = false,
  itemTitle,
}: {
  label: string;
  count: number;
  items: string[];
  emptyText: string;
  tone: ToolAccessTone;
  onItemClick?: (item: string) => void;
  disabled?: boolean;
  itemTitle?: string;
}) {
  const toneClassName =
    tone === "allow"
      ? "border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]"
      : "border-[color-mix(in_srgb,var(--app-danger)_45%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-danger)_10%,transparent)] text-[var(--app-danger)]";
  return (
    <div className="grid gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 sm:grid-cols-[8rem_1fr] sm:items-start">
      <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-muted)]">
        <span>{label}</span>
        <span className="rounded-md border border-[var(--app-border)] px-1.5 py-0.5 text-[10px] leading-none text-[var(--app-text-muted)]">
          {count}
        </span>
      </div>
      {items.length > 0 ? (
        <div className="flex min-w-0 flex-wrap gap-1.5">
          {items.map((item) =>
            onItemClick ? (
              <button
                key={item}
                type="button"
                onClick={() => onItemClick(item)}
                disabled={disabled}
                title={itemTitle}
                className={cn(
                  "rounded-md border px-2 py-0.5 text-left text-xs leading-5",
                  toneClassName,
                  !disabled && "hover:border-[var(--app-primary)]",
                )}
              >
                {item}
              </button>
            ) : (
              <span
                key={item}
                className={cn("rounded-md border px-2 py-0.5 text-xs leading-5", toneClassName)}
              >
                {item}
              </span>
            ),
          )}
        </div>
      ) : (
        <div className="text-xs leading-5 text-[var(--app-text-muted)]">{emptyText}</div>
      )}
    </div>
  );
}

function emptyAgentForm(): AgentFormState {
  return {
    name: "",
    mode: "primary",
    description: "",
    provider: "",
    model: "",
    thinking: "",
    modelMode: "single",
    planProvider: "",
    planModel: "",
    planThinking: "",
    planServiceTier: "",
    autoProvider: "",
    autoModel: "",
    autoThinking: "",
    autoServiceTier: "",
    prompt: "",
    runtimeMode: "readwrite",
    executionSetting: "readwrite",
    exitPlanModeEnabled: false,
    toolContractPreset: "",
    toolContractInheritPolicy: false,
    toolContractTools: {},
    enabled: true,
  };
}

function agentRuntimeSummary(profile: AgentProfileRecord): string {
  const mode = profile.runtimeMode || (profile.exitPlanModeEnabled ? "plan_auto" : profile.executionSetting || "");
  return runtimeModeLabel(mode);
}

function agentProviderModelSummary(
  profile: AgentProfileRecord,
  fallback = "Default model",
): string {
  if (profile.modelMode === "split") {
    const planProvider = profile.planProvider.trim();
    const planModel = profile.planModel.trim();
    const autoProvider = profile.autoProvider.trim();
    const autoModel = profile.autoModel.trim();
    if (planProvider && planModel && autoProvider && autoModel) {
      return `Plan ${planProvider}/${planModel} → Auto ${autoProvider}/${autoModel}`;
    }
  }
  const provider = profile.provider.trim();
  const model = profile.model.trim();
  if (provider && model) {
    return `${provider}/${model}`;
  }
  return fallback;
}

function formatCurrentDefaultModel(preview: ProviderDefaultsPreviewRecord | null): string {
  const provider = preview?.provider?.trim() || "";
  const model = preview?.primaryModel?.trim() || "";
  if (provider && model) {
    return `${provider}/${model}`;
  }
  return "the current default model";
}

function utilityAIForProfiles(
  profiles: AgentProfileRecord[],
  preview: ProviderDefaultsPreviewRecord | null,
): UtilityAIFormState {
  const utilityNames = preview?.utilityBaselineAgents?.length
    ? preview.utilityBaselineAgents
    : preview?.customUtilityAgents?.length
      ? []
      : preview?.utilityAgents?.length
        ? preview.utilityAgents
        : ["explorer", "memory", "parallel"];
  for (const name of utilityNames) {
    const profile = profiles.find((entry) =>
      entry.name.trim().toLowerCase() === name.trim().toLowerCase(),
    );
    if (profile?.provider?.trim() && profile.model.trim()) {
      return {
        provider: profile.provider.trim(),
        model: profile.model.trim(),
        thinking: profile.thinking.trim() || "off",
      };
    }
  }
  return {
    provider: preview?.utilityProvider?.trim() || preview?.provider?.trim() || "",
    model: preview?.utilityModel?.trim() || "",
    thinking: preview?.utilityThinking?.trim() || "off",
  };
}

function defaultUtilityThinkingForProvider(provider: string): string {
  switch (provider.trim().toLowerCase()) {
    case "copilot":
    case "fireworks":
      return "high";
    default:
      return "xhigh";
  }
}

function modelOptionKey(provider: string, model: string, contextMode = ""): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`;
}

function modelOptionFor(provider: string, model: string, modelOptions: ModelOptionRecord[]): ModelOptionRecord | null {
  return modelOptions.find((option) => option.provider === provider && option.model === model) ?? null;
}

function serviceTierOptionsForModel(provider: string, model: string, modelOptions: ModelOptionRecord[]) {
  const option = modelOptionFor(provider, model, modelOptions);
  return modelServiceTierOptions(provider, model, option?.serviceTiers ?? []);
}

function modelSupportsServiceTierSetting(provider: string, model: string, modelOptions: ModelOptionRecord[], tier = ""): boolean {
  const option = modelOptionFor(provider, model, modelOptions);
  return supportsModelServiceTier(provider, model, option?.serviceTiers ?? [], tier);
}

function normalizedSingleServiceTier(form: AgentFormState, modelOptions: ModelOptionRecord[]): string {
  return modelSupportsServiceTierSetting(form.provider, form.model, modelOptions, form.autoServiceTier)
    ? normalizeModelServiceTier(form.provider, form.autoServiceTier)
    : "";
}

function normalizedSplitServiceTier(form: AgentFormState, prefix: AgentSplitFieldPrefix, modelOptions: ModelOptionRecord[]): string {
  const provider = splitFieldValue(form, prefix, "provider");
  const model = splitFieldValue(form, prefix, "model");
  const serviceTier = splitFieldValue(form, prefix, "serviceTier");
  return modelSupportsServiceTierSetting(provider, model, modelOptions, serviceTier)
    ? normalizeModelServiceTier(provider, serviceTier)
    : "";
}

function normalizeAgentModelFields(form: AgentFormState, modelOptions: ModelOptionRecord[]): AgentFormState {
  if (form.modelMode === "split") {
    return {
      ...form,
      provider: "",
      model: "",
      thinking: "",
      planServiceTier: normalizedSplitServiceTier(form, "plan", modelOptions),
      autoServiceTier: normalizedSplitServiceTier(form, "auto", modelOptions),
    };
  }
  return {
    ...form,
    planProvider: "",
    planModel: "",
    planThinking: "",
    planServiceTier: "",
    autoProvider: "",
    autoModel: "",
    autoThinking: "",
    autoServiceTier: normalizedSingleServiceTier(form, modelOptions),
  };
}

type AgentSplitFieldPrefix = "plan" | "auto";

type AgentSplitModelSectionProps = {
  title: string;
  description: string;
  prefix: AgentSplitFieldPrefix;
  form: AgentFormState;
  providerOptions: string[];
  modelOptions: ModelOptionRecord[];
  selectedProfile: AgentProfileRecord | null;
  currentDefaultModelLabel: string;
  busy: boolean;
  setForm: (updater: (current: AgentFormState) => AgentFormState) => void;
};

function splitFieldValue(form: AgentFormState, prefix: AgentSplitFieldPrefix, field: "provider" | "model" | "thinking" | "serviceTier"): string {
  if (prefix === "plan") {
    switch (field) {
      case "provider":
        return form.planProvider;
      case "model":
        return form.planModel;
      case "thinking":
        return form.planThinking;
      case "serviceTier":
        return form.planServiceTier;
    }
  }
  switch (field) {
    case "provider":
      return form.autoProvider;
    case "model":
      return form.autoModel;
    case "thinking":
      return form.autoThinking;
    case "serviceTier":
      return form.autoServiceTier;
  }
}

function withSplitFieldValue(
  form: AgentFormState,
  prefix: AgentSplitFieldPrefix,
  field: "provider" | "model" | "thinking" | "serviceTier",
  value: string,
  modelOptions: ModelOptionRecord[],
): AgentFormState {
  if (prefix === "plan") {
    switch (field) {
      case "provider":
        return { ...form, planProvider: value, planModel: value === form.planProvider ? form.planModel : "", planServiceTier: value === form.planProvider ? form.planServiceTier : "" };
      case "model":
        return { ...form, planModel: value, planServiceTier: modelSupportsServiceTierSetting(form.planProvider, value, modelOptions, form.planServiceTier) ? form.planServiceTier : "" };
      case "thinking":
        return { ...form, planThinking: value };
      case "serviceTier":
        return { ...form, planServiceTier: value };
    }
  }
  switch (field) {
    case "provider":
      return { ...form, autoProvider: value, autoModel: value === form.autoProvider ? form.autoModel : "", autoServiceTier: value === form.autoProvider ? form.autoServiceTier : "" };
    case "model":
      return { ...form, autoModel: value, autoServiceTier: modelSupportsServiceTierSetting(form.autoProvider, value, modelOptions, form.autoServiceTier) ? form.autoServiceTier : "" };
    case "thinking":
      return { ...form, autoThinking: value };
    case "serviceTier":
      return { ...form, autoServiceTier: value };
  }
}

function splitModelChoices(
  provider: string,
  modelOptions: ModelOptionRecord[],
  selectedProfile: AgentProfileRecord | null,
  prefix: AgentSplitFieldPrefix,
): string[] {
  const trimmedProvider = provider.trim();
  if (!trimmedProvider) {
    return [];
  }
  const values = new Set<string>();
  for (const option of modelOptions) {
    if (option.provider === trimmedProvider && option.model.trim() !== "") {
      values.add(option.model.trim());
    }
  }
  if (selectedProfile) {
    const savedProvider = prefix === "plan" ? selectedProfile.planProvider : selectedProfile.autoProvider;
    const savedModel = prefix === "plan" ? selectedProfile.planModel : selectedProfile.autoModel;
    if (savedProvider === trimmedProvider && savedModel.trim() !== "") {
      values.add(savedModel.trim());
    }
  }
  return Array.from(values).sort((left, right) => left.localeCompare(right));
}

function SplitModelSection({
  title,
  description,
  prefix,
  form,
  providerOptions,
  modelOptions,
  selectedProfile,
  currentDefaultModelLabel,
  busy,
  setForm,
}: AgentSplitModelSectionProps) {
  const provider = splitFieldValue(form, prefix, "provider");
  const model = splitFieldValue(form, prefix, "model");
  const thinking = splitFieldValue(form, prefix, "thinking");
  const serviceTier = splitFieldValue(form, prefix, "serviceTier");
  const modelChoices = splitModelChoices(provider, modelOptions, selectedProfile, prefix);
  const serviceTierOptions = serviceTierOptionsForModel(provider, model, modelOptions);
  const serviceTierSupported = serviceTierOptions.length > 1;
  const normalizedServiceTier = serviceTierSupported ? normalizeModelServiceTier(provider, serviceTier) : "";

  return (
    <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
      <div className="mb-4">
        <div className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">{title}</div>
        <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">{description}</p>
      </div>
      <div className="space-y-3">
        <div className="flex items-center gap-3">
          <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Provider</label>
          <div className="relative min-w-0 flex-1">
            <select
              value={provider}
              onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                const nextProvider = event.target.value;
                setForm((current) => withSplitFieldValue(current, prefix, "provider", nextProvider, modelOptions));
              }}
              disabled={busy}
              className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
            >
              <option value="" disabled>Choose provider</option>
              {providerOptions.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Model</label>
          <div className="relative min-w-0 flex-1">
            <select
              value={model}
              onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                setForm((current) => withSplitFieldValue(current, prefix, "model", event.target.value, modelOptions));
              }}
              disabled={busy || !provider.trim()}
              className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
            >
              <option value="" disabled>Choose model</option>
              {modelChoices.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Thinking</label>
          <div className="relative min-w-0 flex-1">
            <select
              value={thinking}
              onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                setForm((current) => withSplitFieldValue(current, prefix, "thinking", event.target.value, modelOptions));
              }}
              disabled={busy}
              className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
            >
              {THINKING_OPTIONS.map((option) => (
                <option key={option.label} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Service tier</label>
          <div className="relative min-w-0 flex-1">
            <select
              value={normalizedServiceTier}
              onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                setForm((current) => withSplitFieldValue(current, prefix, "serviceTier", event.target.value, modelOptions));
              }}
              disabled={busy || !serviceTierSupported}
              className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
            >
              {serviceTierOptions.map((option) => (
                <option key={option.label} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </div>
      </div>
      <p className="mt-3 text-xs leading-5 text-[var(--app-text-muted)]">
        Split mode requires explicit provider/model choices for both Plan and Auto. Choose a provider/model here to lock this mode to that preset. Service tier options are provider-specific. Current default chat model: {currentDefaultModelLabel}.
      </p>
    </div>
  );
}

function legacyToolScopeTools(
  profile: AgentProfileRecord,
): Record<string, AgentToolContractToolRecord> {
  const tools: Record<string, AgentToolContractToolRecord> = {};
  for (const tool of profile.toolScope?.allowTools ?? []) {
    tools[tool] = { enabled: true, bashPrefixes: [] };
  }
  for (const tool of profile.toolScope?.denyTools ?? []) {
    tools[tool] = { enabled: false, bashPrefixes: [] };
  }
  if ((profile.toolScope?.bashPrefixes ?? []).length > 0) {
    tools.bash = {
      enabled: true,
      bashPrefixes: profile.toolScope?.bashPrefixes ?? [],
    };
  }
  return tools;
}

function profileToForm(
  profile: AgentProfileRecord | null | undefined,
): AgentFormState {
  if (!profile) {
    return emptyAgentForm();
  }
  return {
    name: profile.name,
    mode: profile.mode || "primary",
    description: profile.description,
    provider: profile.provider,
    model: profile.model,
    thinking: profile.thinking,
    modelMode: profile.modelMode,
    planProvider: profile.planProvider,
    planModel: profile.planModel,
    planThinking: profile.planThinking,
    planServiceTier: profile.planServiceTier,
    autoProvider: profile.autoProvider,
    autoModel: profile.autoModel,
    autoThinking: profile.autoThinking,
    autoServiceTier: profile.autoServiceTier,
    prompt: profile.prompt,
    runtimeMode: profile.exitPlanModeEnabled ? "plan_auto" : profile.runtimeMode,
    executionSetting: profile.exitPlanModeEnabled ? "" : profile.executionSetting,
    exitPlanModeEnabled: profile.exitPlanModeEnabled || profile.runtimeMode === "plan_auto",
    toolContractPreset:
      profile.toolContract?.preset ?? profile.toolScope?.preset ?? "",
    toolContractInheritPolicy: Boolean(
      profile.toolContract?.inheritPolicy ?? profile.toolScope?.inheritPolicy,
    ),
    toolContractTools: profile.toolContract?.tools ?? legacyToolScopeTools(profile),
    enabled: profile.enabled,
  };
}

async function upsertAgent(input: AgentFormState): Promise<string> {
  const toolContractTools: Record<
    string,
    { enabled?: boolean; bash_prefixes?: string[] }
  > = {};
  for (const [name, config] of Object.entries(input.toolContractTools)) {
    const toolName = name.trim();
    if (!toolName) {
      continue;
    }
    const bashPrefixes = config.bashPrefixes
      .map((value) => value.trim())
      .filter(Boolean);
    const toolConfig: { enabled?: boolean; bash_prefixes?: string[] } = {};
    if (typeof config.enabled === "boolean") {
      toolConfig.enabled = config.enabled;
    }
    if (bashPrefixes.length > 0) {
      toolConfig.bash_prefixes = bashPrefixes;
      if (toolConfig.enabled === undefined) {
        toolConfig.enabled = true;
      }
    }
    if (toolConfig.enabled !== undefined || toolConfig.bash_prefixes) {
      toolContractTools[toolName] = toolConfig;
    }
  }

  const resolvedExecutionMode = normalizeExecutionMode(input) || "readwrite";

  const response = await requestJson<{ profile?: { name?: string } }>(
    `/v2/agents/${encodeURIComponent(input.name.trim())}`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        mode: input.mode,
        description: input.description.trim(),
        provider: input.modelMode === "split" ? "" : input.provider,
        model: input.modelMode === "split" ? "" : input.model,
        thinking: input.modelMode === "split" ? "" : input.thinking,
        model_mode: input.modelMode === "split" ? "split" : "single",
        plan_provider: input.planProvider,
        plan_model: input.planModel,
        plan_thinking: input.planThinking,
        plan_service_tier: input.planServiceTier,
        auto_provider: input.autoProvider,
        auto_model: input.autoModel,
        auto_thinking: input.autoThinking,
        auto_service_tier: input.autoServiceTier,
        prompt: input.prompt,
        runtime_mode: resolvedExecutionMode,
        execution_setting:
          resolvedExecutionMode === "plan_auto" ? "" : resolvedExecutionMode,
        exit_plan_mode_enabled: resolvedExecutionMode === "plan_auto",
        tool_contract: {
          preset:
            input.toolContractPreset.trim() &&
            input.toolContractPreset.trim() !== CUSTOM_AGENT_TOOL_PRESET_ID
              ? input.toolContractPreset.trim()
              : undefined,
          tools:
            Object.keys(toolContractTools).length > 0
              ? toolContractTools
              : undefined,
          inherit_policy: input.toolContractInheritPolicy,
        },
        enabled: input.enabled,
      }),
    },
  );
  return String(response.profile?.name ?? input.name).trim();
}

async function activatePrimaryAgent(name: string): Promise<void> {
  await requestJson("/v2/agents/active/primary", {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ name: name.trim() }),
  });
}

async function deleteAgent(name: string): Promise<void> {
  await requestJson(`/v2/agents/${encodeURIComponent(name.trim())}`, {
    method: "DELETE",
  });
}

function actionButtonClassName(
  intent: "primary" | "secondary" | "danger",
): string {
  if (intent === "primary") {
    return "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[var(--app-primary)] bg-transparent px-4 py-2 text-sm font-medium text-[var(--app-primary)] shadow-sm transition-colors hover:bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)] hover:text-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50";
  }
  if (intent === "danger") {
    return "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[var(--app-danger)]/25 bg-[var(--app-danger)]/10 px-4 py-2 text-sm font-medium text-[var(--app-danger)] shadow-sm transition-colors hover:bg-[var(--app-danger)]/18 disabled:cursor-not-allowed disabled:opacity-50";
  }
  return "inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-2 text-sm font-medium text-[var(--app-text)] shadow-sm transition-colors hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50";
}

async function setUtilityAI(input: {
  utilityProvider: string;
  utilityModel: string;
  utilityThinking: string;
  overwriteExplicit?: boolean;
}): Promise<AgentStateRecord> {
  return restoreAgentDefaults(input);
}

function isUtilityAgent(profileName: string, utilityAgents: string[]): boolean {
  const normalized = profileName.trim().toLowerCase();
  return utilityAgents.some(
    (name) => name.trim().toLowerCase() === normalized,
  );
}

function PromptEditor({
  form,
  onSavePrompt,
  busy,
  disabled,
}: {
  form: AgentFormState;
  onSavePrompt: (prompt: string) => Promise<void>;
  busy: boolean;
  disabled: boolean;
}) {
  const [isEditing, setIsEditing] = useState(false);
  const [currentPrompt, setCurrentPrompt] = useState(form.prompt);

  useEffect(() => {
    if (!isEditing) {
      setCurrentPrompt(form.prompt);
    }
  }, [form.prompt, isEditing]);

  useEffect(() => {
    setIsEditing(false);
  }, [form.name]);

  const hasChanges = currentPrompt !== form.prompt;

  const handleSavePrompt = async () => {
    await onSavePrompt(currentPrompt);
    setIsEditing(false);
  };

  if (!isEditing) {
    return (
      <div
        className="w-full cursor-pointer rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
        onClick={() => {
          if (!disabled) setIsEditing(true);
        }}
      >
        {form.prompt ? (
          <div className="line-clamp-3 whitespace-pre-wrap font-mono text-sm opacity-80 text-[var(--app-text)]">
            {form.prompt}
          </div>
        ) : (
          <div className="font-mono text-sm text-[var(--app-text-muted)] italic">
            No system prompt set. Click to edit.
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="flex w-full flex-col gap-3 rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-bg)] p-4 shadow-sm">
      <textarea
        value={currentPrompt}
        onChange={(e) => setCurrentPrompt(e.target.value)}
        disabled={busy}
        placeholder="System prompt / instructions for this agent"
        className="min-h-[240px] w-full resize-y bg-transparent font-mono text-sm leading-relaxed text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-muted)]"
      />
      <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] pt-3">
        <button
          type="button"
          onClick={() => {
            setCurrentPrompt(form.prompt);
            setIsEditing(false);
          }}
          disabled={busy}
          className="rounded-md px-3 py-1.5 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)]"
        >
          Cancel
        </button>
        {hasChanges && (
          <button
            type="button"
            onClick={() => void handleSavePrompt()}
            disabled={busy}
            className="rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-medium text-[var(--app-primary)] transition-colors hover:bg-[var(--app-surface-subtle)] disabled:opacity-50"
          >
            {busy ? "Saving…" : "Save Prompt"}
          </button>
        )}
      </div>
    </div>
  );
}

function UtilityAISettingsModal({
  open,
  value,
  options,
  utilityAgents,
  customUtilityAgents,
  baselineUtilityAgents,
  busy,
  error,
  onChange,
  onClose,
  onApply,
  onClearOverrides,
}: {
  open: boolean;
  value: UtilityAIFormState;
  options: ModelOptionRecord[];
  utilityAgents: string[];
  customUtilityAgents: string[];
  baselineUtilityAgents: string[];
  busy: boolean;
  error: string | null;
  onChange: (next: UtilityAIFormState) => void;
  onClose: () => void;
  onApply: () => Promise<void>;
  onClearOverrides: () => Promise<void>;
}) {
  const providers = useMemo(() => {
    const groups = new Map<string, ModelOptionRecord[]>();
    for (const option of options) {
      const provider = option.provider.trim();
      const model = option.model.trim();
      if (!provider || !model) {
        continue;
      }
      const next: ModelOptionRecord = { ...option, provider, model };
      const list = groups.get(provider) ?? [];
      if (!list.some((entry) => entry.model === model)) {
        list.push(next);
      }
      groups.set(provider, list);
    }
    return Array.from(groups.entries()).sort(([left], [right]) =>
      left.localeCompare(right),
    );
  }, [options]);

  const activeProvider = value.provider.trim() || providers[0]?.[0] || "";
  const activeModels =
    providers.find(([provider]) => provider === activeProvider)?.[1] ?? [];
  const selectedKey = modelOptionKey(value.provider.trim(), value.model.trim());
  const utilityAgentsLabel = displayListLabel(
    utilityAgents,
    "explorer, memory, parallel",
  );
  const baselineAgentsLabel = displayListLabel(
    baselineUtilityAgents,
    customUtilityAgents.length > 0 ? "none" : utilityAgentsLabel,
  );
  const customAgentsLabel = customUtilityAgents.join(", ");
  const hasOverrides = customUtilityAgents.length > 0;
  const hasBaselineTargets = baselineUtilityAgents.length > 0;
  const canApply = value.provider.trim() !== "" && value.model.trim() !== "";
  const clearOverridesTitle = hasOverrides
    ? `Clear overrides for ${customAgentsLabel} and apply this Utility AI to all built-in utility agents.`
    : "Apply this Utility AI to all built-in utility agents; no per-agent overrides are currently detected.";
  const clearOverridesLabel = hasOverrides
    ? `Clear overrides + set Utility AI (${customUtilityAgents.length})`
    : "Clear overrides + set Utility AI";

  if (!open) {
    return null;
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Set Utility AI">
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="mx-auto flex w-[min(860px,calc(100vw-24px))] max-w-[860px] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(860px,calc(100vw-48px))]">
        <form
          className="flex min-h-0 flex-col"
          onSubmit={(event) => {
            event.preventDefault();
            if (!busy && canApply) {
              void onApply();
            }
          }}
        >
          <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4">
            <div>
              <h2 className="text-base font-semibold text-[var(--app-text)]">
                Set Utility AI
              </h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                Pick the provider/model for the shared Utility AI baseline. Set
                Utility AI fills only blank agents ({baselineAgentsLabel}); Clear
                overrides also moves custom utility agents back onto that baseline.
              </p>
            </div>
            <ModalCloseButton
              type="button"
              onClick={onClose}
              disabled={busy}
              aria-label="Close Utility AI picker"
            />
          </div>

          <div className="max-h-[calc(100vh-220px)] overflow-y-auto p-5">
            {error ? (
              <div className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">
                {error}
              </div>
            ) : null}

            <div className="mb-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
              <div>
                Selected Utility AI: {canApply ? `${value.provider}/${value.model}` : "choose a provider and model"}
              </div>
              <div className="mt-1">
                {hasBaselineTargets
                  ? `Set Utility AI updates blank/baseline agents: ${baselineAgentsLabel}.`
                  : "No built-in utility agents are currently using the shared baseline."}
              </div>
              {hasOverrides ? (
                <div className="mt-1">
                  Overrides currently exist for {customAgentsLabel}. Use Set
                  Utility AI to leave them alone, or Clear overrides + set Utility
                  AI to replace them with this baseline.
                </div>
              ) : null}
            </div>

            <div className="grid min-h-[360px] overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] md:grid-cols-[220px_minmax(0,1fr)]">
              <div className="border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] md:border-b-0 md:border-r">
                <div className="border-b border-[var(--app-border)] px-4 py-3 text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                  Providers
                </div>
                <div className="max-h-[320px] overflow-y-auto py-1">
                  {providers.length === 0 ? (
                    <div className="px-4 py-6 text-sm text-[var(--app-text-muted)]">
                      No runnable providers available.
                    </div>
                  ) : (
                    providers.map(([provider, models]) => {
                      const isActive = provider === activeProvider;
                      const hasSelected =
                        value.provider.trim() === provider && value.model.trim() !== "";
                      return (
                        <button
                          key={provider}
                          type="button"
                          onClick={() => {
                            const selectedModel = models.find(
                              (option) => option.model === value.model,
                            );
                            const selected = selectedModel ?? models[0];
                            onChange({
                              provider,
                              model: selected?.model || "",
                              thinking:
                                selected?.thinking ||
                                value.thinking ||
                                defaultUtilityThinkingForProvider(provider),
                            });
                          }}
                          disabled={busy}
                          className={`flex w-full items-center justify-between gap-2 px-4 py-3 text-left text-sm transition ${
                            isActive
                              ? "bg-[var(--app-surface)] text-[var(--app-text)]"
                              : "text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          } disabled:cursor-not-allowed disabled:opacity-60`}
                        >
                          <span className="truncate font-medium">{provider}</span>
                          <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">
                            {hasSelected ? "selected" : models.length}
                          </span>
                        </button>
                      );
                    })
                  )}
                </div>
              </div>

              <div className="min-w-0">
                <div className="border-b border-[var(--app-border)] px-4 py-3 text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                  {activeProvider || "Models"}
                </div>
                <div className="max-h-[320px] overflow-y-auto py-1">
                  {activeModels.length === 0 ? (
                    <div className="px-4 py-6 text-sm text-[var(--app-text-muted)]">
                      Select a provider to choose a model.
                    </div>
                  ) : (
                    activeModels.map((option) => {
                      const key = modelOptionKey(option.provider, option.model);
                      const isSelected = key === selectedKey;
                      return (
                        <button
                          key={key}
                          type="button"
                          onClick={() => {
                            onChange({
                              provider: option.provider,
                              model: option.model,
                              thinking:
                                option.thinking ||
                                value.thinking ||
                                defaultUtilityThinkingForProvider(option.provider),
                            });
                          }}
                          disabled={busy}
                          className={`flex w-full items-start gap-3 px-4 py-3 text-left text-sm transition ${
                            isSelected
                              ? "bg-[var(--app-surface-subtle)] text-[var(--app-text)]"
                              : "text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          } disabled:cursor-not-allowed disabled:opacity-60`}
                        >
                          <span className="mt-0.5 w-[14px] shrink-0 text-[var(--app-primary)]">
                            {isSelected ? "✓" : ""}
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="block truncate font-medium text-[var(--app-text)]">
                              {displayModelName(option.provider, option.model, "")}
                            </span>
                            <span className="mt-1 block truncate text-[11px] text-[var(--app-text-subtle)]">
                              {option.label || `${option.provider}/${option.model}`}
                            </span>
                          </span>
                        </button>
                      );
                    })
                  )}
                </div>
              </div>
            </div>

            <div className="mt-4 flex items-center gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-3">
              <label className="shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                Thinking
              </label>
              <div className="relative min-w-0 flex-1">
                <select
                  value={value.thinking || "off"}
                  onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                    onChange({ ...value, thinking: event.target.value })
                  }
                  disabled={busy}
                  className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {UTILITY_THINKING_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
                <ChevronDown
                  size={14}
                  className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                />
              </div>
            </div>
          </div>

          <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-[var(--app-border)] px-5 py-4">
            <button
              type="button"
              onClick={onClose}
              disabled={busy}
              className={actionButtonClassName("secondary")}
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => {
                if (!busy && canApply) {
                  void onClearOverrides();
                }
              }}
              disabled={busy || !canApply}
              className={actionButtonClassName("secondary")}
              title={clearOverridesTitle}
            >
              {busy ? "Setting…" : clearOverridesLabel}
            </button>
            <button
              type="submit"
              disabled={busy || !canApply}
              className={actionButtonClassName("primary")}
              title="Set Utility AI only for blank/inheriting utility agents; existing per-agent overrides stay intact."
            >
              {busy ? "Setting…" : "Set Utility AI"}
            </button>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  );
}

export function AgentsSettingsPage() {
  const queryClient = useQueryClient();
  const {
    data: agentState,
    isLoading,
    isFetching,
    refetch: refetchAgentState,
  } = useQuery(agentSettingsStateQueryOptions());
  const { data: modelOptions = [] } = useQuery(modelOptionsQueryOptions());

  useEffect(() => {
    void refetchAgentState();
  }, [refetchAgentState]);

  const profiles = agentState?.profiles ?? [];
  const activePrimary = agentState?.activePrimary?.trim() || "swarm";
  const providerDefaultsPreview = agentState?.providerDefaultsPreview ?? null;
  const currentDefaultModelLabel = formatCurrentDefaultModel(providerDefaultsPreview);

  const [viewMode, setViewMode] = useState<"list" | "edit">("list");
  const [selectedKey, setSelectedKey] = useState<string>("");
  const [form, setForm] = useState<AgentFormState>(emptyAgentForm());
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [utilityModalOpen, setUtilityModalOpen] = useState(false);
  const [utilityForm, setUtilityForm] = useState<UtilityAIFormState>({
    provider: "",
    model: "",
    thinking: "off",
  });
  const [utilityError, setUtilityError] = useState<string | null>(null);

  useEffect(() => {
    if (profiles.length === 0) {
      // If there are literally no profiles, default to new
      setSelectedKey(NEW_AGENT_KEY);
      setForm(emptyAgentForm());
      return;
    }
    const hasSelected =
      selectedKey !== "" &&
      selectedKey !== NEW_AGENT_KEY &&
      profiles.some((profile) => profile.name === selectedKey);
    if (hasSelected) {
      return;
    }
    const nextSelected = profiles.some(
      (profile) => profile.name === activePrimary,
    )
      ? activePrimary
      : profiles[0].name;
    setSelectedKey(nextSelected);
  }, [activePrimary, profiles, selectedKey]);

  const selectedProfile = useMemo(
    () =>
      selectedKey && selectedKey !== NEW_AGENT_KEY
        ? (profiles.find((profile) => profile.name === selectedKey) ?? null)
        : null,
    [profiles, selectedKey],
  );
  const selectedToolContractName =
    viewMode === "edit" && selectedKey !== NEW_AGENT_KEY
      ? selectedProfile?.name.trim() || ""
      : "";
  const {
    data: toolContractRuntime,
    isFetching: toolContractFetching,
    error: toolContractError,
  } = useQuery(agentToolContractQueryOptions(selectedToolContractName));
  const toolPresetOptions = useMemo(
    () =>
      mergedPresetOptions(
        toolContractRuntime,
        toolContractRuntime?.toolInventory?.presets ?? agentState?.toolInventory?.presets,
        form.toolContractPreset,
      ),
    [agentState?.toolInventory?.presets, form.toolContractPreset, toolContractRuntime],
  );
  const activeToolPreset =
    toolPresetOptions.find(
      (preset) => preset.id === form.toolContractPreset.trim(),
    ) ??
    agentToolPresetByID(form.toolContractPreset) ??
    CUSTOM_AGENT_TOOL_PRESET;
  const runtimeToolNames = useMemo(() => {
    const names = new Set<string>();
    for (const name of Object.keys(toolContractRuntime?.resolved?.tools ?? {})) {
      if (name.trim()) names.add(name.trim());
    }
    for (const tool of toolContractRuntime?.toolInventory?.tools ?? agentState?.toolInventory?.tools ?? []) {
      const toolName = (tool.contractName || tool.name).trim();
      if (toolName) names.add(toolName);
    }
    return Array.from(names).sort((left, right) => left.localeCompare(right));
  }, [agentState?.toolInventory?.tools, toolContractRuntime]);
  const effectiveToolContractAccess = useMemo(
    () => effectiveToolAccess(activeToolPreset, form.toolContractTools, runtimeToolNames),
    [activeToolPreset, form.toolContractTools, runtimeToolNames],
  );
  const runtimeToolAccess = useMemo(
    () => resolvedToolAccess(toolContractRuntime),
    [toolContractRuntime],
  );
  const showRuntimeToolAccess =
    Boolean(runtimeToolAccess) && formMatchesSavedToolContract(form, selectedProfile);
  const displayedRuntimeMode = normalizeExecutionMode(form);
  const rawDisplayedToolContractAccess =
    showRuntimeToolAccess && runtimeToolAccess
      ? runtimeToolAccess
      : effectiveToolContractAccess;
  const displayedToolContractAccess = toolAccessForExecutionMode(
    rawDisplayedToolContractAccess,
    displayedRuntimeMode,
  );

  const setExecutionMode = (nextMode: AgentFormState["runtimeMode"]) => {
    setForm((current) => {
      const currentPreset =
        toolPresetOptions.find((option) => option.id === current.toolContractPreset) ??
        agentToolPresetByID(current.toolContractPreset) ??
        CUSTOM_AGENT_TOOL_PRESET;
      const currentAccess = effectiveToolAccess(
        currentPreset,
        current.toolContractTools,
        runtimeToolNames,
      );
      const nextAccess = toolAccessForExecutionModeChange(
        currentAccess,
        nextMode,
        runtimeToolNames,
      );
      return {
        ...current,
        runtimeMode: nextMode,
        executionSetting: nextMode === "plan_auto" ? "" : nextMode,
        exitPlanModeEnabled: nextMode === "plan_auto",
        toolContractPreset: CUSTOM_AGENT_TOOL_PRESET_ID,
        toolContractTools: customToolsFromAccess(nextAccess),
      };
    });
  };

  useEffect(() => {
    if (selectedKey === NEW_AGENT_KEY) {
      setForm(emptyAgentForm());
      return;
    }
    setForm(profileToForm(selectedProfile));
  }, [selectedKey, selectedProfile]);

  const providerOptions = useMemo(() => {
    const values = new Set<string>();
    for (const option of modelOptions) {
      if (option.provider.trim() !== "") {
        values.add(option.provider.trim());
      }
    }
    if (selectedProfile?.provider?.trim()) {
      values.add(selectedProfile.provider.trim());
    }
    return Array.from(values).sort((left, right) => left.localeCompare(right));
  }, [modelOptions, selectedProfile?.provider]);

  const modelChoices = useMemo(() => {
    if (!form.provider.trim()) {
      return [];
    }
    const values = new Set<string>();
    for (const option of modelOptions) {
      if (option.provider === form.provider && option.model.trim() !== "") {
        values.add(option.model.trim());
      }
    }
    if (
      selectedProfile?.provider === form.provider &&
      selectedProfile.model.trim() !== ""
    ) {
      values.add(selectedProfile.model.trim());
    }
    return Array.from(values).sort((left, right) => left.localeCompare(right));
  }, [
    form.provider,
    modelOptions,
    selectedProfile?.model,
    selectedProfile?.provider,
  ]);

  useEffect(() => {
    if (!form.provider.trim() && form.model !== "") {
      setForm((current) => ({ ...current, model: "" }));
      return;
    }
    if (
      form.model &&
      modelChoices.length > 0 &&
      !modelChoices.includes(form.model)
    ) {
      setForm((current) => ({ ...current, model: "" }));
    }
  }, [form.model, form.provider, modelChoices]);

  const singleServiceTierOptions = serviceTierOptionsForModel(form.provider, form.model, modelOptions);
  const singleServiceTierSupported = singleServiceTierOptions.length > 1;
  const currentSingleServiceTier = singleServiceTierSupported ? normalizeModelServiceTier(form.provider, form.autoServiceTier) : "";

  const agentStateQueryKey = agentSettingsStateQueryOptions().queryKey;
  const agentStateSummaryQueryKey = agentStateQueryOptions().queryKey;

  const applyAgentState = (nextState: AgentStateRecord) => {
    queryClient.setQueryData(agentStateQueryKey, nextState);
    queryClient.setQueryData(agentStateSummaryQueryKey, nextState);
    void queryClient.invalidateQueries({ queryKey: agentStateSummaryQueryKey, refetchType: "inactive" });
    return nextState;
  };

  const refreshAgents = async () => {
    const nextState = await refreshAgentModelMutationCaches(queryClient);
    return applyAgentState(nextState);
  };

  const handleSelectProfile = (name: string) => {
    setSelectedKey(name);
    setStatus(null);
    setError(null);
    setViewMode("edit");
  };

  const handleCreateNew = () => {
    setSelectedKey(NEW_AGENT_KEY);
    setStatus(null);
    setError(null);
    setViewMode("edit");
  };

  const handleBackToList = () => {
    setViewMode("list");
    setStatus(null);
    setError(null);
  };

  const setToolContractPreset = (presetID: string) => {
    setForm((current) => {
      const preset =
        toolPresetOptions.find((option) => option.id === presetID) ??
        agentToolPresetByID(presetID) ??
        CUSTOM_AGENT_TOOL_PRESET;
      if (preset.id === CUSTOM_AGENT_TOOL_PRESET_ID) {
        const currentAccess = effectiveToolAccess(
          activeToolPreset,
          current.toolContractTools,
          runtimeToolNames,
        );
        const nextRuntimeMode = normalizeExecutionMode(current);
        return {
          ...current,
          runtimeMode: nextRuntimeMode,
          executionSetting: nextRuntimeMode === "plan_auto" ? "" : nextRuntimeMode,
          exitPlanModeEnabled: nextRuntimeMode === "plan_auto",
          toolContractPreset: CUSTOM_AGENT_TOOL_PRESET_ID,
          toolContractTools: customToolsFromAccess(currentAccess),
        };
      }
      const presetAccess = toolAccessForPreset(preset);
      const nextRuntimeMode = runtimeModeForToolAccess(presetAccess);
      return {
        ...current,
        runtimeMode: nextRuntimeMode,
        executionSetting: nextRuntimeMode,
        exitPlanModeEnabled: false,
        toolContractPreset: preset.id,
        toolContractTools: {},
      };
    });
  };

  const setToolAccess = (toolName: string, enabled: boolean) => {
    const normalized = toolName.trim();
    if (!normalized) {
      return;
    }
    setForm((current) => {
      const currentPreset =
        toolPresetOptions.find((option) => option.id === current.toolContractPreset) ??
        agentToolPresetByID(current.toolContractPreset) ??
        CUSTOM_AGENT_TOOL_PRESET;
      const nextAccess = effectiveToolAccess(
        currentPreset,
        current.toolContractTools,
        runtimeToolNames,
      );
      nextAccess.allowed = nextAccess.allowed.filter((name) => name !== normalized);
      nextAccess.blocked = nextAccess.blocked.filter((name) => name !== normalized);
      if (enabled) {
        nextAccess.allowed.push(normalized);
      } else {
        nextAccess.blocked.push(normalized);
      }
      nextAccess.allowed = sortedUnique(nextAccess.allowed);
      nextAccess.blocked = sortedUnique(nextAccess.blocked);
      const nextRuntimeMode = normalizeExecutionMode(current);
      return {
        ...current,
        runtimeMode: nextRuntimeMode,
        executionSetting: nextRuntimeMode === "plan_auto" ? "" : nextRuntimeMode,
        exitPlanModeEnabled: nextRuntimeMode === "plan_auto",
        toolContractPreset: matchedToolPresetID(nextAccess, toolPresetOptions),
        toolContractTools: customToolsFromAccess(nextAccess),
      };
    });
  };

  const handleSaveWithPrompt = async (newPrompt: string) => {
    const trimmedName = form.name.trim();
    if (!trimmedName) {
      setError("Agent name is required.");
      return;
    }
    if (!form.mode.trim()) {
      setError("Agent mode is required.");
      return;
    }
    if (
      form.modelMode === "split" &&
      (!form.planProvider.trim() ||
        !form.planModel.trim() ||
        !form.autoProvider.trim() ||
        !form.autoModel.trim())
    ) {
      setError("Split model mode requires explicit Plan and Auto provider/model selections.");
      return;
    }
    setSaving(true);
    setError(null);
    setStatus(null);
    try {
      const savedName = await upsertAgent(normalizeAgentModelFields({
        ...form,
        name: trimmedName,
        description: form.description.trim(),
        provider: form.provider.trim(),
        model: form.provider.trim() ? form.model.trim() : "",
        thinking: form.thinking.trim(),
        planProvider: form.planProvider.trim(),
        planModel: form.planProvider.trim() ? form.planModel.trim() : "",
        planThinking: form.planThinking.trim(),
        autoProvider: form.autoProvider.trim(),
        autoModel: form.autoProvider.trim() ? form.autoModel.trim() : "",
        autoThinking: form.autoThinking.trim(),
        runtimeMode: displayedRuntimeMode,
        executionSetting: displayedRuntimeMode === "plan_auto" ? "" : displayedRuntimeMode,
        prompt: newPrompt,
      }, modelOptions));
      await refreshAgents();
      setSelectedKey(savedName || trimmedName);
      setForm((current) => ({ ...current, prompt: newPrompt }));
      setStatus(`Saved prompt for agent ${savedName || trimmedName}.`);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to save agent prompt",
      );
    } finally {
      setSaving(false);
    }
  };

  const handleSave = async () => {
    const trimmedName = form.name.trim();
    if (!trimmedName) {
      setError("Agent name is required.");
      return;
    }
    if (!form.mode.trim()) {
      setError("Agent mode is required.");
      return;
    }
    if (
      form.modelMode === "split" &&
      (!form.planProvider.trim() ||
        !form.planModel.trim() ||
        !form.autoProvider.trim() ||
        !form.autoModel.trim())
    ) {
      setError("Split model mode requires explicit Plan and Auto provider/model selections.");
      return;
    }
    setSaving(true);
    setError(null);
    setStatus(null);
    try {
      const savedName = await upsertAgent(normalizeAgentModelFields({
        ...form,
        name: trimmedName,
        description: form.description.trim(),
        provider: form.provider.trim(),
        model: form.provider.trim() ? form.model.trim() : "",
        thinking: form.thinking.trim(),
        planProvider: form.planProvider.trim(),
        planModel: form.planProvider.trim() ? form.planModel.trim() : "",
        planThinking: form.planThinking.trim(),
        autoProvider: form.autoProvider.trim(),
        autoModel: form.autoProvider.trim() ? form.autoModel.trim() : "",
        autoThinking: form.autoThinking.trim(),
        runtimeMode: displayedRuntimeMode,
        executionSetting: displayedRuntimeMode === "plan_auto" ? "" : displayedRuntimeMode,
      }, modelOptions));
      await refreshAgents();
      setSelectedKey(savedName || trimmedName);
      setStatus(`Saved agent ${savedName || trimmedName}.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save agent");
    } finally {
      setSaving(false);
    }
  };

  const handleActivate = async () => {
    const targetName = selectedProfile?.name?.trim() || form.name.trim();
    if (!targetName) {
      setError("Choose a primary agent first.");
      return;
    }
    if (
      (selectedProfile?.mode || form.mode).trim().toLowerCase() !== "primary"
    ) {
      setError("Only primary agents can be activated.");
      return;
    }
    setSaving(true);
    setError(null);
    setStatus(null);
    try {
      await activatePrimaryAgent(targetName);
      await refreshAgents();
      setSelectedKey(targetName);
      setStatus(`Activated primary agent ${targetName}.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to activate agent");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    const targetName = selectedProfile?.name?.trim();
    if (!targetName) {
      setError("Choose an existing agent to delete.");
      return;
    }
    if (selectedProfile?.protected || targetName.toLowerCase() === "memory") {
      setError(
        "memory cannot be deleted because it is used for session titles.",
      );
      return;
    }
    if (
      (selectedProfile?.mode || form.mode).trim().toLowerCase() ===
        "primary" &&
      primaryAgents.length <= 1
    ) {
      setError("The last primary agent cannot be deleted.");
      return;
    }
    if (!window.confirm(`Delete agent ${targetName}?`)) {
      return;
    }
    setSaving(true);
    setError(null);
    setStatus(null);
    try {
      await deleteAgent(targetName);
      const nextState = await refreshAgents();
      applyAgentState(nextState);
      setSelectedKey("");
      setStatus(`Deleted agent ${targetName}.`);
      setViewMode("list");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete agent");
    } finally {
      setSaving(false);
    }
  };

  const handleOpenUtilityAI = () => {
    setUtilityForm(utilityAIForProfiles(profiles, providerDefaultsPreview));
    setUtilityError(null);
    setError(null);
    setStatus(null);
    setUtilityModalOpen(true);
  };

  const applyUtilityAISelection = async (overwriteExplicit: boolean) => {
    const utilityProvider = utilityForm.provider.trim();
    const utilityModel = utilityForm.model.trim();
    const utilityThinking = utilityForm.thinking.trim() || "off";
    if (!utilityProvider || !utilityModel) {
      setUtilityError("Choose a provider and model for Utility AI.");
      return;
    }
    const defaultTargets = providerDefaultsPreview?.utilityAgents ?? [];
    const baselineTargets = providerDefaultsPreview?.utilityBaselineAgents?.length
      ? providerDefaultsPreview.utilityBaselineAgents
      : providerDefaultsPreview?.customUtilityAgents?.length
        ? []
        : defaultTargets;
    const utilityAgentsLabel = overwriteExplicit
      ? displayListLabel(defaultTargets, "explorer, memory, parallel")
      : displayListLabel(
          baselineTargets,
          providerDefaultsPreview?.customUtilityAgents?.length
            ? "none"
            : "explorer, memory, parallel",
        );
    setSaving(true);
    setUtilityError(null);
    setError(null);
    setStatus(null);
    try {
      const nextState = await setUtilityAI({
        utilityProvider,
        utilityModel,
        utilityThinking,
        overwriteExplicit,
      });
      applyAgentState(nextState);
      setUtilityModalOpen(false);
      setSelectedKey(
        nextState.activePrimary || nextState.profiles[0]?.name || "",
      );
      setViewMode("list");
      setStatus(
        overwriteExplicit
          ? `Cleared Utility AI overrides and set ${utilityProvider}/${utilityModel} for ${utilityAgentsLabel}.`
          : baselineTargets.length > 0
            ? `Set Utility AI ${utilityProvider}/${utilityModel} for ${utilityAgentsLabel}; per-agent overrides stayed custom.`
            : `No blank Utility AI agents to set for ${utilityProvider}/${utilityModel}; per-agent overrides stayed custom.`,
      );
    } catch (err) {
      setUtilityError(
        err instanceof Error
          ? err.message
          : "Failed to set Utility AI for built-in agents",
      );
    } finally {
      setSaving(false);
    }
  };

  const handleSetUtilityAI = async () => {
    await applyUtilityAISelection(false);
  };

  const handleClearOverridesAndSetUtilityAI = async () => {
    await applyUtilityAISelection(true);
  };

  const handleResetDefaults = async () => {
    const confirmed = window.confirm(
      "Delete all custom agents and custom tools, then reset agent state to the built-in defaults? This cannot be undone.",
    );
    if (!confirmed) {
      return;
    }
    setSaving(true);
    setError(null);
    setStatus(null);
    try {
      const nextState = await resetAgentDefaults();
      applyAgentState(nextState);
      setSelectedKey(
        nextState.activePrimary || nextState.profiles[0]?.name || "",
      );
      setViewMode("list");
      setStatus("Reset agents and tools to defaults.");
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "Failed to reset agents to defaults",
      );
    } finally {
      setSaving(false);
    }
  };

  const selectedMode = (selectedProfile?.mode || form.mode)
    .trim()
    .toLowerCase();
  const primaryAgents = profiles.filter(
    (p) => (p.mode || "primary").toLowerCase() === "primary",
  );
  const canActivate =
    Boolean(selectedProfile?.name) &&
    selectedMode === "primary" &&
    selectedProfile?.name !== activePrimary;
  const canDelete =
    Boolean(selectedProfile?.name) &&
    !Boolean(selectedProfile?.protected) &&
    (selectedMode !== "primary" || primaryAgents.length > 1);
  const busy = saving || isFetching;

  const subAgents = profiles.filter(
    (p) => (p.mode || "primary").toLowerCase() === "subagent",
  );
  const backgroundAgents = profiles.filter(
    (p) => (p.mode || "primary").toLowerCase() === "background",
  );
  const utilityAgents = providerDefaultsPreview?.utilityAgents ?? [];
  const customUtilityAgents = providerDefaultsPreview?.customUtilityAgents ?? [];
  const baselineUtilityAgents =
    providerDefaultsPreview?.utilityBaselineAgents?.length
      ? providerDefaultsPreview.utilityBaselineAgents
      : customUtilityAgents.length > 0
        ? []
        : utilityAgents;
  const utilityAgentsLabel = displayListLabel(
    utilityAgents,
    "explorer, memory, parallel",
  );
  const customUtilityAgentsLabel = customUtilityAgents.join(", ");
  const staleInheritedTargets = providerDefaultsPreview?.staleInheritedAgents ?? [];
  const currentUtilityAI = utilityAIForProfiles(profiles, providerDefaultsPreview);
  const allUtilityAgentsHaveOverrides =
    customUtilityAgents.length > 0 && baselineUtilityAgents.length === 0;
  const utilityLabel = allUtilityAgentsHaveOverrides
    ? "not set (all utility agents have overrides)"
    : currentUtilityAI.provider && currentUtilityAI.model
      ? `${currentUtilityAI.provider}/${currentUtilityAI.model}`
      : "not set";
  const utilitySummary = `Set Utility AI fills blank/inheriting utility agents (${displayListLabel(
    baselineUtilityAgents,
    customUtilityAgents.length > 0 ? "none" : utilityAgentsLabel,
  )}) while preserving overrides. Use Clear overrides + set Utility AI in the picker to move custom utility agents back to the baseline.`;

  if (viewMode === "list") {
    return (
      <>
      <div className="flex h-full flex-col">
        <div className="mb-6 space-y-3">
          <div>
            <h1 className="text-xl font-semibold text-[var(--app-text)]">
              Agents
            </h1>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">
              Manage desktop and TUI agent profiles.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={handleCreateNew}
              disabled={busy}
              className={actionButtonClassName("primary")}
            >
              <Plus size={16} />
              New agent
            </button>
            <button
              type="button"
              onClick={handleOpenUtilityAI}
              disabled={busy}
              className={actionButtonClassName("secondary")}
              title={utilitySummary}
            >
              <Settings2 size={16} />
              Set Utility AI
            </button>
            <button
              type="button"
              onClick={() => void handleResetDefaults()}
              disabled={busy}
              className={actionButtonClassName("danger")}
            >
              <Trash2 size={16} />
              Delete all & reset
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto pb-12 pr-2">
          {error ? (
            <div className="mb-6 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">
              {error}
            </div>
          ) : null}
          {status ? (
            <div className="mb-6 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">
              {status}
            </div>
          ) : null}
          <div className="mb-6 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
            <div className="font-medium text-[var(--app-text)]">
              Utility AI for built-in agents: {utilityLabel}
            </div>
            <div className="mt-1">{utilitySummary}</div>
            {providerDefaultsPreview ? (
              <div className="mt-1">
                Primary swarm default: {providerDefaultsPreview.provider}/
                {providerDefaultsPreview.primaryModel}. Utility baseline covers{" "}
                {displayListLabel(
                  baselineUtilityAgents,
                  customUtilityAgents.length > 0 ? "none" : utilityAgentsLabel,
                )}.
              </div>
            ) : null}
            {customUtilityAgents.length > 0 ? (
              <div className="mt-1">
                Per-agent overrides preserved: {customUtilityAgentsLabel}. Open Set
                Utility AI and choose Clear overrides + set Utility AI to move them
                back to the shared baseline.
              </div>
            ) : null}
            {staleInheritedTargets.length > 0 ? (
              <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-[var(--app-warning)]">
                Inherited Utility AI fallback: {staleInheritedTargets.join(", ")}. Set
                Utility AI fills only these blank utility agents; Clear overrides + set
                Utility AI also resets custom utility-agent models.
              </div>
            ) : null}
          </div>

          <div className="flex flex-col gap-8">
            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">
                Primary Agents
              </h3>
              <div className="flex flex-col gap-3">
                {primaryAgents.map((profile) => {
                  const isActive = profile.name === activePrimary;
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 flex w-full items-center justify-between gap-2">
                        <span className="truncate font-semibold text-[var(--app-text)]">
                          {profile.name}
                        </span>
                        {isActive && (
                          <span
                            className="h-2 w-2 shrink-0 rounded-full bg-[var(--app-success)]"
                            title="Active Primary"
                          />
                        )}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {agentProviderModelSummary(profile)}
                      </span>
                      <span className="mt-1 w-full truncate text-[11px] text-[var(--app-text-muted)] opacity-80">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            </div>

            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">
                Subagents
              </h3>
              <div className="flex flex-col gap-3">
                {subAgents.map((profile) => {
                  const utilityTagged = isUtilityAgent(profile.name, utilityAgents);
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 flex w-full items-center justify-between gap-2">
                        <span className="truncate font-semibold text-[var(--app-text)]">
                          {profile.name}
                        </span>
                        {utilityTagged ? (
                          <span className="shrink-0 rounded-full border border-[var(--app-primary)]/30 bg-[var(--app-primary)]/10 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wide text-[var(--app-primary)]">
                            Utility AI
                          </span>
                        ) : null}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {utilityTagged
                          ? agentProviderModelSummary(profile, utilityLabel)
                          : agentProviderModelSummary(profile)}
                      </span>
                      <span className="mt-1 w-full truncate text-[11px] text-[var(--app-text-muted)] opacity-80">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            </div>

            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">
                Background Agents
              </h3>
              <div className="flex flex-col gap-3">
                {backgroundAgents.map((profile) => {
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 w-full truncate font-semibold text-[var(--app-text)]">
                        {profile.name}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {agentProviderModelSummary(profile)}
                      </span>
                      <span className="mt-1 w-full truncate text-[11px] text-[var(--app-text-muted)] opacity-80">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            </div>
          </div>
        </div>
      </div>
      <UtilityAISettingsModal
        open={utilityModalOpen}
        value={utilityForm}
        options={modelOptions}
        utilityAgents={utilityAgents}
        customUtilityAgents={customUtilityAgents}
        baselineUtilityAgents={baselineUtilityAgents}
        busy={busy}
        error={utilityError}
        onChange={setUtilityForm}
        onClose={() => {
          if (!busy) {
            setUtilityModalOpen(false);
            setUtilityError(null);
          }
        }}
        onApply={handleSetUtilityAI}
        onClearOverrides={handleClearOverridesAndSetUtilityAI}
      />
      </>
    );
  }

  // Edit View
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-3">
        <button
          onClick={handleBackToList}
          className="flex items-center gap-2 text-sm font-medium text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)]"
        >
          <span>←</span> Back to Agents
        </button>
        <div className="flex flex-wrap items-center gap-4 text-sm text-[var(--app-text-muted)]">
          <div>
            <span className="font-medium text-[var(--app-text)]">
              Active primary:
            </span>{" "}
            {activePrimary}
          </div>
          <div>
            <span className="font-medium text-[var(--app-text)]">Status:</span>{" "}
            {busy ? "Refreshing…" : isLoading ? "Loading…" : "Ready"}
          </div>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto pb-12 pr-2">
        {error ? (
          <div className="mb-6 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">
            {error}
          </div>
        ) : null}
        {status ? (
          <div className="mb-6 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">
            {status}
          </div>
        ) : null}
        <div className="mb-6 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
          memory cannot be deleted because it is used for session titles. Use Set
          Utility AI to fill blank built-in utility agents, then fine-tune each agent if needed
          {utilityAgents.length > 0 ? `: ${utilityAgents.join(", ")}.` : "."}{" "}
          Custom agents are not changed.
        </div>

        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] shadow-sm">
          <div className="space-y-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 rounded-t-xl">
            <h4 className="text-sm font-semibold text-[var(--app-text)]">
              {selectedProfile ? `Edit ${selectedProfile.name}` : "New agent"}
            </h4>
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() => void handleActivate()}
                disabled={!canActivate || busy}
                className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface-elevated)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition-colors hover:bg-[var(--app-surface-hover)] disabled:opacity-50 disabled:cursor-not-allowed"
              >
                Make active primary
              </button>
              {canDelete && (
                <button
                  type="button"
                  onClick={() => void handleDelete()}
                  disabled={!canDelete || busy}
                  className="rounded-md border border-transparent bg-[var(--app-danger)]/10 px-3 py-1.5 text-xs font-medium text-[var(--app-danger)] transition-colors hover:bg-[var(--app-danger)]/20 disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  Delete
                </button>
              )}
              <button
                type="button"
                onClick={() => void handleSave()}
                disabled={busy}
                className="rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-medium text-[var(--app-primary)] transition-colors hover:bg-[var(--app-surface-subtle)] disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {saving ? "Saving…" : "Save agent"}
              </button>
            </div>
          </div>

          <div className="p-0">
            <div className="border-b border-[var(--app-border)]">
              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  Name
                </label>
                <input
                  type="text"
                  value={form.name}
                  onChange={(event: ChangeEvent<HTMLInputElement>) =>
                    setForm((current) => ({
                      ...current,
                      name: event.target.value,
                    }))
                  }
                  disabled={busy}
                  placeholder="agent-name"
                  autoComplete="off"
                  className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 placeholder:text-[var(--app-text-muted)]"
                />
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  Description
                </label>
                <input
                  type="text"
                  value={form.description}
                  onChange={(event: ChangeEvent<HTMLInputElement>) =>
                    setForm((current) => ({
                      ...current,
                      description: event.target.value,
                    }))
                  }
                  disabled={busy}
                  placeholder="What this agent is for"
                  autoComplete="off"
                  className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 placeholder:text-[var(--app-text-muted)]"
                />
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  Mode
                </label>
                <div className="relative w-full">
                  <select
                    value={form.mode}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                      setForm((current) => ({
                        ...current,
                        mode: event.target.value,
                      }))
                    }
                    disabled={busy}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    <option value="primary">Primary</option>
                    <option value="subagent">Subagent</option>
                    <option value="background">Background</option>
                  </select>
                  <ChevronDown
                    size={14}
                    className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                  />
                </div>
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  Model mode
                </label>
                <div className="relative w-full">
                  <select
                    value={form.modelMode}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                      const modelMode = event.target.value === "split" ? "split" : "single";
                      setForm((current) => ({ ...current, modelMode }));
                    }}
                    disabled={busy}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    <option value="single">Single model (default or selected)</option>
                    <option value="split">Split plan/auto models</option>
                  </select>
                  <ChevronDown
                    size={14}
                    className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                  />
                </div>
              </div>

              {form.modelMode === "split" ? (
                <div className="border-b border-[var(--app-border)] px-4 py-4 text-sm text-[var(--app-text)]">
                  <div className="mb-4">
                    <div className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Plan/auto split</div>
                    <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">
                      Plan mode runs on the plan model. Exiting plan mode continues on the auto model. Leave either provider/model on Default to inherit the current chat default.
                    </p>
                  </div>
                  <div className="grid gap-4 xl:grid-cols-2">
                    <SplitModelSection
                      title="Plan"
                      description="Used while the agent is drafting a plan or waiting for plan approval."
                      prefix="plan"
                      form={form}
                      providerOptions={providerOptions}
                      modelOptions={modelOptions}
                      selectedProfile={selectedProfile}
                      currentDefaultModelLabel={currentDefaultModelLabel}
                      busy={busy}
                      setForm={setForm}
                    />
                    <SplitModelSection
                      title="Auto"
                      description="Used after plan approval and for direct auto execution."
                      prefix="auto"
                      form={form}
                      providerOptions={providerOptions}
                      modelOptions={modelOptions}
                      selectedProfile={selectedProfile}
                      currentDefaultModelLabel={currentDefaultModelLabel}
                      busy={busy}
                      setForm={setForm}
                    />
                  </div>
                </div>
              ) : (
                <>
                  <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                    <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Provider
                    </label>
                    <div className="relative w-full">
                      <select
                        value={form.provider}
                        onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                          const provider = event.target.value;
                          setForm((current) => ({
                            ...current,
                            provider,
                            model:
                              provider === current.provider ? current.model : "",
                            autoServiceTier:
                              provider === current.provider ? current.autoServiceTier : "",
                          }));
                        }}
                        disabled={busy}
                        className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                      >
                        <option value="">Default</option>
                        {providerOptions.map((provider) => (
                          <option key={provider} value={provider}>
                            {provider}
                          </option>
                        ))}
                      </select>
                      <ChevronDown
                        size={14}
                        className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                      />
                    </div>
                  </div>

                  <div className="border-b border-[var(--app-border)] px-4 py-3">
                    <div className="flex items-center">
                      <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                        Model
                      </label>
                      <div className="relative w-full">
                        <select
                          value={form.model}
                          onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                            setForm((current) => ({
                              ...current,
                              model: event.target.value,
                              autoServiceTier: modelSupportsServiceTierSetting(current.provider, event.target.value, modelOptions, current.autoServiceTier)
                                ? current.autoServiceTier
                                : "",
                            }))
                          }
                          disabled={busy || !form.provider.trim()}
                          className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                        >
                          <option value="">Default</option>
                          {modelChoices.map((model) => (
                            <option key={model} value={model}>
                              {model}
                            </option>
                          ))}
                        </select>
                        <ChevronDown
                          size={14}
                          className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                        />
                      </div>
                    </div>
                    <p className="mt-2 pl-[25%] text-xs leading-5 text-[var(--app-text-muted)]">
                      In the default state, you can freely change your settings and new chats will continue with your settings for agents with "Default" settings. Current default chat model: {currentDefaultModelLabel}. Choose a provider/model here to lock the agent to that preset.
                    </p>
                  </div>

                  <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                    <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Thinking
                    </label>
                    <div className="relative w-full">
                      <select
                        value={form.thinking}
                        onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                          setForm((current) => ({
                            ...current,
                            thinking: event.target.value,
                          }))
                        }
                        disabled={busy}
                        className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                      >
                        {THINKING_OPTIONS.map((option) => (
                          <option key={option.label} value={option.value}>
                            {option.label}
                          </option>
                        ))}
                      </select>
                      <ChevronDown
                        size={14}
                        className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                      />
                    </div>
                  </div>

                  <div className="flex items-center px-4 py-3">
                    <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Service tier
                    </label>
                    <div className="relative w-full">
                      <select
                        value={currentSingleServiceTier}
                        onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                          setForm((current) => ({
                            ...current,
                            autoServiceTier: event.target.value,
                          }))
                        }
                        disabled={busy || !singleServiceTierSupported}
                        className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                      >
                        {singleServiceTierOptions.map((option) => (
                          <option key={option.label} value={option.value}>
                            {option.label}
                          </option>
                        ))}
                      </select>
                      <ChevronDown
                        size={14}
                        className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                      />
                    </div>
                  </div>
                </>
              )}

              <div className="flex items-start border-t border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 pt-2 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  Execution mode
                </label>
                <div className="min-w-0 flex-1 space-y-2 text-sm text-[var(--app-text)]">
                  <div className="relative">
                    <select
                      value={displayedRuntimeMode || "readwrite"}
                      onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                        const nextMode = event.target.value as AgentFormState["runtimeMode"];
                        setExecutionMode(nextMode);
                      }}
                      disabled={busy}
                      className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                    >
                      <option value="plan_auto">Plan approval</option>
                      <option value="read">Direct read-only</option>
                      <option value="readwrite">Direct read/write</option>
                    </select>
                    <ChevronDown
                      size={14}
                      className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                    />
                  </div>
                  <div className="font-medium">
                    Saving as {runtimeModeLabel(displayedRuntimeMode)} with a Custom tool contract
                  </div>
                  <p className="text-xs leading-5 text-[var(--app-text-muted)]">
                    Plan approval saves <code>runtime_mode=plan_auto</code> and enables
                    <code> exit_plan_mode</code>, so the agent starts in plan mode and
                    asks before switching to auto execution. Direct modes save
                    <code> runtime_mode=read</code> or <code>runtime_mode=readwrite</code>;
                    <code> plan_manage</code> can still be used, but
                    <code> exit_plan_mode</code> is disabled.
                  </p>
                  <p className="text-xs leading-5 text-[var(--app-text-muted)]">
                    Changing the tool preset chooses that preset’s suggested Direct
                    mode. Changing Execution mode converts the tool contract to Custom
                    so the selected runtime flags are saved exactly.
                  </p>
                </div>
              </div>

              <div className="border-t border-[var(--app-border)] px-4 py-4">
                <div className="mb-3 flex items-center gap-2 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  <Settings2 size={14} /> Tool contract
                </div>
                <p className="mb-4 text-xs text-[var(--app-text-muted)]">
                  Tool presets are suggestions for allowed tools. Switching Execution
                  mode saves a Custom tool contract so runtime_mode and
                  exit_plan_mode_enabled stay authoritative.
                </p>
                <div className="space-y-3">
                  <div className="flex items-start gap-3">
                    <label className="w-1/4 shrink-0 pt-2 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Preset
                    </label>
                    <div className="min-w-0 flex-1 space-y-2">
                      <div className="relative">
                        <select
                          value={form.toolContractPreset || CUSTOM_AGENT_TOOL_PRESET_ID}
                          onChange={(event: ChangeEvent<HTMLSelectElement>) =>
                            setToolContractPreset(event.target.value)
                          }
                          disabled={busy}
                          className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50"
                        >
                          <option value="">Choose a preset</option>
                          {toolPresetOptions.map((preset) => (
                            <option key={preset.id} value={preset.id}>
                              {presetLabel(preset)}
                            </option>
                          ))}
                        </select>
                        <ChevronDown
                          size={14}
                          className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]"
                        />
                      </div>
                      <div className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text-muted)]">
                        <div className="font-semibold text-[var(--app-text)]">
                          {presetLabel(activeToolPreset)}
                        </div>
                        {activeToolPreset.description ? (
                          <div className="mt-1">{activeToolPreset.description}</div>
                        ) : null}
                        {activeToolPreset.bashPrefixes.length > 0 ? (
                          <div className="mt-2 text-[var(--app-warning)]">
                            Bash prefixes: {activeToolPreset.bashPrefixes.join(", ")}
                          </div>
                        ) : null}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-start gap-3">
                    <div className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Runtime
                    </div>
                    <div className="min-w-0 flex-1 text-xs text-[var(--app-text-muted)]">
                      {toolContractFetching
                        ? "Loading resolved runtime contract…"
                        : toolContractError
                          ? `Failed to load runtime contract: ${toolContractError instanceof Error ? toolContractError.message : "unknown error"}`
                          : toolContractRuntime?.resolved
                            ? `${toolContractRuntime.resolved.availableTools.length} enabled, ${toolContractRuntime.resolved.unavailableTools.length} disabled. Runtime mode: ${toolContractRuntime.resolved.runtimeMode || "unset"}.`
                            : selectedToolContractName
                              ? "No runtime contract returned."
                              : "Save the agent before runtime resolution is available."}
                    </div>
                  </div>
                  <div className="flex items-start gap-3">
                    <div className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                      Tools
                    </div>
                    <div className="min-w-0 flex-1 space-y-2">
                      <ToolAccessRow
                        label="Allowed"
                        count={displayedToolContractAccess.allowed.length}
                        items={displayedToolContractAccess.allowed}
                        emptyText="No tools are allowed"
                        tone="allow"
                        onItemClick={(item) => setToolAccess(item, false)}
                        disabled={busy}
                        itemTitle="Click to block this tool"
                      />
                      <ToolAccessRow
                        label="Blocked"
                        count={displayedToolContractAccess.blocked.length}
                        items={displayedToolContractAccess.blocked}
                        emptyText="No tools are blocked"
                        tone="block"
                        onItemClick={(item) => setToolAccess(item, true)}
                        disabled={busy}
                        itemTitle="Click to allow this tool"
                      />
                      <div className="text-xs text-[var(--app-text-muted)]">
                        {showRuntimeToolAccess
                          ? "Showing the resolved runtime tool set for this saved agent. Click a chip to customize and save as Custom."
                          : "Click a tool chip to move it between allowed and blocked. Any combination that does not match a preset is saved as Custom."}
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div className="flex flex-col gap-3 px-4 py-4 transition-colors">
              <label className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                System Prompt
              </label>
              <PromptEditor
                form={form}
                onSavePrompt={handleSaveWithPrompt}
                busy={busy}
                disabled={busy}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
