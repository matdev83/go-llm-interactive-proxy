import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";
import { redactMessage } from "./errors.js";
import type { AgentCreateParams } from "./params.js";
import {
  RunSequencer,
  SCHEMA_VERSION,
  TYPE_EVENT,
  encodeFrame,
  type EventKind,
  type Frame,
} from "./protocol.js";

export const MAX_MCP_CONFIG_BYTES = 256 << 10;

const VALID_SETTING_SOURCES = new Set([
  "project",
  "user",
  "team",
  "mdm",
  "plugins",
  "all",
]);

export interface MappedBridgeEvent {
  kind: EventKind;
  payload: Record<string, unknown>;
}

export interface SdkDeltaUpdate {
  type?: string;
  text?: string;
  usage?: Record<string, number>;
}

export interface SdkConversationStep {
  type?: string;
  message?: Record<string, unknown>;
}

export interface SdkRunHandle {
  id: string;
  stream(): AsyncGenerator<unknown, void>;
  wait(): Promise<{ status?: string; usage?: Record<string, number>; error?: { message?: string } }>;
  cancel(): Promise<void>;
  readonly status?: string;
  readonly usage?: Record<string, number>;
}

export interface SdkAgentHandle {
  send(
    prompt: string,
    options?: {
      onDelta?: (args: { update: SdkDeltaUpdate }) => void | Promise<void>;
      onStep?: (args: { step: SdkConversationStep }) => void | Promise<void>;
      local?: Record<string, never>;
    },
  ): Promise<SdkRunHandle>;
  /** SDK 1.0.23 primary disposal. */
  [Symbol.asyncDispose]?: () => Promise<void>;
  /** SDK 1.0.23 supported sync fallback when asyncDispose is absent. */
  close?: () => void;
}

export interface SdkAgentModule {
  create(options: Record<string, unknown>): Promise<SdkAgentHandle>;
}

export interface AgentPoolOptions {
  Agent: SdkAgentModule;
  createStore?: () => unknown;
  emitEvent: (line: string) => void;
  cancelTimeoutMs?: number;
}

type AgentState = "ready" | "busy" | "invalidated";

interface AgentRecord {
  sdkAgent: SdkAgentHandle;
  state: AgentState;
  apiKey: string;
  activeRunId?: string;
  disposed?: boolean;
}

interface RunRecord {
  done: Promise<void>;
  handle: SdkRunHandle;
  agent: AgentRecord;
  seqGuard: RunSequencer;
  ctx: { runId: string; seq: number };
  cancelTimedOut?: boolean;
}

export class CancelTimeoutError extends Error {
  constructor() {
    super("cursor_sdk_cancel_timeout");
    this.name = "CancelTimeoutError";
  }
}

const DEFAULT_CANCEL_TIMEOUT_MS = 10_000;

export class AgentPool {
  private readonly agents = new Map<string, AgentRecord>();
  private readonly runs = new Map<string, RunRecord>();
  private nextAgentId = 1;
  private readonly Agent: SdkAgentModule;
  private readonly createStore: () => unknown;
  private readonly emitEvent: (line: string) => void;
  private readonly cancelTimeoutMs: number;
  private shutDown = false;

  constructor(options: AgentPoolOptions) {
    this.Agent = options.Agent;
    this.createStore = options.createStore ?? createBridgeInMemoryLocalAgentStore;
    this.emitEvent = options.emitEvent;
    this.cancelTimeoutMs = options.cancelTimeoutMs ?? DEFAULT_CANCEL_TIMEOUT_MS;
  }

  async createAgent(params: AgentCreateParams): Promise<{ agentId: string }> {
    if (this.shutDown) {
      throw new Error("bridge_shutting_down");
    }
    validateAgentCreateParams(params);
    const sdkAgent = await this.Agent.create(mapCreateOptions(params, this.createStore()));
    const agentId = `agent-${this.nextAgentId++}`;
    this.agents.set(agentId, { sdkAgent, state: "ready", apiKey: params.apiKey });
    return { agentId };
  }

