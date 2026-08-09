/**
 * Opt-in live scenario runner for @cursor/sdk 1.0.23.
 * Default tests inject mocks; the CLI loads the real package when opted in.
 */

import type { LocalAgentStore } from "@cursor/sdk";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";

export const PINNED_LIVE_SDK_VERSION = "1.0.23";

export const DEFAULT_SCENARIOS_TOTAL_TIMEOUT_MS = 300_000;
export const DEFAULT_SCENARIOS_PHASE_TIMEOUT_MS = 45_000;
export const DEFAULT_CLEANUP_BOUND_MS = 1_000;
export const DEFAULT_SETTLE_GRACE_MS = 2_500;

/** Prefix for bounded text-only live harness prompts (no tools / shell / workspace). */
export const LIVE_TEXT_ONLY_PREFIX = "LIP_LIVE_TEXT_ONLY:";

export const LIVE_REUSE_TOKEN_FIRST = "REUSE_TURN_1";
export const LIVE_REUSE_TOKEN_SECOND = "REUSE_TURN_2";

/** Build an exact-token text-only prompt that denies tools/shell/filesystem/commands/workspace. */
export function liveTextOnlyPrompt(exactToken: string): string {
  const token = exactToken.trim() || "OK";
  return (
    LIVE_TEXT_ONLY_PREFIX +
    ` Reply with exactly the token "${token}" as plain text only.` +
    ` Do not use tools, shell, PowerShell, filesystem, commands, or workspace actions.`
  );
}

/** True when prompt carries text-only deny semantics and the live prefix. */
export function livePromptHasTextOnlyDeny(prompt: string): boolean {
  const lower = prompt.toLowerCase();
  for (const frag of [
    "do not use tools",
    "shell",
    "filesystem",
    "commands",
    "workspace",
    "plain text",
  ]) {
    if (!lower.includes(frag)) return false;
  }
  return prompt.startsWith(LIVE_TEXT_ONLY_PREFIX);
}

export interface ScenarioModelRow {
  id?: string;
  displayName?: string;
  parameters?: Array<{ id?: string; values?: string[] }>;
}

export interface ScenarioDelta {
  type: string;
  text?: string;
  usage?: Record<string, number>;
}

export interface ScenarioStep {
  type?: string;
}

export interface ScenarioRunResult {
  status: string;
}

export interface ScenarioRun {
  id: string;
  status?: string;
  supports?(op: string): boolean;
  stream(): AsyncGenerator<unknown, void, unknown>;
  wait(): Promise<ScenarioRunResult>;
  cancel(): Promise<void>;
}

export interface ScenarioAgent {
  agentId: string;
  send(prompt: string, options?: {
    onDelta?: (args: { update: ScenarioDelta }) => void | Promise<void>;
    onStep?: (args: { step: ScenarioStep }) => void | Promise<void>;
  }): Promise<ScenarioRun>;
  close(): void;
  [Symbol.asyncDispose](): Promise<void>;
}

export interface ScenarioModelParam {
  id: string;
  value: string;
}

export interface ScenarioAgentCreateOptions {
  apiKey: string;
  model: { id: string; params?: ScenarioModelParam[] };
  local: {
    cwd: string;
    store: LocalAgentStore;
    settingSources?: readonly string[];
    sandboxOptions?: { enabled: boolean };
    autoReview?: boolean;
    enableAgentRetries?: boolean;
  };
  mcpServers?: Record<string, unknown>;
}

export interface LiveScenariosSdk {
  packageVersion: string;
  platform: string;
  Cursor: {
    models: {
      list(opts: { apiKey: string }): Promise<ScenarioModelRow[]>;
    };
  };
  Agent: {
    create(opts: ScenarioAgentCreateOptions): Promise<ScenarioAgent>;
  };
}

export interface LiveScenarioWorkspace {
  cwd: string;
  cleanup: () => Promise<void>;
}

export interface LiveScenariosEnv {
  liveEnabled: boolean;
  apiKey?: string | undefined;
  nodeVersion: string;
}

export interface LiveScenariosTimeouts {
  totalMs?: number;
  phaseMs?: number;
  cleanupBoundMs?: number;
  settleGraceMs?: number;
}

