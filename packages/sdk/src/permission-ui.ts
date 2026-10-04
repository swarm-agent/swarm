import type { PendingPermissionRecord, ResolvePermissionResult } from './permissions.js';

export type PermissionAction = 'allow_once' | 'deny' | 'deny_once' | 'allow_always' | 'deny_always';
export interface ResolvePermissionOptions {
  reason?: string;
  approvedArguments?: Record<string, unknown> | string;
}
export type PermissionResolver = (action: PermissionAction, options?: ResolvePermissionOptions | string) => Promise<ResolvePermissionResult>;
export interface PermissionChoice { label: string; value: string; description?: string; custom: boolean }
export interface PermissionQuestion { id: string; question: string; header?: string; required: boolean; options: PermissionChoice[] }
export interface PermissionRequestView {
  id: string;
  sessionId: string;
  toolName: string;
  status: string;
  kind: 'tool' | 'ask-user';
  arguments: Record<string, unknown> | null;
  rawArguments: string;
  parseError?: string;
  title?: string;
  context?: string;
  structured: boolean;
  questions: PermissionQuestion[];
  /** Conservative per-request actions, not a grant of backend authority. */
  actions: Array<'allow_once' | 'deny'>;
}

export function permissionId(value: unknown, label = 'permissionId'): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label} must be a non-empty string`);
  return value.trim();
}
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
function text(value: unknown): string { return typeof value === 'string' ? value.trim() : ''; }
export function isAskUserPermission(permission: Pick<PendingPermissionRecord, 'tool_name'>): boolean {
  if (typeof permission?.tool_name !== 'string') return false;
  const name = permission.tool_name.trim().toLowerCase().split('.').at(-1)?.replaceAll('-', '_');
  return name === 'ask_user' || name === 'askuser';
}

/** Parse untrusted request arguments for text rendering. Never render these fields as HTML. */
export function describePermission(permission: PendingPermissionRecord): PermissionRequestView {
  const id = permissionId(permission?.id);
  const sessionId = permissionId(permission?.session_id, 'sessionId');
  const raw = permission.tool_arguments ?? permission.tool_call_arguments ?? '';
  const view: PermissionRequestView = {
    id, sessionId, toolName: permission.tool_name, status: permission.status,
    kind: isAskUserPermission(permission) ? 'ask-user' : 'tool', arguments: null,
    rawArguments: typeof raw === 'string' ? raw : '', structured: false, questions: [],
    actions: permission.status === 'pending' ? ['allow_once', 'deny'] : [],
  };
  try {
    const args: unknown = raw === '' ? {} : JSON.parse(raw);
    if (!object(args)) throw new Error('Permission arguments must be a JSON object');
    view.arguments = args;
    if (view.kind === 'tool') return view;
    view.title = text(args.title) || undefined;
    view.context = text(args.context) || undefined;
    view.structured = Object.hasOwn(args, 'questions');
    const questions = view.structured ? args.questions : [args];
    if (!Array.isArray(questions) || questions.length === 0) throw new Error('Ask-user requires questions');
    const ids = new Set<string>();
    view.questions = questions.map((q: unknown, index): PermissionQuestion => {
      if (!object(q) || !text(q.question) || !Array.isArray(q.options)) throw new Error('Invalid ask-user question');
      const id = text(q.id) || `q_${index + 1}`;
      if (ids.has(id)) throw new Error('Duplicate ask-user question ID');
      ids.add(id);
      const options = q.options.map((item: unknown): PermissionChoice => {
        if (typeof item === 'string' && item.trim()) return { label: item.trim(), value: item.trim(), custom: item === '__custom__' || item === 'Custom response' };
        if (!object(item)) throw new Error('Invalid ask-user option');
        const value = text(item.value) || text(item.label);
        if (!value) throw new Error('Empty ask-user option');
        return { value, label: text(item.label) || value, description: text(item.description) || undefined,
          custom: item.allow_custom === true || item.allowCustom === true || value === '__custom__' || value === 'Custom response' };
      });
      if (options.filter(option => !option.custom).length < 2) throw new Error('Ask-user requires at least two concrete options');
      return { id, question: text(q.question), header: text(q.header) || undefined, required: q.required !== false, options };
    });
  } catch (error) {
    view.parseError = error instanceof Error ? error.message : String(error);
    view.questions = [];
    // A malformed question may be denied, but never silently approved without an answer.
    if (view.kind === 'ask-user' && permission.status === 'pending') view.actions = ['deny'];
  }
  return view;
}

/** Serialize actual user input using the backend's reason/answers-map contract. */
export function answerPermission(permission: PendingPermissionRecord, answer: string | Record<string, string>): ResolvePermissionOptions {
  const view = describePermission(permission);
  if (view.status !== 'pending' || view.kind !== 'ask-user' || view.parseError) throw new Error(view.parseError || 'Expected a pending ask-user request');
  const validate = (question: PermissionQuestion, value: unknown): string => {
    const result = text(value);
    if (!result) throw new Error(`Answer required for ${question.id}`);
    if (result === '__custom__') throw new Error('Enter custom response text, not the custom option marker');
    if (!question.options.some(option => !option.custom && (option.value === result || option.label === result)) && !question.options.some(option => option.custom)) {
      throw new Error(`Select a supported answer for ${question.id}`);
    }
    return result;
  };
  if (!view.structured) {
    if (typeof answer !== 'string') throw new Error('Single question requires a string answer');
    return { reason: validate(view.questions[0], answer) };
  }
  if (!object(answer)) throw new Error('Structured questions require an answers map keyed by question ID');
  const answers: Record<string, string> = Object.create(null);
  for (const key of Object.keys(answer)) if (!view.questions.some(q => q.id === key)) throw new Error(`Unknown question ID: ${key}`);
  for (const question of view.questions) {
    if (!Object.hasOwn(answer, question.id) && !question.required) continue;
    answers[question.id] = validate(question, answer[question.id]);
  }
  if (!Object.keys(answers).length) throw new Error('Provide at least one explicit answer');
  return { reason: JSON.stringify({ answers }) };
}

/** Validate JavaScript callers before any mutation is sent. */
export function permissionResolutionBody(action: PermissionAction, options: ResolvePermissionOptions = {}): Record<string, unknown> {
  if (!['allow_once', 'deny', 'deny_once', 'allow_always', 'deny_always'].includes(action)) throw new Error('Unsupported permission action');
  if (!object(options)) throw new Error('Permission resolution options must be an object');
  if (options.reason !== undefined && typeof options.reason !== 'string') throw new Error('Permission reason must be a string');
  const body: Record<string, unknown> = { action, reason: options.reason ?? '' };
  if (options.approvedArguments !== undefined) {
    let args: unknown = options.approvedArguments;
    if (typeof args === 'string') {
      try { args = JSON.parse(args); } catch { throw new Error('approvedArguments must contain valid JSON'); }
    }
    if (!object(args)) throw new Error('approvedArguments must be a JSON object');
    // Reject circular or otherwise non-JSON data before transport.
    body.approved_arguments = JSON.parse(JSON.stringify(args));
  }
  return body;
}

export class PermissionResolutionError extends Error {
  constructor(message: string, public readonly result?: ResolvePermissionResult) {
    super(message); this.name = 'PermissionResolutionError';
  }
}
/** HTTP success is not proof that a stale decision won. Keep the actual record on conflicts. */
export function confirmPermissionResolution(result: ResolvePermissionResult, sessionId: string, id: string, action: PermissionAction, options: ResolvePermissionOptions = {}): ResolvePermissionResult {
  const p = result?.permission;
  const canonical = (value: unknown): string => JSON.stringify(value, (_key, item) =>
    object(item) ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item);
  let argumentsMatch = true;
  if (options.approvedArguments !== undefined) {
    try {
      const expected = typeof options.approvedArguments === 'string' ? JSON.parse(options.approvedArguments) : options.approvedArguments;
      argumentsMatch = canonical(JSON.parse(p?.approved_arguments ?? 'null')) === canonical(expected);
    } catch { argumentsMatch = false; }
  }
  if (result?.ok !== true || result.session_id !== sessionId || p?.session_id !== sessionId || p.id !== id) {
    throw new PermissionResolutionError('Invalid permission resolution response; refresh the conversation');
  }
  const normalize = (value: string | undefined) => value === 'deny_once' ? 'deny' : value;
  const expectedStatus = action.startsWith('allow') ? 'approved' : 'denied';
  if (!argumentsMatch || p.status !== expectedStatus || normalize(p.decision) !== normalize(action) ||
      (options.reason !== undefined && (p.reason ?? '').trim() !== options.reason.trim())) {
    throw new PermissionResolutionError('Permission decision was not confirmed (stale, cancelled or resolved differently); refresh before retrying', result);
  }
  return result;
}
