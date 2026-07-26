/**
 * Testable live probe workflow for exact @cursor/sdk 1.0.23.
 * Default tests inject mocks; the CLI loads the real package when opted in.
 */

import type { LocalAgentStore } from "@cursor/sdk";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";

export const PINNED_LIVE_SDK_VERSION = "1.0.23";

export const DEFAULT_PROBE_TOTAL_TIMEOUT_MS = 120_000;
export const DEFAULT_PROBE_PHASE_TIMEOUT_MS = 30_000;

export interface ProbeModelRow {
  id?: string;
  displayName?: string;
}

export interface ProbeTokenUsage {
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  reasoningTokens?: number;
}

export interface ProbeDelta {
  type: string;
  text?: string;
  usage?: ProbeTokenUsage;
}

export interface ProbeStep {
  type?: string;
}

export interface ProbeRunResult {
  status: string;
  usage?: ProbeTokenUsage | undefined;
}

export interface ProbeRun {
  id: string;
  status?: string | undefined;
  usage?: ProbeTokenUsage | undefined;
  supports?(op: string): boolean;
  stream(signal?: AbortSignal): AsyncGenerator<unknown, void, unknown>;
  wait(signal?: AbortSignal): Promise<ProbeRunResult>;
  cancel(): Promise<void>;
}

export interface ProbeSendOptions {
  onDelta?: (args: { update: ProbeDelta }) => void | Promise<void>;
  onStep?: (args: { step: ProbeStep }) => void | Promise<void>;
}

export interface ProbeAgent {
  agentId: string;
  send(prompt: string, options?: ProbeSendOptions): Promise<ProbeRun>;
  close(): void;
  [Symbol.asyncDispose](): Promise<void>;
}

export interface ProbeAgentCreateOptions {
  apiKey: string;
  model: { id: string };
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

export interface LiveProbeSdk {
  packageVersion: string;
  Cursor: {
    models: {
      list(opts: { apiKey: string }): Promise<ProbeModelRow[]>;
    };
  };
  Agent: {
    create(opts: ProbeAgentCreateOptions): Promise<ProbeAgent>;
  };
}

export interface LiveProbeEnv {
  liveProbeEnabled: boolean;
  apiKey?: string | undefined;
  cwd: string;
  nodeVersion: string;
}

export interface LiveProbeTimeouts {
  totalMs?: number;
  phaseMs?: number;
}

export interface LiveProbeDeps {
  env: LiveProbeEnv;
  loadSdk: () => Promise<LiveProbeSdk>;
  createStore?: () => LocalAgentStore;
  timeouts?: LiveProbeTimeouts;
  now?: () => number;
  log?: (line: string) => void;
  logError?: (line: string) => void;
}

export interface LiveProbeSummary {
  ok: true;
  sdkVersion: string;
  node: string;
  modelCount: number;
  sample: Array<{ id: string; displayName: string }>;
  observedDeltaKinds: string[];
  observedStepTypes: string[];
  turnUsageKeys: string[];
  cumulativeUsageKeys: string[];
  cancelStatus: string;
  disposed: boolean;
  usedInMemoryStore: boolean;
}

export class LiveProbeDisabledError extends Error {
  readonly exitCode = 2;
  constructor(message: string) {
    super(message);
    this.name = "LiveProbeDisabledError";
  }
}

export class LiveProbeTimeoutError extends Error {
  constructor(phase: string) {
    super(`live probe timeout: ${phase}`);
    this.name = "LiveProbeTimeoutError";
  }
}

function sanitizeUsageKeys(usage: ProbeTokenUsage | undefined): string[] {
  if (!usage || typeof usage !== "object") return [];
  return Object.keys(usage)
    .filter((k) => typeof (usage as Record<string, unknown>)[k] === "number")
    .sort();
}

function sanitizeError(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err);
  return message
    .replace(/sk-[A-Za-z0-9_-]+/g, "[REDACTED]")
    .replace(/crsr_[A-Za-z0-9_-]+/g, "[REDACTED]")
    .replace(/apiKey["']?\s*[:=]\s*["']?[^"',\s}]+/gi, "apiKey=[REDACTED]")
    .slice(0, 200);
}

async function withTimeout<T>(
  phase: string,
  ms: number,
  signal: AbortSignal,
  work: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  if (signal.aborted) throw new LiveProbeTimeoutError(phase);
  let timer: ReturnType<typeof setTimeout> | undefined;
  const phaseController = new AbortController();
  const onAbort = () => phaseController.abort();
  signal.addEventListener("abort", onAbort, { once: true });
  const timeoutPromise = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      phaseController.abort();
      reject(new LiveProbeTimeoutError(phase));
    }, ms);
  });
  try {
    return await Promise.race([work(phaseController.signal), timeoutPromise]);
  } finally {
    if (timer) clearTimeout(timer);
    signal.removeEventListener("abort", onAbort);
  }
}

async function drainStream(stream: AsyncGenerator<unknown, void, unknown>, signal: AbortSignal): Promise<void> {
  for await (const _event of stream) {
    if (signal.aborted) break;
  }
}