export interface LiveScenariosProcessLifecycle {
  /** Hard-kill and recover the bridge process (Go harness). */
  hardBridgeRestart?: () => Promise<string | undefined>;
  /** Prove canonical transcript rebootstrap after restart (Go harness). */
  canonicalRebootstrap?: () => Promise<string | undefined>;
}

export interface LiveScenariosDeps {
  env: LiveScenariosEnv;
  loadSdk: () => Promise<LiveScenariosSdk>;
  createStore?: () => LocalAgentStore;
  mkWorkspace: (label: string) => LiveScenarioWorkspace | Promise<LiveScenarioWorkspace>;
  /** Optional process-lifecycle hooks; absent => skipped (Go platform/live covers them). */
  processLifecycle?: LiveScenariosProcessLifecycle;
  timeouts?: LiveScenariosTimeouts;
  log?: (line: string) => void;
  logError?: (line: string) => void;
}

export type ScenarioStatus = "passed" | "skipped" | "blocked" | "failed";

export type LiveScenariosRunStatus = "complete" | "blocked" | "failed";

export interface ScenarioOutcome {
  name: string;
  status: ScenarioStatus;
  detail?: string;
}

export interface LiveScenariosSummary {
  ok: boolean;
  status: LiveScenariosRunStatus;
  sdkVersion: string;
  node: string;
  platform: string;
  scenarios: ScenarioOutcome[];
  /** Present for pre-scenario / envelope failures (loadSdk, timeout, unexpected). */
  error?: string;
}

/** Scenarios that must pass (or optional skip) for overall complete. */
const OPTIONAL_SCENARIOS = new Set<string>(["reasoning"]);

export class LiveScenariosDisabledError extends Error {
  readonly exitCode = 2;
  constructor(message: string) {
    super(message);
    this.name = "LiveScenariosDisabledError";
  }
}

export class LiveScenariosTimeoutError extends Error {
  constructor(phase: string) {
    super(`live scenarios timeout: ${phase}`);
    this.name = "LiveScenariosTimeoutError";
  }
}

