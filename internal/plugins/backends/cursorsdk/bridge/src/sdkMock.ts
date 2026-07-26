/**
 * Deterministic mocked @cursor/sdk runtime for Node tests.
 * Tracks agent/run creation and proves disposal leaves no open handles.
 */

export type DeltaKind = "text-delta" | "thinking-delta" | "turn-ended";

export interface MockDelta {
  type: DeltaKind;
  text?: string;
  usage?: Record<string, number>;
}

export interface MockStep {
  type: string;
  message?: Record<string, unknown>;
}

export interface MockSendOptions {
  onDelta?: (args: { update: MockDelta }) => void | Promise<void>;
  onStep?: (args: { step: MockStep }) => void | Promise<void>;
}

export interface MockRunScript {
  deltas?: MockDelta[];
  steps?: MockStep[];
  status?: "finished" | "cancelled" | "error";
  errorMessage?: string;
  blockCancel?: boolean;
  /** When true, cancel() never resolves (for timeout tests). */
  hangCancel?: boolean;
  /** Cumulative usage returned by wait(); must not be mapped as per-turn usage. */
  waitUsage?: Record<string, number>;
}

export interface MockAgentOptions {
  apiKey?: string;
  model?: { id: string };
  local?: {
    cwd?: string;
    store?: unknown;
    settingSources?: string[];
    sandboxOptions?: { enabled?: boolean };
    autoReview?: boolean;
    enableAgentRetries?: boolean;
  };
  mcpServers?: Record<string, unknown>;
}

export interface OpenHandleSnapshot {
  agents: number;
  runs: number;
  listeners: number;
}

let nextAgent = 1;
let nextRun = 1;

const openAgents = new Set<string>();
const openRuns = new Set<string>();
const openListeners = new Set<string>();

export function resetSdkMock(): void {
  nextAgent = 1;
  nextRun = 1;
  openAgents.clear();
  openRuns.clear();
  openListeners.clear();
  scriptedModels = defaultModels();
  runScripts.clear();
}

export function openHandles(): OpenHandleSnapshot {
  return {
    agents: openAgents.size,
    runs: openRuns.size,
    listeners: openListeners.size,
  };
}

let scriptedModels = defaultModels();
const runScripts = new Map<string, MockRunScript>();

function defaultModels() {
  return [
    {
      id: "gpt-5.3-codex",
      displayName: "GPT-5.3 Codex",
      parameters: [
        { id: "reasoning", type: "string", values: ["low", "medium", "high", "xhigh"] },
      ],
      variants: [{ id: "reasoning-xhigh", params: { reasoning: "xhigh" } }],
    },
    {
      id: "claude-4.6-sonnet-thinking",
      displayName: "Claude 4.6 Sonnet Thinking",
      parameters: [
        { id: "thinking", type: "boolean" },
        { id: "effort", type: "string", values: ["low", "medium", "high", "extra-high"] },
      ],
      variants: [
        { id: "effort-high-thinking", params: { effort: "high", thinking: true } },
        { id: "effort-extra-high-thinking", params: { effort: "extra-high", thinking: true } },
      ],
    },
    {
      id: "composer-2-fast",
      displayName: "Composer 2 Fast",
      parameters: [{ id: "thinking", type: "boolean" }],
      variants: [],
    },
  ];
}

export function setMockModels(models: ReturnType<typeof defaultModels>): void {
  scriptedModels = models;
}

export function setRunScript(promptSubstring: string, script: MockRunScript): void {
  runScripts.set(promptSubstring, script);
}

function redact(value: unknown): unknown {
  if (typeof value === "string") {
    if (value.length > 8 && /key|secret|token/i.test(value)) return "[REDACTED]";
    if (value.startsWith("sk-") || value.startsWith("crsr_")) return "[REDACTED]";
    return value;
  }
  if (Array.isArray(value)) return value.map(redact);
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (/apiKey|api_key|authorization|password|secret|token/i.test(k)) {
        out[k] = "[REDACTED]";
      } else {
        out[k] = redact(v);
      }
    }
    return out;
  }
  return value;
}

export class MockRun {
  readonly id: string;
  status: "running" | "finished" | "cancelled" | "error" = "running";
  private readonly script: MockRunScript;
  private readonly sendOptions: MockSendOptions | undefined;
  private completed = false;
  private streamHangResolve: (() => void) | undefined;
  private cancelHangPromise: Promise<void> | undefined;
  private cancelHangResolve: (() => void) | undefined;

  constructor(script: MockRunScript, sendOptions?: MockSendOptions) {
    this.id = `run-${nextRun++}`;
    this.script = script;
    this.sendOptions = sendOptions;
    openRuns.add(this.id);
  }

  /** Test helper for late stream settlement without mock-only abort hooks. */
  releaseStreamForTest(): void {
    this.streamHangResolve?.();
    this.streamHangResolve = undefined;
  }