  async send(agentId: string, prompt: string): Promise<{ runId: string }> {
    if (this.shutDown) {
      throw new Error("bridge_shutting_down");
    }
    const record = this.agents.get(agentId);
    if (!record) {
      throw new Error(`unknown agentId ${agentId}`);
    }
    if (record.state === "invalidated") {
      throw new Error("agent_invalidated");
    }
    if (record.state === "busy") {
      throw new Error("agent_busy");
    }

    const ctx = { runId: "", seq: 0 };
    const seqGuard = new RunSequencer();
    const secrets = agentSecrets(record);
    const run = await record.sdkAgent.send(prompt, {
      onDelta: ({ update }) => {
        const mapped = mapSdkDelta(update);
        if (!mapped || !ctx.runId) return;
        this.emitMapped(ctx.runId, seqGuard, ctx, mapped, secrets);
      },
      onStep: ({ step }) => {
        const mapped = mapSdkStep(step);
        if (!mapped || !ctx.runId) return;
        this.emitMapped(ctx.runId, seqGuard, ctx, mapped, secrets);
      },
    });
    ctx.runId = run.id;
    record.state = "busy";
    record.activeRunId = run.id;

    const done = Promise.resolve().then(() => this.executeRun(record, ctx, run, seqGuard));
    this.runs.set(run.id, { done, handle: run, agent: record, seqGuard, ctx });
    return { runId: run.id };
  }

  async cancelRun(runId: string): Promise<{ cancelled: boolean }> {
    const record = this.runs.get(runId);
    if (!record) {
      return { cancelled: true };
    }
    if (record.cancelTimedOut) {
      return { cancelled: true };
    }
    try {
      await withTimeout(record.handle.cancel(), this.cancelTimeoutMs, () => new CancelTimeoutError());
    } catch (err) {
      if (err instanceof CancelTimeoutError) {
        this.invalidateTimedOutRun(record);
        throw err;
      }
      const message = err instanceof Error ? err.message : String(err ?? "");
      throw new Error(`run cancel failed: ${message}`);
    }
    return { cancelled: true };
  }

  private invalidateTimedOutRun(record: RunRecord): void {
    record.cancelTimedOut = true;
    if (!record.seqGuard.terminated(record.ctx.runId)) {
      this.emitTerminal(
        record.ctx.runId,
        record.seqGuard,
        record.ctx,
        "error",
        {
          message: "cursor_sdk_cancel_timeout",
          code: "cursor_sdk_cancel_timeout",
        },
        agentSecrets(record.agent),
      );
    }
    // Keep ownership of the timed-out run and its done promise. Go Task 4 owns hard kill.
    record.agent.state = "invalidated";
  }

  /** Test/observability helper: stuck cancel-timeout runs remain tracked until they settle. */
  trackedRunCount(): number {
    return this.runs.size;
  }

  async disposeAgent(agentId: string): Promise<{ disposed: boolean }> {
    const record = this.agents.get(agentId);
    if (!record) {
      throw new Error(`unknown agentId ${agentId}`);
    }
    if (record.disposed) {
      return { disposed: true };
    }
    if (record.activeRunId) {
      const runId = record.activeRunId;
      try {
        await this.cancelRun(runId);
      } catch {
        // Cancel may time out; dispose still waits on the run with a bound.
      }
      const run = this.runs.get(runId);
      if (run) {
        await withTimeout(run.done.catch(() => undefined), this.cancelTimeoutMs, () => {
          return new Error("cursor_sdk_dispose_timeout");
        });
      }
    }
    await disposeSdkAgent(record.sdkAgent);
    record.disposed = true;
    record.state = "ready";
    delete record.activeRunId;
    return { disposed: true };
  }

  async shutdown(): Promise<{ shutdown: boolean }> {
    if (this.shutDown) {
      if (this.runs.size > 0) {
        throw new Error("cursor_sdk_shutdown_timeout");
      }
      return { shutdown: true };
    }
    this.shutDown = true;
    const activeRuns = [...this.runs.keys()];
    for (const runId of activeRuns) {
      try {
        await this.cancelRun(runId);
      } catch {
        // Continue shutdown wait even when cancel times out.
      }
    }
    const pending = [...this.runs.values()];
    const waitResults = await Promise.all(
      pending.map((run) =>
        withTimeout(run.done.catch(() => undefined), this.cancelTimeoutMs, () => {
          return new Error("cursor_sdk_shutdown_timeout");
        })
          .then(() => undefined as Error | undefined)
          .catch((err: unknown) => (err instanceof Error ? err : new Error(String(err)))),
      ),
    );
    const shutdownTimeout = waitResults.find((err) => err?.message === "cursor_sdk_shutdown_timeout");
    const agentIds = [...this.agents.keys()];
    for (const agentId of agentIds) {
      const record = this.agents.get(agentId);
      if (!record || record.disposed) continue;
      try {
        await this.disposeAgent(agentId);
      } catch {
        // Continue disposing other agents; surface stuck-run failure below.
      }
    }
    if (shutdownTimeout) {
      throw shutdownTimeout;
    }
    return { shutdown: true };
  }