/** Sanitize live-scenario errors for logs/throws (keys, apiKey, absolute paths). */
export function sanitizeLiveScenariosError(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err);
  return message
    .replace(/sk-[A-Za-z0-9_-]+/g, "[REDACTED]")
    .replace(/crsr_[A-Za-z0-9_-]+/g, "[REDACTED]")
    .replace(/apiKey["']?\s*[:=]\s*["']?[^"',\s}]+/gi, "apiKey=[REDACTED]")
    .replace(/[A-Za-z]:\\[^\s"']+/g, "[path]")
    .replace(/\/(?:home|Users|tmp|var|private)\/[^\s"']+/g, "[path]")
    .slice(0, 200);
}

function sanitizeError(err: unknown): string {
  return sanitizeLiveScenariosError(err);
}

export const DEFAULT_CLEANUP_MAX_ATTEMPTS = 4;
export const DEFAULT_CLEANUP_RETRY_MS = 40;

export type WorkspaceCleanupResult =
  | { ok: true; attempts: number }
  | { ok: false; attempts: number; detail: string };

export interface WorkspaceCleanupOptions {
  maxAttempts?: number;
  retryMs?: number;
  sleep?: (ms: number) => Promise<void>;
  /** Defaults to process.platform; inject in tests. */
  platform?: NodeJS.Platform | string;
}

function defaultSleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function cleanupErrorCode(err: unknown): string | undefined {
  if (err && typeof err === "object" && "code" in err && typeof (err as { code: unknown }).code === "string") {
    return (err as { code: string }).code;
  }
  const msg = err instanceof Error ? err.message : String(err);
  const match = /\b(EBUSY|EPERM|EACCES)\b/.exec(msg);
  return match?.[1];
}

function isRetryableCleanupError(err: unknown, platform: string): boolean {
  const code = cleanupErrorCode(err);
  if (code === "EBUSY") return true;
  if ((code === "EPERM" || code === "EACCES") && platform === "win32") return true;
  return false;
}

/** Bounded filesystem cleanup helper. Never throws; sanitizes failure detail. */
export async function runWorkspaceCleanup(
  cleanup: () => Promise<void>,
  opts?: WorkspaceCleanupOptions,
): Promise<WorkspaceCleanupResult> {
  const maxAttempts = Math.max(1, opts?.maxAttempts ?? DEFAULT_CLEANUP_MAX_ATTEMPTS);
  const retryMs = Math.max(0, opts?.retryMs ?? DEFAULT_CLEANUP_RETRY_MS);
  const sleep = opts?.sleep ?? defaultSleep;
  const platform = opts?.platform ?? process.platform;
  let lastErr: unknown;
  let attempts = 0;
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    attempts = attempt;
    try {
      await cleanup();
      return { ok: true, attempts };
    } catch (err) {
      lastErr = err;
      if (attempt < maxAttempts && isRetryableCleanupError(err, platform)) {
        await sleep(retryMs * attempt);
        continue;
      }
      break;
    }
  }
  return {
    ok: false,
    attempts,
    detail: `workspace cleanup failed: ${sanitizeError(lastErr)}`,
  };
}

async function disposeAgent(agent: ScenarioAgent | undefined): Promise<void> {
  if (!agent) return;
  try {
    await agent[Symbol.asyncDispose]();
  } catch {
    try {
      agent.close();
    } catch {
      // ignore dispose failures
    }
  }
}

async function withScenarioWorkspace(
  mkWorkspace: LiveScenariosDeps["mkWorkspace"],
  label: string,
  fn: (ws: LiveScenarioWorkspace) => Promise<string | undefined>,
): Promise<string | undefined> {
  const ws = await resolveWorkspace(mkWorkspace, label);
  let scenarioErr: unknown;
  let detail: string | undefined;
  try {
    detail = await fn(ws);
  } catch (err) {
    scenarioErr = err;
  }
  const cleaned = await runWorkspaceCleanup(() => ws.cleanup());
  if (scenarioErr !== undefined) throw scenarioErr;
  if (!cleaned.ok) throw new Error(sanitizeError(cleaned.detail));
  return detail;
}

export async function withTimeout<T>(
  phase: string,
  ms: number,
  signal: AbortSignal,
  work: (signal: AbortSignal) => Promise<T>,
  settleGraceMs: number = DEFAULT_SETTLE_GRACE_MS,
): Promise<T> {
  if (signal.aborted) throw new LiveScenariosTimeoutError(phase);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let settleTimer: ReturnType<typeof setTimeout> | undefined;
  const phaseController = new AbortController();
  const onAbort = () => phaseController.abort();
  signal.addEventListener("abort", onAbort, { once: true });
  const workPromise = work(phaseController.signal);
  const settled = workPromise.then(
    (value) => ({ tag: "ok" as const, value }),
    (error) => ({ tag: "err" as const, error }),
  );
  const timeoutPromise = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => {
      phaseController.abort();
      resolve("timeout");
    }, ms);
  });
  try {
    const raced = await Promise.race([
      settled.then(() => "settled" as const),
      timeoutPromise,
    ]);
    if (raced === "timeout") {
      await Promise.race([
        settled.then(() => undefined),
        new Promise<void>((resolve) => {
          settleTimer = setTimeout(resolve, settleGraceMs);
          // Intentionally ref'd: an unref'd settle timer lets the event loop
          // drain mid-settle, which under tsx trips Node's test-runner
          // pending-promise check and cancels tests (cancelledByParent).
        }),
      ]);
      throw new LiveScenariosTimeoutError(phase);
    }
    const outcome = await settled;
    if (outcome.tag === "ok") return outcome.value;
    throw outcome.error;
  } finally {
    if (timer) clearTimeout(timer);
    if (settleTimer) clearTimeout(settleTimer);
    signal.removeEventListener("abort", onAbort);
  }
}

function pickModel(models: ScenarioModelRow[]): ScenarioModelRow | undefined {
  return models.find((m) => typeof m.id === "string" && m.id.length > 0);
}

function pickReasoningModel(models: ScenarioModelRow[]): ScenarioModelRow | undefined {
  return models.find((m) =>
    Array.isArray(m.parameters) &&
    m.parameters.some((p) => p.id === "reasoning" && Array.isArray(p.values) && p.values.length > 0),
  );
}