  private defaultDeltas(): MockDelta[] {
    return (
      this.script.deltas ?? [
        { type: "text-delta", text: "hello" },
        { type: "turn-ended", usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 } },
      ]
    );
  }

  private async *iterate(): AsyncGenerator<MockDelta> {
    const listenerId = `listener-${this.id}`;
    openListeners.add(listenerId);
    try {
      const deltas = this.defaultDeltas();
      const content = deltas.filter((delta) => delta.type !== "turn-ended");
      const ending = deltas.filter((delta) => delta.type === "turn-ended");
      for (const delta of content) {
        if (this.status === "cancelled" || this.status === "error") break;
        await this.sendOptions?.onDelta?.({ update: delta });
        yield delta;
      }
      // hangCancel keeps the run active like a real SDK cancel that never finishes.
      if (this.script.hangCancel && this.status === "running") {
        await new Promise<void>((resolve) => {
          this.streamHangResolve = resolve;
        });
        return;
      }
      for (const step of this.script.steps ?? []) {
        if (this.status === "cancelled" || this.status === "error") break;
        await this.sendOptions?.onStep?.({ step });
      }
      for (const delta of ending) {
        if (this.status === "cancelled" || this.status === "error") break;
        await this.sendOptions?.onDelta?.({ update: delta });
        yield delta;
      }
      if (this.status === "running") {
        this.status = this.script.status ?? "finished";
      }
      if (this.status === "error") {
        throw new Error(this.script.errorMessage ?? "mock run error");
      }
    } finally {
      openListeners.delete(listenerId);
      openRuns.delete(this.id);
      this.completed = true;
    }
  }

  async *[Symbol.asyncIterator](): AsyncGenerator<MockDelta> {
    if (this.completed) return;
    yield* this.iterate();
  }

  stream(onDelta?: (d: MockDelta) => void): AsyncGenerator<MockDelta> {
    const self = this;
    return (async function* () {
      for await (const d of self) {
        onDelta?.(d);
        yield d;
      }
    })();
  }

  async wait(): Promise<{ status: string; usage?: Record<string, number> }> {
    if (!this.completed) {
      for await (const _ of this) {
        // drain once
      }
    }
    const result: { status: string; usage?: Record<string, number> } = { status: this.status };
    const usage =
      this.script.waitUsage ??
      (this.status === "finished"
        ? this.script.deltas?.find((d) => d.type === "turn-ended")?.usage
        : undefined);
    if (usage) result.usage = usage;
    return result;
  }

  async cancel(): Promise<void> {
    if (this.script.blockCancel) {
      throw new Error("cancel blocked");
    }
    if (this.script.hangCancel) {
      if (!this.cancelHangPromise) {
        this.cancelHangPromise = new Promise<void>((resolve) => {
          this.cancelHangResolve = resolve;
        });
      }
      return this.cancelHangPromise;
    }
    this.status = "cancelled";
    this.streamHangResolve?.();
    this.streamHangResolve = undefined;
  }

  /** Test helper: settle a hanging cancel() after the pool timeout path. */
  settleCancelForTest(): void {
    this.cancelHangResolve?.();
    this.cancelHangResolve = undefined;
  }
}

export class MockAgent {
  readonly id: string;
  readonly options: MockAgentOptions;
  private disposed = false;

  constructor(options: MockAgentOptions) {
    this.id = `agent-${nextAgent++}`;
    this.options = redact(options) as MockAgentOptions;
    openAgents.add(this.id);
  }

  async send(prompt: string, options?: MockSendOptions): Promise<MockRun> {
    if (this.disposed) throw new Error("agent disposed");
    let script: MockRunScript = {};
    for (const [key, value] of runScripts) {
      if (prompt.includes(key)) {
        script = value;
        break;
      }
    }
    const run = new MockRun(script, options);
    return run;
  }

  async [Symbol.asyncDispose](): Promise<void> {
    this.disposed = true;
    openAgents.delete(this.id);
  }

  /** Matches SDK 1.0.23 sync fallback; pool prefers asyncDispose. */
  close(): void {
    this.disposed = true;
    openAgents.delete(this.id);
  }
}

export const Cursor = {
  models: {
    async list(_opts: { apiKey?: string }): Promise<typeof scriptedModels> {
      return scriptedModels;
    },
  },
};

export const Agent = {
  async create(options: MockAgentOptions): Promise<MockAgent> {
    const local = options.local ?? {};
    if (local.enableAgentRetries !== false) {
      // Bridge must force false; mock records the value for assertions.
    }
    return new MockAgent({
      ...options,
      local: {
        ...local,
        settingSources: local.settingSources ?? [],
        autoReview: local.autoReview ?? false,
        enableAgentRetries: local.enableAgentRetries ?? false,
      },
    });
  },
  async resume(): Promise<never> {
    throw new Error("Agent.resume is excluded by design");
  },
};

export function assertNoOpenHandles(label = "handles"): void {
  const snap = openHandles();
  if (snap.agents || snap.runs || snap.listeners) {
    throw new Error(
      `${label}: open agents=${snap.agents} runs=${snap.runs} listeners=${snap.listeners}`,
    );
  }
}