  isShutDown(): boolean {
    return this.shutDown;
  }

  async waitForRun(runId: string): Promise<void> {
    const run = this.runs.get(runId);
    if (!run) throw new Error(`unknown runId ${runId}`);
    await run.done;
  }

  private emitMapped(
    runId: string,
    seqGuard: RunSequencer,
    ctx: { seq: number },
    mapped: MappedBridgeEvent,
    secrets: string[] = [],
  ): void {
    if (seqGuard.terminated(runId)) {
      return;
    }
    const payload = sanitizeEventPayload(mapped.payload, secrets);
    ctx.seq += 1;
    const frame: Frame = {
      schemaVersion: SCHEMA_VERSION,
      type: TYPE_EVENT,
      runId,
      seq: ctx.seq,
      kind: mapped.kind,
      payload,
    };
    try {
      seqGuard.accept(frame);
    } catch {
      return;
    }
    this.emitEvent(`${encodeFrame(frame)}\n`);
  }

  private async executeRun(
    record: AgentRecord,
    ctx: { runId: string; seq: number },
    run: SdkRunHandle,
    seqGuard: RunSequencer,
  ): Promise<void> {
    try {
      for await (const _ of run.stream()) {
        // onDelta/onStep callbacks fire during stream consumption.
      }
      const secrets = agentSecrets(record);
      const terminal = await run.wait();
      if (terminal.status === "error" || run.status === "error") {
        const message =
          terminal.error?.message ??
          (typeof terminal.status === "string" ? terminal.status : "run failed");
        this.emitTerminal(ctx.runId, seqGuard, ctx, "error", { message }, secrets);
        return;
      }
      if (terminal.status === "cancelled" || run.status === "cancelled") {
        this.emitTerminal(ctx.runId, seqGuard, ctx, "finished", { status: "cancelled" }, secrets);
        return;
      }
      this.emitTerminal(ctx.runId, seqGuard, ctx, "finished", { status: "finished" }, secrets);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err ?? "run failed");
      if (!seqGuard.terminated(ctx.runId)) {
        this.emitTerminal(ctx.runId, seqGuard, ctx, "error", { message }, agentSecrets(record));
      }
    } finally {
      const tracked = this.runs.get(ctx.runId);
      if (tracked?.cancelTimedOut) {
        // Late settlement after cancel timeout: drop run ownership once done finishes.
        // Agent stays invalidated; do not reopen for sends.
        this.runs.delete(ctx.runId);
        delete record.activeRunId;
        return;
      }
      if (record.state !== "invalidated") {
        record.state = "ready";
      }
      delete record.activeRunId;
      this.runs.delete(ctx.runId);
    }
  }

  private emitTerminal(
    runId: string,
    seqGuard: RunSequencer,
    ctx: { seq: number },
    kind: "finished" | "error",
    payload: Record<string, unknown>,
    secrets: string[] = [],
  ): void {
    if (seqGuard.terminated(runId)) return;
    this.emitMapped(runId, seqGuard, ctx, { kind, payload }, secrets);
  }
}

function agentSecrets(record: AgentRecord): string[] {
  return record.apiKey ? [record.apiKey] : [];
}

function sanitizeEventPayload(
  payload: Record<string, unknown>,
  secrets: string[],
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...payload };
  if (typeof out.message === "string") {
    out.message = redactMessage(out.message, secrets);
  }
  return out;
}

async function disposeSdkAgent(agent: SdkAgentHandle): Promise<void> {
  const asyncDispose = agent[Symbol.asyncDispose];
  if (typeof asyncDispose === "function") {
    await asyncDispose.call(agent);
    return;
  }
  if (typeof agent.close === "function") {
    agent.close();
  }
}