function firstReasoningValue(model: ScenarioModelRow): string | undefined {
  const param = model.parameters?.find((p) => p.id === "reasoning");
  const first = param?.values?.[0];
  return typeof first === "string" && first.length > 0 ? first : undefined;
}

async function resolveWorkspace(
  mkWorkspace: LiveScenariosDeps["mkWorkspace"],
  label: string,
): Promise<LiveScenarioWorkspace> {
  return await mkWorkspace(label);
}

async function runScenario(
  name: string,
  fn: () => Promise<string | undefined>,
): Promise<ScenarioOutcome> {
  try {
    const detail = await fn();
    return { name, status: "passed", ...(detail ? { detail } : {}) };
  } catch (err) {
    const message = sanitizeError(err);
    if (message.includes("SKIP:")) {
      return { name, status: "skipped", detail: message.replace(/^SKIP:\s*/, "") };
    }
    if (message.includes("BLOCKED:")) {
      return { name, status: "blocked", detail: message.replace(/^BLOCKED:\s*/, "") };
    }
    return { name, status: "failed", detail: message };
  }
}

async function raceIteratorNext<T>(
  next: Promise<IteratorResult<T>>,
  signal: AbortSignal,
): Promise<IteratorResult<T> | "aborted"> {
  if (signal.aborted) return "aborted";
  return await new Promise<IteratorResult<T> | "aborted">((resolve, reject) => {
    const onAbort = () => resolve("aborted");
    signal.addEventListener("abort", onAbort, { once: true });
    next.then(
      (value) => {
        signal.removeEventListener("abort", onAbort);
        resolve(value);
      },
      (err) => {
        signal.removeEventListener("abort", onAbort);
        reject(err);
      },
    );
  });
}

export async function boundCall(work: () => Promise<unknown>, boundMs: number): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      Promise.resolve()
        .then(work)
        .then(
          () => undefined,
          () => undefined,
        ),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, boundMs);
        timer.unref?.();
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

async function abortRunResources(
  run: ScenarioRun,
  iter: AsyncGenerator<unknown, void, unknown>,
  boundMs: number,
): Promise<void> {
  if (!run.supports || run.supports("cancel")) {
    await boundCall(() => run.cancel(), boundMs);
  }
  if (typeof iter.return === "function") {
    await boundCall(() => iter.return(undefined), boundMs);
  }
}

export interface CompleteRunOptions {
  cleanupBoundMs?: number;
}

export async function completeRun(
  run: ScenarioRun,
  signal: AbortSignal,
  opts?: CompleteRunOptions,
): Promise<void> {
  const cleanupBoundMs = opts?.cleanupBoundMs ?? DEFAULT_CLEANUP_BOUND_MS;
  const iter = run.stream();
  let cleanupPromise: Promise<void> | undefined;
  const cleanup = (): Promise<void> => {
    if (!cleanupPromise) {
      cleanupPromise = abortRunResources(run, iter, cleanupBoundMs);
    }
    return cleanupPromise;
  };
  const onAbort = () => {
    void cleanup();
  };
  signal.addEventListener("abort", onAbort, { once: true });
  try {
    while (!signal.aborted) {
      const raced = await raceIteratorNext(iter.next(), signal);
      if (raced === "aborted") {
        await cleanup();
        throw new LiveScenariosTimeoutError("run.stream");
      }
      if (raced.done) break;
    }
    if (signal.aborted) {
      await cleanup();
      throw new LiveScenariosTimeoutError("run.stream");
    }
    await run.wait();
  } catch (err) {
    if (signal.aborted) {
      await cleanup();
      if (err instanceof LiveScenariosTimeoutError) throw err;
      throw new LiveScenariosTimeoutError("run.stream");
    }
    throw err;
  } finally {
    signal.removeEventListener("abort", onAbort);
  }
}