/** Bounded live probe covering the Task 1.1 contract surface. */
export async function runLiveProbe(deps: LiveProbeDeps): Promise<LiveProbeSummary> {
  const log = deps.log ?? (() => undefined);
  const logError = deps.logError ?? (() => undefined);
  const totalMs = deps.timeouts?.totalMs ?? DEFAULT_PROBE_TOTAL_TIMEOUT_MS;
  const phaseMs = deps.timeouts?.phaseMs ?? DEFAULT_PROBE_PHASE_TIMEOUT_MS;
  const createStore = deps.createStore ?? createBridgeInMemoryLocalAgentStore;

  if (!deps.env.liveProbeEnabled) {
    throw new LiveProbeDisabledError("live probe disabled; set CURSOR_SDK_LIVE_PROBE=1 to enable");
  }
  const apiKey = deps.env.apiKey?.trim();
  if (!apiKey) {
    throw new LiveProbeDisabledError("CURSOR_API_KEY missing from environment");
  }

  const totalController = new AbortController();
  const totalTimer = setTimeout(() => totalController.abort(), totalMs);
  const store = createStore();
  let agent: ProbeAgent | undefined;
  let disposed = false;

  try {
    const sdk = await withTimeout("loadSdk", phaseMs, totalController.signal, async () => deps.loadSdk());
    if (sdk.packageVersion !== PINNED_LIVE_SDK_VERSION) {
      throw new Error(
        `expected @cursor/sdk ${PINNED_LIVE_SDK_VERSION}, found ${sdk.packageVersion}`,
      );
    }

    const models = await withTimeout("models.list", phaseMs, totalController.signal, async () =>
      sdk.Cursor.models.list({ apiKey }),
    );
    const modelList = Array.isArray(models) ? models : [];
    const sample = modelList.slice(0, 3).map((m) => ({
      id: typeof m.id === "string" ? m.id : "",
      displayName: typeof m.displayName === "string" ? m.displayName : "",
    }));
    const modelId = sample.find((m) => m.id)?.id;
    if (!modelId) {
      throw new Error("models.list returned no usable model id");
    }

    const observedDeltaKinds: string[] = [];
    const observedStepTypes: string[] = [];
    let turnUsageKeys: string[] = [];
    let cancelStatus = "skipped";
    let cumulativeUsageKeys: string[] = [];

    agent = await withTimeout("Agent.create", phaseMs, totalController.signal, async () =>
      sdk.Agent.create({
        apiKey,
        model: { id: modelId },
        local: {
          cwd: deps.env.cwd,
          store,
          settingSources: [],
          sandboxOptions: { enabled: false },
          autoReview: false,
          enableAgentRetries: false,
        },
      }),
    );

    const run = await withTimeout("agent.send", phaseMs, totalController.signal, async () =>
      agent!.send("probe: reply with ok", {
        onDelta: ({ update }) => {
          if (update && typeof update.type === "string") {
            if (!observedDeltaKinds.includes(update.type)) {
              observedDeltaKinds.push(update.type);
            }
            if (update.type === "turn-ended") {
              turnUsageKeys = sanitizeUsageKeys(update.usage);
            }
          }
        },
        onStep: ({ step }) => {
          const kind = typeof step?.type === "string" ? step.type : "step";
          if (!observedStepTypes.includes(kind)) observedStepTypes.push(kind);
        },
      }),
    );

    await withTimeout("run.stream", phaseMs, totalController.signal, async (signal) =>
      drainStream(run.stream(signal), signal),
    );
    const terminal = await withTimeout("run.wait", phaseMs, totalController.signal, async (signal) =>
      run.wait(signal),
    );
    cumulativeUsageKeys = sanitizeUsageKeys(terminal.usage ?? run.usage);

    const cancelRun = await withTimeout("agent.send.cancel", phaseMs, totalController.signal, async () =>
      agent!.send("probe: cancel target"),
    );
    if (!cancelRun.supports || cancelRun.supports("cancel")) {
      await withTimeout("run.cancel", phaseMs, totalController.signal, async () => cancelRun.cancel());
      cancelStatus = cancelRun.status ?? "cancelled";
      try {
        await withTimeout("run.wait.afterCancel", phaseMs, totalController.signal, async (signal) =>
          cancelRun.wait(signal),
        );
      } catch {
        // cancel may abort wait
      }
      cancelStatus = cancelRun.status ?? cancelStatus;
    } else {
      cancelStatus = "unsupported";
      try {
        await withTimeout("run.wait.unsupportedCancel", phaseMs, totalController.signal, async (signal) =>
          cancelRun.wait(signal),
        );
      } catch {
        // ignore
      }
    }

    await withTimeout("agent.dispose", phaseMs, totalController.signal, async () =>
      agent![Symbol.asyncDispose](),
    );
    disposed = true;
    agent = undefined;

    const summary: LiveProbeSummary = {
      ok: true,
      sdkVersion: sdk.packageVersion,
      node: deps.env.nodeVersion,
      modelCount: modelList.length,
      sample,
      observedDeltaKinds,
      observedStepTypes,
      turnUsageKeys,
      cumulativeUsageKeys,
      cancelStatus,
      disposed,
      usedInMemoryStore: true,
    };
    log(JSON.stringify(summary));
    return summary;
  } catch (err) {
    logError(JSON.stringify({ ok: false, error: sanitizeError(err) }));
    if (agent) {
      try {
        agent.close();
        await agent[Symbol.asyncDispose]();
        disposed = true;
      } catch {
        // ignore dispose failures during error path
      }
    }
    throw err instanceof Error ? err : new Error(sanitizeError(err));
  } finally {
    clearTimeout(totalTimer);
  }
}