async function withTimeout<T>(
  promise: Promise<T>,
  timeoutMs: number,
  onTimeout: () => Error,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await new Promise<T>((resolve, reject) => {
      let settled = false;
      const finish = (fn: () => void): void => {
        if (settled) return;
        settled = true;
        fn();
      };
      // Attach handlers immediately so a late reject after timeout is not unhandled.
      promise.then(
        (value) => finish(() => resolve(value)),
        (err) => finish(() => reject(err)),
      );
      timer = setTimeout(() => finish(() => reject(onTimeout())), timeoutMs);
    });
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

export function validateAgentCreateParams(params: AgentCreateParams): void {
  for (const source of params.settingSources) {
    if (typeof source !== "string" || !VALID_SETTING_SOURCES.has(source)) {
      throw new Error(`unknown settingSources value ${String(source)}`);
    }
  }
  if (params.mcpServers !== undefined) {
    const encoded = JSON.stringify(params.mcpServers);
    if (encoded.length > MAX_MCP_CONFIG_BYTES) {
      throw new Error(`mcpServers exceeds ${MAX_MCP_CONFIG_BYTES} byte limit`);
    }
    if (params.mcpServers === null || typeof params.mcpServers !== "object" || Array.isArray(params.mcpServers)) {
      throw new Error("invalid mcpServers");
    }
  }
}

export function mapCreateOptions(params: AgentCreateParams, store: unknown): Record<string, unknown> {
  const model: Record<string, unknown> = { id: params.model.id };
  const mappedParams = mapModelParams(params.model.params);
  if (mappedParams) model.params = mappedParams;

  const options: Record<string, unknown> = {
    apiKey: params.apiKey,
    model,
    local: {
      cwd: params.local.cwd,
      store,
      settingSources: [...params.settingSources],
      sandboxOptions: { enabled: params.sandboxOptions.enabled },
      autoReview: params.autoReview,
      enableAgentRetries: false,
    },
  };
  if (params.mcpServers !== undefined) {
    options.mcpServers = params.mcpServers;
  }
  return options;
}

function mapModelParams(raw: unknown): Array<{ id: string; value: string }> | undefined {
  if (raw == null) return undefined;
  if (!Array.isArray(raw)) return undefined;
  const out: Array<{ id: string; value: string }> = [];
  for (const item of raw) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    const obj = item as Record<string, unknown>;
    if (typeof obj.id !== "string" || typeof obj.value !== "string") continue;
    out.push({ id: obj.id, value: obj.value });
  }
  return out.length > 0 ? out : undefined;
}

export function mapSdkDelta(update: SdkDeltaUpdate): MappedBridgeEvent | undefined {
  switch (update.type) {
    case "text-delta":
      if (typeof update.text !== "string") return undefined;
      return { kind: "text_delta", payload: { text: update.text } };
    case "thinking-delta":
      if (typeof update.text !== "string") return undefined;
      return { kind: "reasoning_delta", payload: { text: update.text } };
    case "turn-ended": {
      const usage = normalizeTurnUsage(update.usage);
      if (!usage) return undefined;
      return { kind: "usage", payload: usage };
    }
    default:
      return undefined;
  }
}

export function mapSdkStep(step: SdkConversationStep): MappedBridgeEvent | undefined {
  if (step.type === "toolCall") {
    const message = step.message;
    const name = typeof message?.type === "string" ? message.type : "tool";
    return { kind: "activity", payload: { name } };
  }
  if (step.type === "warning") {
    const message = step.message;
    const text =
      typeof message?.text === "string"
        ? message.text
        : typeof message?.message === "string"
          ? message.message
          : "warning";
    return { kind: "warning", payload: { message: text } };
  }
  return undefined;
}

export function normalizeTurnUsage(raw: Record<string, number> | undefined): Record<string, number> | undefined {
  if (!raw) return undefined;
  const inputTokens = raw.inputTokens;
  const outputTokens = raw.outputTokens;
  const totalTokens = raw.totalTokens;
  if (
    typeof inputTokens !== "number" ||
    typeof outputTokens !== "number" ||
    typeof totalTokens !== "number"
  ) {
    return undefined;
  }
  const usage: Record<string, number> = {
    inputTokens,
    outputTokens,
    totalTokens,
    cacheReadTokens: typeof raw.cacheReadTokens === "number" ? raw.cacheReadTokens : 0,
    cacheWriteTokens: typeof raw.cacheWriteTokens === "number" ? raw.cacheWriteTokens : 0,
  };
  if (typeof raw.reasoningTokens === "number") {
    usage.reasoningTokens = raw.reasoningTokens;
  }
  return usage;
}