/** Bounded live scenarios covering Task 6.3 SDK surfaces. */
export async function runLiveScenarios(deps: LiveScenariosDeps): Promise<LiveScenariosSummary> {
  const log = deps.log ?? (() => undefined);
  const logError = deps.logError ?? (() => undefined);
  const totalMs = deps.timeouts?.totalMs ?? DEFAULT_SCENARIOS_TOTAL_TIMEOUT_MS;
  const phaseMs = deps.timeouts?.phaseMs ?? DEFAULT_SCENARIOS_PHASE_TIMEOUT_MS;
  const settleGraceMs = deps.timeouts?.settleGraceMs ?? DEFAULT_SETTLE_GRACE_MS;
  const cleanupBoundMs = deps.timeouts?.cleanupBoundMs ?? DEFAULT_CLEANUP_BOUND_MS;
  const createStore = deps.createStore ?? createBridgeInMemoryLocalAgentStore;

  if (!deps.env.liveEnabled) {
    throw new LiveScenariosDisabledError("live scenarios disabled; set CURSOR_SDK_LIVE=1 to enable");
  }
  const apiKey = deps.env.apiKey?.trim();
  if (!apiKey) {
    throw new LiveScenariosDisabledError("CURSOR_API_KEY missing from environment");
  }

  const totalController = new AbortController();
  const totalTimer = setTimeout(() => totalController.abort(), totalMs);
  const wt = <T>(phase: string, work: (signal: AbortSignal) => Promise<T>): Promise<T> =>
    withTimeout(phase, phaseMs, totalController.signal, work, settleGraceMs);
  const outcomes: ScenarioOutcome[] = [];
  let sdk: LiveScenariosSdk | undefined;
  let models: ScenarioModelRow[] = [];

  try {
    sdk = await wt("loadSdk", async () => deps.loadSdk());
    if (sdk.packageVersion !== PINNED_LIVE_SDK_VERSION) {
      throw new Error(`expected @cursor/sdk ${PINNED_LIVE_SDK_VERSION}, found ${sdk.packageVersion}`);
    }

    outcomes.push(
      await runScenario("discovery", async () => {
        const listed = await wt("models.list", async () =>
          sdk!.Cursor.models.list({ apiKey }),
        );
        models = Array.isArray(listed) ? listed : [];
        if (!pickModel(models)) {
          throw new Error("models.list returned no usable model id");
        }
        return `modelCount=${models.length}`;
      }),
    );

    const defaultModel = pickModel(models);
    const reasoningModel = pickReasoningModel(models);
    const defaultModelID = defaultModel?.id;
    if (!defaultModelID) {
      for (const name of [
        "text",
        "reasoning",
        "workspace_safety_required",
        "workspace_safety_off",
        "configured_mcp",
        "reuse",
        "cancellation",
        "hard_bridge_restart",
        "canonical_rebootstrap",
        "shutdown",
      ]) {
        outcomes.push({
          name,
          status: "failed",
          detail: "no usable model id after discovery",
        });
      }
      const summary = buildLiveScenariosSummary({
        sdkVersion: sdk.packageVersion,
        node: deps.env.nodeVersion,
        platform: sdk.platform,
        scenarios: outcomes,
      });
      logError(JSON.stringify(summary));
      return summary;
    }

    outcomes.push(
      await runScenario("text", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "text", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await wt("text.create", async () =>
              sdk!.Agent.create({
                apiKey,
                model: { id: defaultModelID },
                local: {
                  cwd: ws.cwd,
                  store,
                  settingSources: [],
                  sandboxOptions: { enabled: false },
                  autoReview: false,
                  enableAgentRetries: false,
                },
              }),
            );
            let sawText = false;
            const run = await wt("text.send", async () =>
              agent!.send("live: reply with ok", {
                onDelta: ({ update }) => {
                  if (update?.type === "text-delta") sawText = true;
                },
              }),
            );
            await wt("text.complete", async (signal) =>
              completeRun(run, signal, { cleanupBoundMs }),
            );
            if (!sawText) throw new Error("text-delta not observed");
            return "text-delta observed";
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("reasoning", async () => {
        if (!reasoningModel?.id) {
          throw new Error("SKIP: no model advertises reasoning parameters");
        }
        const reasoningModelID = reasoningModel.id;
        if (!reasoningModelID) {
          throw new Error("SKIP: no model advertises reasoning parameters");
        }
        const reasoningValue = firstReasoningValue(reasoningModel) ?? "low";
        return withScenarioWorkspace(deps.mkWorkspace, "reasoning", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await wt("reasoning.create", async () =>
              sdk!.Agent.create({
                apiKey,
                model: {
                  id: reasoningModelID,
                  params: [{ id: "reasoning", value: reasoningValue }],
                },
                local: {
                  cwd: ws.cwd,
                  store,
                  settingSources: [],
                  sandboxOptions: { enabled: false },
                  autoReview: false,
                  enableAgentRetries: false,
                },
              }),
            );
            let sawThinking = false;
            const run = await wt("reasoning.send", async () =>
              agent!.send("live: brief plan only", {
                onDelta: ({ update }) => {
                  if (update?.type === "thinking-delta") sawThinking = true;
                },
              }),
            );
            await wt("reasoning.complete", async (signal) =>
              completeRun(run, signal, { cleanupBoundMs }),
            );
            if (!sawThinking) {
              throw new Error("SKIP: model did not emit thinking-delta");
            }
            return "thinking-delta observed";
          } finally {
            await disposeAgent(agent);
          }
        });
      }),
    );

    outcomes.push(
      await runScenario("workspace_safety_required", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "sandbox-required", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await sdk!.Agent.create({
              apiKey,
              model: { id: defaultModelID },
              local: {
                cwd: ws.cwd,
                store,
                settingSources: [],
                sandboxOptions: { enabled: true },
                autoReview: false,
                enableAgentRetries: false,
              },
            });
            if (sdk!.platform === "win32") {
              throw new Error("BLOCKED: sandbox required succeeded on win32 unexpectedly");
            }
            return "sandbox required accepted";
          } catch (err) {
            const msg = sanitizeError(err).toLowerCase();
            if (sdk!.platform === "win32" || msg.includes("sandbox")) {
              throw new Error("BLOCKED: sandbox required unavailable on this platform");
            }
            throw err;
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("workspace_safety_off", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "sandbox-off", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await sdk!.Agent.create({
              apiKey,
              model: { id: defaultModelID },
              local: {
                cwd: ws.cwd,
                store,
                settingSources: [],
                sandboxOptions: { enabled: false },
                autoReview: false,
                enableAgentRetries: false,
              },
            });
            return "explicit sandbox off accepted";
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("configured_mcp", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "mcp", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await sdk!.Agent.create({
              apiKey,
              model: { id: defaultModelID },
              local: {
                cwd: ws.cwd,
                store,
                settingSources: ["project"],
                sandboxOptions: { enabled: false },
                autoReview: false,
                enableAgentRetries: false,
              },
              mcpServers: {},
            });
            return "empty MCP config accepted";
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("reuse", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "reuse", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await wt("reuse.create", async () =>
              sdk!.Agent.create({
                apiKey,
                model: { id: defaultModelID },
                local: {
                  cwd: ws.cwd,
                  store,
                  settingSources: [],
                  sandboxOptions: { enabled: false },
                  autoReview: false,
                  enableAgentRetries: false,
                },
              }),
            );
            const run1 = await wt("reuse.send1", async () =>
              agent!.send(liveTextOnlyPrompt(LIVE_REUSE_TOKEN_FIRST)),
            );
            await wt("reuse.wait1", async () => run1.wait());
            if (run1.status && run1.status !== "finished" && run1.status !== "completed") {
              throw new Error(`reuse run1 unexpected status=${run1.status}`);
            }
            const run2 = await wt("reuse.send2", async () =>
              agent!.send(liveTextOnlyPrompt(LIVE_REUSE_TOKEN_SECOND)),
            );
            await wt("reuse.wait2", async () => run2.wait());
            if (run2.status && run2.status !== "finished" && run2.status !== "completed") {
              throw new Error(`reuse run2 unexpected status=${run2.status}`);
            }
            return "two sends on same agent";
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("cancellation", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "cancel", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await wt("cancel.create", async () =>
              sdk!.Agent.create({
                apiKey,
                model: { id: defaultModelID },
                local: {
                  cwd: ws.cwd,
                  store,
                  settingSources: [],
                  sandboxOptions: { enabled: false },
                  autoReview: false,
                  enableAgentRetries: false,
                },
              }),
            );
            const run = await wt("cancel.send", async () =>
              agent!.send("live: cancel target"),
            );
            if (!run.supports || run.supports("cancel")) {
              await wt("cancel.run", async () => run.cancel());
            } else {
              throw new Error("SKIP: run cancel unsupported");
            }
            try {
              await wt("cancel.wait", async () => run.wait());
            } catch {
              // cancel may abort wait
            }
            return `cancelStatus=${run.status ?? "cancelled"}`;
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    outcomes.push(
      await runScenario("hard_bridge_restart", async () => {
        const hook = deps.processLifecycle?.hardBridgeRestart;
        if (!hook) {
          throw new Error("BLOCKED: hard_bridge_restart unavailable (inject processLifecycle.hardBridgeRestart)");
        }
        return await wt("hard_bridge_restart", async () => hook());
      }),
    );

    outcomes.push(
      await runScenario("canonical_rebootstrap", async () => {
        const hook = deps.processLifecycle?.canonicalRebootstrap;
        if (!hook) {
          throw new Error("BLOCKED: canonical_rebootstrap unavailable (inject processLifecycle.canonicalRebootstrap)");
        }
        return await wt("canonical_rebootstrap", async () => hook());
      }),
    );

    outcomes.push(
      await runScenario("shutdown", async () =>
        withScenarioWorkspace(deps.mkWorkspace, "shutdown", async (ws) => {
          const store = createStore();
          let agent: ScenarioAgent | undefined;
          try {
            agent = await wt("shutdown.create", async () =>
              sdk!.Agent.create({
                apiKey,
                model: { id: defaultModelID },
                local: {
                  cwd: ws.cwd,
                  store,
                  settingSources: [],
                  sandboxOptions: { enabled: false },
                  autoReview: false,
                  enableAgentRetries: false,
                },
              }),
            );
            return "agent disposed";
          } finally {
            await disposeAgent(agent);
          }
        }),
      ),
    );

    const summary = buildLiveScenariosSummary({
      sdkVersion: sdk.packageVersion,
      node: deps.env.nodeVersion,
      platform: sdk.platform,
      scenarios: outcomes,
    });
    // One authoritative summary JSON: return failed/blocked instead of throw+catch re-log.
    const line = JSON.stringify(summary);
    if (summary.status === "complete" && summary.ok) {
      log(line);
    } else {
      logError(line);
    }
    return summary;
  } catch (err) {
    if (err instanceof LiveScenariosDisabledError) {
      throw err;
    }
    // One parseable JSON envelope for load/timeout/pre-scenario and mid-suite failures.
    const safe = sanitizeError(err);
    const failedSummary: LiveScenariosSummary = {
      ok: false,
      status: "failed",
      sdkVersion: sdk?.packageVersion ?? "unknown",
      node: deps.env.nodeVersion,
      platform: sdk?.platform ?? "unknown",
      scenarios: outcomes,
      error: safe,
    };
    logError(JSON.stringify(failedSummary));
    return failedSummary;
  } finally {
    clearTimeout(totalTimer);
  }
}

export function buildLiveScenariosSummary(input: {
  sdkVersion: string;
  node: string;
  platform: string;
  scenarios: ScenarioOutcome[];
}): LiveScenariosSummary {
  const failed = input.scenarios.filter((o) => o.status === "failed");
  if (failed.length > 0) {
    return {
      ok: false,
      status: "failed",
      sdkVersion: input.sdkVersion,
      node: input.node,
      platform: input.platform,
      scenarios: input.scenarios,
    };
  }
  const blocked = input.scenarios.filter((o) => o.status === "blocked");
  const requiredSkipped = input.scenarios.filter(
    (o) => o.status === "skipped" && !OPTIONAL_SCENARIOS.has(o.name),
  );
  if (blocked.length > 0 || requiredSkipped.length > 0) {
    return {
      ok: false,
      status: "blocked",
      sdkVersion: input.sdkVersion,
      node: input.node,
      platform: input.platform,
      scenarios: input.scenarios,
    };
  }
  return {
    ok: true,
    status: "complete",
    sdkVersion: input.sdkVersion,
    node: input.node,
    platform: input.platform,
    scenarios: input.scenarios,
  };
}
