import assert from "node:assert/strict";
import { test } from "node:test";
import type { LocalAgentStore } from "@cursor/sdk";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";
import {
  boundCall,
  completeRun,
  LIVE_REUSE_TOKEN_FIRST,
  LIVE_REUSE_TOKEN_SECOND,
  LIVE_TEXT_ONLY_PREFIX,
  LiveScenariosDisabledError,
  LiveScenariosTimeoutError,
  livePromptHasTextOnlyDeny,
  PINNED_LIVE_SDK_VERSION,
  runLiveScenarios,
  runWorkspaceCleanup,
  sanitizeLiveScenariosError,
  withTimeout,
  type LiveScenariosSdk,
  type ScenarioAgent,
  type ScenarioAgentCreateOptions,
  type ScenarioRun,
} from "./liveScenariosLib.js";

function isReuseTrackedPrompt(prompt: string): boolean {
  return (
    prompt.includes(LIVE_REUSE_TOKEN_FIRST) || prompt.includes(LIVE_REUSE_TOKEN_SECOND)
  );
}

function isReuseFirstPrompt(prompt: string): boolean {
  return prompt.includes(LIVE_REUSE_TOKEN_FIRST);
}

const LIVE_SCENARIO_ORDER = [
  "discovery",
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
] as const;

function mockSdk(overrides?: {
  reasoningModel?: boolean;
  sandboxFails?: boolean;
  hangPhase?: string;
  sendError?: Error;
  streamError?: Error;
  waitError?: Error;
  reasoningBodyError?: Error;
  /** After cancel(), subsequent Agent.send stalls (process-global poison). */
  poisonAfterCancel?: boolean;
  trackDispose?: { count: number };
  createdModels?: ScenarioAgentCreateOptions["model"][];
  trackRun?: {
    cancel: number;
    wait: number;
    streamReturn: number;
    streamCalls: number;
    sends: number;
    agentId: string;
    prompts?: string[];
  };
}): LiveScenariosSdk {
  let disposed = false;
  let sendCount = 0;
  let lastPrompt = "";
  let cancelPoisoned = false;

  const makeRun = (mode: "normal" | "cancel" | "reuse"): ScenarioRun => {
    let status = "running";
    let releaseHang: (() => void) | undefined;
    return {
      id: `run-${mode}`,
      get status() {
        return status;
      },
      supports(op: string) {
        return op === "cancel";
      },
      async *stream() {
        const reusePrompt = isReuseTrackedPrompt(lastPrompt);
        if (overrides?.trackRun && reusePrompt) {
          overrides.trackRun.streamCalls += 1;
        }
        try {
          if (overrides?.hangPhase === "reuse.stream1" && isReuseFirstPrompt(lastPrompt)) {
            await new Promise<void>((resolve) => {
              releaseHang = resolve;
            });
          }
          if (overrides?.streamError && mode === "normal") {
            throw overrides.streamError;
          }
          yield { type: "assistant" };
        } finally {
          if (overrides?.trackRun && isReuseFirstPrompt(lastPrompt)) {
            overrides.trackRun.streamReturn += 1;
          }
        }
      },
      async wait() {
        const reusePrompt = isReuseTrackedPrompt(lastPrompt);
        if (overrides?.trackRun && reusePrompt) {
          overrides.trackRun.wait += 1;
        }
        if (overrides?.hangPhase === "reuse.wait1" && isReuseFirstPrompt(lastPrompt)) {
          await new Promise<void>(() => undefined);
        }
        if (overrides?.waitError && mode === "normal") {
          throw overrides.waitError;
        }
        status = mode === "cancel" ? "cancelled" : "finished";
        return { status };
      },
      async cancel() {
        if (overrides?.trackRun && isReuseFirstPrompt(lastPrompt)) {
          overrides.trackRun.cancel += 1;
        }
        status = "cancelled";
        if (overrides?.poisonAfterCancel) {
          cancelPoisoned = true;
        }
        releaseHang?.();
        releaseHang = undefined;
      },
    };
  };

  const agent: ScenarioAgent = {
    agentId: "agent-1",
    async send(prompt, options) {
      sendCount += 1;
      lastPrompt = prompt;
      if (overrides?.trackRun?.prompts) {
        overrides.trackRun.prompts.push(prompt);
      }
      if (overrides?.trackRun && isReuseTrackedPrompt(prompt)) {
        overrides.trackRun.sends += 1;
        overrides.trackRun.agentId = agent.agentId;
      }
      if (overrides?.poisonAfterCancel && cancelPoisoned) {
        await new Promise(() => undefined);
      }
      if (overrides?.hangPhase === "agent.send") {
        await new Promise(() => undefined);
      }
      if (overrides?.sendError && prompt.includes("reply with ok")) {
        throw overrides.sendError;
      }
      if (overrides?.reasoningBodyError && prompt.includes("brief plan")) {
        throw overrides.reasoningBodyError;
      }
      if (prompt.includes("cancel")) {
        return makeRun("cancel");
      }
      await options?.onStep?.({ step: { type: "assistantMessage" } });
      if (prompt.includes("reasoning") || prompt.includes("brief plan")) {
        await options?.onDelta?.({ update: { type: "thinking-delta", text: "plan" } });
      }
      await options?.onDelta?.({ update: { type: "text-delta", text: "ok" } });
      await options?.onDelta?.({
        update: {
          type: "turn-ended",
          usage: { inputTokens: 1, outputTokens: 2, totalTokens: 3 },
        },
      });
      return makeRun(sendCount > 1 ? "reuse" : "normal");
    },
    close() {},
    async [Symbol.asyncDispose]() {
      disposed = true;
      if (overrides?.trackDispose) overrides.trackDispose.count += 1;
    },
  };

  return {
    packageVersion: PINNED_LIVE_SDK_VERSION,
    platform: "linux",
    Cursor: {
      models: {
        async list(opts) {
          assert.ok(opts.apiKey);
          if (overrides?.hangPhase === "models.list") {
            await new Promise(() => undefined);
          }
          const models = [
            { id: "gpt-5.3-codex", displayName: "GPT-5.3 Codex" },
            {
              id: "reasoning-model",
              displayName: "Reasoning",
              parameters: [{ id: "reasoning", values: ["low", "high"] }],
            },
          ];
          return overrides?.reasoningModel === false
            ? [{ id: "gpt-5.3-codex", displayName: "GPT-5.3 Codex" }]
            : models;
        },
      },
    },
    Agent: {
      async create(opts: ScenarioAgentCreateOptions) {
        if (overrides?.sandboxFails && opts.local.sandboxOptions?.enabled) {
          throw new Error("sandbox unavailable on this platform");
        }
        if (overrides?.hangPhase === "Agent.create") {
          await new Promise(() => undefined);
        }
        if (opts.model.params !== undefined) {
          if (!Array.isArray(opts.model.params)) {
            throw new Error("(intermediate value)(intermediate value)(intermediate value).map is not a function");
          }
          for (const p of opts.model.params) {
            assert.equal(typeof p.id, "string");
            assert.equal(typeof p.value, "string");
          }
        }
        assert.equal(opts.local.enableAgentRetries, false);
        assert.ok(opts.local.store);
        overrides?.createdModels?.push(opts.model);
        return agent;
      },
    },
    get disposed() {
      return disposed;
    },
  } as LiveScenariosSdk & { disposed: boolean };
}

test("runLiveScenarios blocked without opt-in", async () => {
  await assert.rejects(
    () =>
      runLiveScenarios({
        env: { liveEnabled: false, apiKey: "k", nodeVersion: "v22.13.0" },
        loadSdk: async () => mockSdk(),
        mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
      }),
    (err: unknown) => err instanceof LiveScenariosDisabledError && err.exitCode === 2,
  );
});

test("runLiveScenarios requires api key when opted in", async () => {
  await assert.rejects(
    () =>
      runLiveScenarios({
        env: { liveEnabled: true, nodeVersion: "v22.13.0" },
        loadSdk: async () => mockSdk(),
        mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
      }),
    (err: unknown) => err instanceof LiveScenariosDisabledError,
  );
});

test("runLiveScenarios covers mocked scenarios without network", async () => {
  const lines: string[] = [];
  let store: LocalAgentStore | undefined;
  const sdk = mockSdk();
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "crsr_test_key_not_for_network",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => sdk,
    createStore: () => {
      store = createBridgeInMemoryLocalAgentStore();
      return store;
    },
    mkWorkspace: () => ({ cwd: "/tmp/live-ws", cleanup: async () => undefined }),
    log: (line) => lines.push(line),
  });

  assert.equal(summary.ok, false);
  assert.equal(summary.status, "blocked");
  assert.equal(summary.sdkVersion, PINNED_LIVE_SDK_VERSION);
  const names = summary.scenarios.map((s) => s.name);
  assert.deepEqual(names, [...LIVE_SCENARIO_ORDER]);
  const reasoning = summary.scenarios.find((s) => s.name === "reasoning");
  assert.ok(reasoning);
  assert.equal(reasoning!.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "hard_bridge_restart")?.status, "blocked");
  assert.equal(summary.scenarios.find((s) => s.name === "canonical_rebootstrap")?.status, "blocked");

  const printed = lines.join("\n");
  assert.ok(!printed.includes("crsr_test_key"));
  assert.ok(!printed.includes("/tmp/live-ws"));
  assert.equal((sdk as unknown as { disposed: boolean }).disposed, true);
  assert.ok(store);
});

test("runLiveScenarios awaits Promise mkWorkspace and runs cleanup", async () => {
  const labels: string[] = [];
  const cleaned: string[] = [];
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk(),
    mkWorkspace: async (label) => {
      // Regression: factory must be awaited; sync assignment of Promise broke cwd/cleanup.
      await Promise.resolve();
      labels.push(label);
      return {
        cwd: `/tmp/async-${label}`,
        cleanup: async () => {
          cleaned.push(label);
        },
      };
    },
  });
  assert.equal(summary.ok, false);
  assert.equal(summary.status, "blocked");
  assert.ok(labels.includes("text"));
  assert.ok(labels.includes("shutdown"));
  assert.deepEqual(cleaned.slice().sort(), labels.slice().sort());
  assert.equal(summary.scenarios.find((s) => s.name === "hard_bridge_restart")?.status, "blocked");
  assert.equal(summary.scenarios.find((s) => s.name === "canonical_rebootstrap")?.status, "blocked");
});

test("runLiveScenarios runs injected process lifecycle hooks", async () => {
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk(),
    mkWorkspace: async (label) => ({ cwd: `/tmp/${label}`, cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "crash+restart",
      canonicalRebootstrap: async () => "marker gen advanced",
    },
  });
  assert.equal(summary.ok, true);
  assert.equal(summary.status, "complete");
  assert.equal(summary.scenarios.find((s) => s.name === "hard_bridge_restart")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "canonical_rebootstrap")?.status, "passed");
});

test("runLiveScenarios skips reasoning when unsupported still complete with hooks", async () => {
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ reasoningModel: false }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  const reasoning = summary.scenarios.find((s) => s.name === "reasoning");
  assert.ok(reasoning);
  assert.equal(reasoning!.status, "skipped");
  assert.equal(summary.ok, true);
  assert.equal(summary.status, "complete");
});

test("runLiveScenarios sandbox required unavailable is overall blocked", async () => {
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ sandboxFails: true }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  assert.equal(summary.ok, false);
  assert.equal(summary.status, "blocked");
  assert.equal(summary.scenarios.find((s) => s.name === "workspace_safety_required")?.status, "blocked");
});

test("runLiveScenarios times out hung phase", async () => {
  const logs: string[] = [];
  const errors: string[] = [];
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ hangPhase: "models.list" }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    timeouts: { totalMs: 2000, phaseMs: 50, settleGraceMs: 50, cleanupBoundMs: 50 },
    log: (line) => logs.push(line),
    logError: (line) => errors.push(line),
  });
  assert.equal(summary.ok, false);
  assert.equal(summary.status, "failed");
  assert.ok(Array.isArray(summary.scenarios));
  assert.equal(summary.scenarios.find((s) => s.name === "discovery")?.status, "failed");
  assert.ok(summary.scenarios.find((s) => s.name === "discovery")?.detail?.includes("timeout"));
  assert.ok(summary.scenarios.some((s) => s.name === "shutdown"));
  assert.equal(logs.length + errors.length, 1, "exactly one sanitized JSON failure envelope");
  const payload = JSON.parse([...logs, ...errors][0]!);
  assert.equal(payload.status, "failed");
  assert.equal(payload.ok, false);
  assert.ok(Array.isArray(payload.scenarios));
  assert.ok(payload.scenarios.length >= 2);
  assert.ok(!JSON.stringify(payload).includes("crsr_"));
  assert.ok(!JSON.stringify(payload).includes("C:\\Users"));
});

test("reasoning create uses SDK ModelParameterValue[] params shape", async () => {
  const createdModels: ScenarioAgentCreateOptions["model"][] = [];
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ createdModels }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  const reasoning = summary.scenarios.find((s) => s.name === "reasoning");
  assert.equal(reasoning?.status, "passed");
  const withParams = createdModels.find((m) => m.params !== undefined);
  assert.ok(withParams);
  assert.ok(Array.isArray(withParams!.params));
  assert.deepEqual(withParams!.params, [{ id: "reasoning", value: "low" }]);
});

test("live scenario execution order records reuse before cancellation", async () => {
  const executed: string[] = [];
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk(),
    mkWorkspace: (label) => {
      executed.push(label);
      return { cwd: `/tmp/${label}`, cleanup: async () => undefined };
    },
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  const names = summary.scenarios.map((s) => s.name);
  assert.deepEqual(names, [...LIVE_SCENARIO_ORDER]);
  const reuseIdx = names.indexOf("reuse");
  const cancelIdx = names.indexOf("cancellation");
  assert.ok(reuseIdx >= 0 && cancelIdx >= 0);
  assert.ok(reuseIdx < cancelIdx, "reuse must precede cancellation");
  // Workspace labels: reuse before cancel (isolated dirs).
  const wsReuse = executed.indexOf("reuse");
  const wsCancel = executed.indexOf("cancel");
  assert.ok(wsReuse >= 0 && wsCancel >= 0);
  assert.ok(wsReuse < wsCancel, "reuse workspace must be created before cancel workspace");
});

test("cancel poison: old cancel-then-reuse stalls; suite order keeps both passing", async () => {
  // Old destructive order: cancel poisons process-global send → reuse stalls.
  const poisoned = mockSdk({ poisonAfterCancel: true });
  const store = createBridgeInMemoryLocalAgentStore();
  const agent = await poisoned.Agent.create({
    apiKey: "k",
    model: { id: "gpt-5.3-codex" },
    local: {
      cwd: "/tmp/poison-old",
      store,
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    },
  });
  try {
    const cancelRun = await agent.send("live: cancel target");
    await cancelRun.cancel();
    const ac = new AbortController();
    await assert.rejects(
      () =>
        withTimeout("poisoned.reuse.send", 40, ac.signal, async () => agent.send("live: first turn"), 10),
      (err: unknown) => err instanceof LiveScenariosTimeoutError,
    );
  } finally {
    await agent[Symbol.asyncDispose]();
  }

  // New suite order: reuse precedes cancellation → both pass despite poison-after-cancel.
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ poisonAfterCancel: true }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    timeouts: { totalMs: 10_000, phaseMs: 200, cleanupBoundMs: 100, settleGraceMs: 50 },
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  const names = summary.scenarios.map((s) => s.name);
  assert.ok(names.indexOf("reuse") < names.indexOf("cancellation"));
  assert.equal(summary.scenarios.find((s) => s.name === "reuse")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "cancellation")?.status, "passed");
  assert.equal(summary.ok, true);
  assert.equal(summary.status, "complete");
});

test("reuse proves two wait-only sends on same agent without stream", async () => {
  const track = { count: 0 };
  const trackRun = { cancel: 0, wait: 0, streamReturn: 0, streamCalls: 0, sends: 0, agentId: "" };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ trackDispose: track, trackRun }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  assert.equal(summary.scenarios.find((s) => s.name === "reuse")?.status, "passed");
  assert.equal(trackRun.streamCalls, 0, "reuse must not call run.stream");
  assert.equal(trackRun.wait, 2, "reuse must wait twice");
  assert.equal(trackRun.sends, 2, "reuse must send twice");
  assert.equal(trackRun.agentId, "agent-1", "both sends on same agent");
  assert.ok(track.count >= 1, "reuse must dispose agent");
});

test("reuse prompts are text-only exact tokens with deny semantics", async () => {
  const trackRun = {
    cancel: 0,
    wait: 0,
    streamReturn: 0,
    streamCalls: 0,
    sends: 0,
    agentId: "",
    prompts: [] as string[],
  };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ trackRun }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  const reuse = summary.scenarios.find((s) => s.name === "reuse");
  assert.equal(reuse?.status, "passed");
  assert.equal(trackRun.sends, 2);
  assert.equal(trackRun.wait, 2);
  assert.equal(trackRun.streamCalls, 0);
  const reusePrompts = trackRun.prompts.filter(isReuseTrackedPrompt);
  assert.equal(reusePrompts.length, 2);
  const p1 = reusePrompts[0];
  const p2 = reusePrompts[1];
  if (p1 === undefined || p2 === undefined) {
    assert.fail("expected two captured reuse prompts");
  }
  assert.ok(livePromptHasTextOnlyDeny(p1), "first reuse prompt missing text-only deny");
  assert.ok(livePromptHasTextOnlyDeny(p2), "second reuse prompt missing text-only deny");
  assert.ok(p1.includes(LIVE_REUSE_TOKEN_FIRST), "first reuse prompt missing exact token");
  assert.ok(p2.includes(LIVE_REUSE_TOKEN_SECOND), "second reuse prompt missing exact token");
  assert.notEqual(LIVE_REUSE_TOKEN_FIRST, LIVE_REUSE_TOKEN_SECOND);
  assert.ok(!p1.includes(LIVE_REUSE_TOKEN_SECOND));
  assert.ok(!p2.includes(LIVE_REUSE_TOKEN_FIRST));
  assert.ok(!p1.includes("first turn") && !p1.includes("second turn"));
  assert.ok(!p2.includes("first turn") && !p2.includes("second turn"));
  const raw = JSON.stringify(summary);
  assert.ok(!raw.includes(LIVE_REUSE_TOKEN_FIRST), "summary must not leak reuse tokens");
  assert.ok(!raw.includes(LIVE_REUSE_TOKEN_SECOND), "summary must not leak reuse tokens");
  assert.ok(!raw.includes(LIVE_TEXT_ONLY_PREFIX), "summary must not leak prompt prefix");
});

test("phase timeout on reuse wait does not abort later scenarios", async () => {
  const track = { count: 0 };
  const trackRun = { cancel: 0, wait: 0, streamReturn: 0, streamCalls: 0, sends: 0, agentId: "" };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk({ hangPhase: "reuse.wait1", trackDispose: track, trackRun }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    timeouts: { totalMs: 5000, phaseMs: 50, cleanupBoundMs: 100, settleGraceMs: 200 },
    processLifecycle: {
      hardBridgeRestart: async () => "ok",
      canonicalRebootstrap: async () => "ok",
    },
  });
  assert.equal(summary.scenarios.find((s) => s.name === "reuse")?.status, "failed");
  assert.ok(summary.scenarios.find((s) => s.name === "reuse")?.detail?.includes("timeout"));
  assert.equal(trackRun.streamCalls, 0, "reuse timeout path must not stream");
  assert.equal(trackRun.wait, 1, "hung wait must start once");
  assert.equal(summary.scenarios.find((s) => s.name === "hard_bridge_restart")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "canonical_rebootstrap")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "shutdown")?.status, "passed");
  assert.ok(track.count >= 1, "hung reuse must dispose agent");
});

test("completeRun abort cancels once, returns iterator, skips wait", async () => {
  const track = { cancel: 0, wait: 0, streamReturn: 0, finally: 0 };
  let releaseHang: (() => void) | undefined;
  const run: ScenarioRun = {
    id: "run-hang",
    supports: (op) => op === "cancel",
    stream() {
      return (async function* () {
        try {
          await new Promise<void>((resolve) => {
            releaseHang = resolve;
          });
          yield { type: "assistant" };
        } finally {
          track.finally += 1;
          track.streamReturn += 1;
        }
      })();
    },
    async wait() {
      track.wait += 1;
      return { status: "finished" };
    },
    async cancel() {
      track.cancel += 1;
      releaseHang?.();
      releaseHang = undefined;
    },
  };
  const ac = new AbortController();
  const done = completeRun(run, ac.signal);
  await new Promise((r) => setTimeout(r, 20));
  ac.abort();
  await assert.rejects(done, (err: unknown) => err instanceof LiveScenariosTimeoutError);
  assert.equal(track.cancel, 1);
  assert.equal(track.wait, 0);
  assert.equal(track.streamReturn, 1);
  assert.equal(track.finally, 1);
});

test("completeRun cancel error does not mask timeout", async () => {
  const track = { wait: 0 };
  let releaseHang: (() => void) | undefined;
  const run: ScenarioRun = {
    id: "run-cancel-throws",
    supports: (op) => op === "cancel",
    stream() {
      return (async function* () {
        await new Promise<void>((resolve) => {
          releaseHang = resolve;
        });
        yield { type: "assistant" };
      })();
    },
    async wait() {
      track.wait += 1;
      return { status: "finished" };
    },
    async cancel() {
      releaseHang?.();
      releaseHang = undefined;
      throw new Error("cancel boom apiKey=crsr_secret");
    },
  };
  const ac = new AbortController();
  const done = completeRun(run, ac.signal);
  await new Promise((r) => setTimeout(r, 20));
  ac.abort();
  await assert.rejects(done, (err: unknown) => {
    assert.ok(err instanceof LiveScenariosTimeoutError);
    assert.ok(!String(err).includes("crsr_secret"));
    assert.ok(!String(err).includes("cancel boom"));
    return true;
  });
  assert.equal(track.wait, 0);
});

test("completeRun normal path waits after stream completes", async () => {
  const track = { cancel: 0, wait: 0 };
  const run: ScenarioRun = {
    id: "run-ok",
    supports: (op) => op === "cancel",
    async *stream() {
      yield { type: "assistant" };
    },
    async wait() {
      track.wait += 1;
      return { status: "finished" };
    },
    async cancel() {
      track.cancel += 1;
    },
  };
  await completeRun(run, new AbortController().signal);
  assert.equal(track.wait, 1);
  assert.equal(track.cancel, 0);
});

test("boundCall clears timeout when work wins", async () => {
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const realSet = globalThis.setTimeout;
  const realClear = globalThis.clearTimeout;
  globalThis.setTimeout = ((fn: (...args: unknown[]) => void, ms?: number, ...args: unknown[]) => {
    const id = realSet(fn, ms, ...args);
    timers.add(id);
    return id;
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((id?: ReturnType<typeof setTimeout>) => {
    if (id !== undefined) timers.delete(id);
    return realClear(id);
  }) as typeof clearTimeout;
  try {
    await boundCall(async () => undefined, 5_000);
    assert.equal(timers.size, 0, "boundCall must clear timer when work wins");
  } finally {
    globalThis.setTimeout = realSet;
    globalThis.clearTimeout = realClear;
  }
});

test("withTimeout awaits cleanup before returning and has zero unhandledRejection", async () => {
  const order: string[] = [];
  let unhandled = 0;
  const onUnhandled = () => {
    unhandled += 1;
  };
  process.on("unhandledRejection", onUnhandled);
  let releaseHang: (() => void) | undefined;
  const run: ScenarioRun = {
    id: "run-settle",
    supports: (op) => op === "cancel",
    stream() {
      return (async function* () {
        await new Promise<void>((resolve) => {
          releaseHang = resolve;
        });
        yield { type: "assistant" };
      })();
    },
    async wait() {
      return { status: "finished" };
    },
    async cancel() {
      await new Promise((r) => setTimeout(r, 40));
      order.push("cleanup");
      releaseHang?.();
      releaseHang = undefined;
    },
  };
  try {
    await assert.rejects(
      withTimeout(
        "phase.settle",
        20,
        new AbortController().signal,
        async (signal) => completeRun(run, signal, { cleanupBoundMs: 200 }),
        200,
      ),
      (err: unknown) => err instanceof LiveScenariosTimeoutError,
    );
    order.push("timeout-returned");
    await new Promise((r) => setTimeout(r, 30));
    assert.equal(unhandled, 0);
    assert.deepEqual(order, ["cleanup", "timeout-returned"]);
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
});

test("hung cancel and return stay within cleanup bounds", async () => {
  const run: ScenarioRun = {
    id: "run-hung-cleanup",
    supports: (op) => op === "cancel",
    async *stream() {
      await new Promise(() => undefined);
      yield { type: "assistant" };
    },
    async wait() {
      return { status: "finished" };
    },
    async cancel() {
      await new Promise(() => undefined);
    },
  };
  const started = Date.now();
  await assert.rejects(
    withTimeout(
      "phase.hung",
      20,
      new AbortController().signal,
      async (signal) => completeRun(run, signal, { cleanupBoundMs: 40 }),
      100,
    ),
    (err: unknown) => err instanceof LiveScenariosTimeoutError,
  );
  assert.ok(Date.now() - started < 350, "cleanup must be bounded");
});

test("runLiveScenarios loadSdk failure emits exactly one sanitized failed JSON", async () => {
  const logs: string[] = [];
  const errors: string[] = [];
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "crsr_secret_load_fail",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => {
      throw new Error(
        "failed to import @cursor/sdk apiKey=crsr_secret_load_fail at C:\\Users\\secret-user\\bridge",
      );
    },
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
    log: (line) => logs.push(line),
    logError: (line) => errors.push(line),
  });
  assert.equal(summary.ok, false);
  assert.equal(summary.status, "failed");
  assert.deepEqual(summary.scenarios, []);
  assert.equal(logs.length + errors.length, 1, "exactly one sanitized JSON failure envelope");
  const payload = JSON.parse([...logs, ...errors][0]!);
  assert.equal(payload.ok, false);
  assert.equal(payload.status, "failed");
  assert.ok(Array.isArray(payload.scenarios));
  assert.equal(payload.scenarios.length, 0);
  assert.ok(typeof payload.error === "string");
  const raw = JSON.stringify(payload);
  assert.ok(!raw.includes("crsr_secret_load_fail"));
  assert.ok(!raw.includes("secret-user"));
  assert.ok(!raw.includes("C:\\Users"));
});

test("workspace cleanup EBUSY does not abort later mandatory scenarios", async () => {
  const logs: string[] = [];
  const errors: string[] = [];
  const secretPath = "C:\\Users\\secret-user\\AppData\\Local\\Temp\\lip-live-reasoning-abc";
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "crsr_live_cleanup_secret",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => mockSdk(),
    mkWorkspace: (label) => ({
      cwd: label === "reasoning" ? secretPath : `/tmp/${label}`,
      cleanup: async () => {
        if (label === "reasoning") {
          const err = new Error(`EBUSY: resource busy or locked, rmdir '${secretPath}'`);
          (err as NodeJS.ErrnoException).code = "EBUSY";
          throw err;
        }
      },
    }),
    log: (line) => logs.push(line),
    logError: (line) => errors.push(line),
  });

  const names = summary.scenarios.map((s) => s.name);
  for (const expected of ["reuse", "hard_bridge_restart", "canonical_rebootstrap", "shutdown"]) {
    assert.ok(names.includes(expected), `missing later scenario ${expected}`);
  }
  const reasoning = summary.scenarios.find((s) => s.name === "reasoning");
  assert.ok(reasoning);
  assert.equal(reasoning!.status, "failed");
  assert.ok(reasoning!.detail?.startsWith("workspace cleanup failed:"));
  assert.ok(reasoning!.detail?.includes("[path]"));
  assert.ok(!reasoning!.detail?.includes(secretPath));
  assert.ok(!reasoning!.detail?.includes("crsr_live_cleanup_secret"));
  assert.ok(!reasoning!.detail?.includes("secret-user"));
  assert.equal(summary.scenarios.find((s) => s.name === "reuse")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "shutdown")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "hard_bridge_restart")?.status, "blocked");
  assert.equal(summary.status, "failed");
  assert.equal(summary.ok, false);

  const all = [...logs, ...errors].join("\n");
  assert.ok(!all.includes("crsr_live_cleanup_secret"));
  assert.ok(!all.includes(secretPath));
  assert.ok(!all.includes("secret-user"));
  assert.equal(logs.length + errors.length, 1, "exactly one authoritative summary JSON line");
  const payload = JSON.parse([...logs, ...errors][0]!);
  assert.ok(Array.isArray(payload.scenarios));
  assert.ok(payload.scenarios.length >= 10);
});

test("runWorkspaceCleanup retries transient EBUSY then succeeds", async () => {
  let attempts = 0;
  const sleeps: number[] = [];
  const result = await runWorkspaceCleanup(
    async () => {
      attempts += 1;
      if (attempts < 3) {
        const err = new Error("EBUSY: resource busy or locked, rmdir 'C:\\\\Temp\\\\x'");
        (err as NodeJS.ErrnoException).code = "EBUSY";
        throw err;
      }
    },
    {
      sleep: async (ms) => {
        sleeps.push(ms);
      },
      maxAttempts: 4,
    },
  );
  assert.equal(result.ok, true);
  assert.equal(result.attempts, 3);
  assert.equal(attempts, 3);
  assert.equal(sleeps.length, 2);
});

test("runWorkspaceCleanup nontransient ENOENT reports attempts=1 without retry", async () => {
  let attempts = 0;
  let slept = 0;
  const result = await runWorkspaceCleanup(
    async () => {
      attempts += 1;
      const err = new Error("ENOENT: no such file or directory, rmdir 'C:\\Users\\secret-user\\gone'");
      (err as NodeJS.ErrnoException).code = "ENOENT";
      throw err;
    },
    {
      maxAttempts: 4,
      sleep: async () => {
        slept += 1;
      },
    },
  );
  assert.equal(result.ok, false);
  if (!result.ok) {
    assert.equal(result.attempts, 1);
    assert.equal(attempts, 1);
    assert.equal(slept, 0);
    assert.ok(result.detail.startsWith("workspace cleanup failed:"));
    assert.ok(result.detail.includes("[path]"));
    assert.ok(!result.detail.includes("secret-user"));
    assert.ok(!result.detail.includes("C:\\Users"));
  }
});

test("runWorkspaceCleanup retries EPERM only on win32", async () => {
  let linuxAttempts = 0;
  const linux = await runWorkspaceCleanup(
    async () => {
      linuxAttempts += 1;
      const err = new Error("EPERM: operation not permitted, rmdir 'C:\\Users\\secret-user\\tmp'");
      (err as NodeJS.ErrnoException).code = "EPERM";
      throw err;
    },
    { maxAttempts: 3, platform: "linux", sleep: async () => undefined },
  );
  assert.equal(linux.ok, false);
  assert.equal(linuxAttempts, 1);
  if (!linux.ok) assert.equal(linux.attempts, 1);

  let winAttempts = 0;
  const win = await runWorkspaceCleanup(
    async () => {
      winAttempts += 1;
      if (winAttempts < 2) {
        const err = new Error("EPERM: operation not permitted, rmdir 'C:\\Users\\secret-user\\tmp'");
        (err as NodeJS.ErrnoException).code = "EPERM";
        throw err;
      }
    },
    { maxAttempts: 3, platform: "win32", sleep: async () => undefined },
  );
  assert.equal(win.ok, true);
  assert.equal(winAttempts, 2);
  assert.equal(win.attempts, 2);
});

test("runWorkspaceCleanup sanitizes persistent cleanup failure", async () => {
  const result = await runWorkspaceCleanup(async () => {
    const err = new Error(
      "EPERM: operation not permitted, rmdir 'C:\\Users\\secret-user\\tmp' apiKey=crsr_should_redact",
    );
    (err as NodeJS.ErrnoException).code = "EPERM";
    throw err;
  }, { maxAttempts: 2, platform: "win32", sleep: async () => undefined });
  assert.equal(result.ok, false);
  if (!result.ok) {
    assert.equal(result.attempts, 2);
    assert.ok(result.detail.startsWith("workspace cleanup failed:"));
    assert.ok(result.detail.includes("[path]"));
    assert.ok(result.detail.includes("apiKey=[REDACTED]") || result.detail.includes("[REDACTED]"));
    assert.ok(!result.detail.includes("secret-user"));
    assert.ok(!result.detail.includes("crsr_should_redact"));
    assert.ok(!result.detail.includes("C:\\Users"));
  }
});

test("sanitizeLiveScenariosError redacts keys and paths", () => {
  const out = sanitizeLiveScenariosError(
    new Error("boom at C:\\Users\\secret-user\\ws apiKey=crsr_leak sk-abc123"),
  );
  assert.ok(!out.includes("secret-user"));
  assert.ok(!out.includes("crsr_leak"));
  assert.ok(!out.includes("sk-abc123"));
  assert.ok(out.includes("[path]") || out.includes("[REDACTED]"));
});

test("scenario body SKIP wins over cleanup EBUSY and later scenarios continue", async () => {
  const secretPath = "C:\\Users\\secret-user\\AppData\\Local\\Temp\\lip-live-reasoning-skip";
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "crsr_body_over_cleanup",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () =>
      mockSdk({
        reasoningBodyError: new Error("SKIP: model did not emit thinking-delta"),
      }),
    mkWorkspace: (label) => ({
      cwd: label === "reasoning" ? secretPath : `/tmp/${label}`,
      cleanup: async () => {
        if (label === "reasoning") {
          const err = new Error(`EBUSY: resource busy or locked, rmdir '${secretPath}'`);
          (err as NodeJS.ErrnoException).code = "EBUSY";
          throw err;
        }
      },
    }),
  });
  const reasoning = summary.scenarios.find((s) => s.name === "reasoning");
  assert.ok(reasoning);
  assert.equal(reasoning!.status, "skipped");
  assert.ok(!reasoning!.detail?.includes(secretPath));
  assert.ok(!reasoning!.detail?.includes("workspace cleanup failed"));
  assert.equal(summary.scenarios.find((s) => s.name === "reuse")?.status, "passed");
  assert.equal(summary.scenarios.find((s) => s.name === "shutdown")?.status, "passed");
});

test("text scenario disposes agent when send fails", async () => {
  const track = { count: 0 };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () =>
      mockSdk({
        sendError: new Error("send exploded"),
        trackDispose: track,
      }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
  });
  assert.equal(summary.scenarios.find((s) => s.name === "text")?.status, "failed");
  assert.ok(track.count >= 1, "agent must be disposed after send failure");
  assert.ok(summary.scenarios.some((s) => s.name === "shutdown"));
});

test("text scenario disposes agent when stream fails", async () => {
  const track = { count: 0 };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () =>
      mockSdk({
        streamError: new Error("stream exploded at C:\\Users\\secret-user\\x"),
        trackDispose: track,
      }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
  });
  const text = summary.scenarios.find((s) => s.name === "text");
  assert.equal(text?.status, "failed");
  assert.ok(!text?.detail?.includes("secret-user"));
  assert.ok(track.count >= 1, "agent must be disposed after stream failure");
  assert.ok(summary.scenarios.some((s) => s.name === "shutdown"));
});

test("text scenario disposes agent when wait fails", async () => {
  const track = { count: 0 };
  const summary = await runLiveScenarios({
    env: {
      liveEnabled: true,
      apiKey: "k",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () =>
      mockSdk({
        waitError: new Error("wait exploded"),
        trackDispose: track,
      }),
    mkWorkspace: () => ({ cwd: "/tmp/ws", cleanup: async () => undefined }),
  });
  assert.equal(summary.scenarios.find((s) => s.name === "text")?.status, "failed");
  assert.ok(track.count >= 1, "agent must be disposed after wait failure");
  assert.ok(summary.scenarios.some((s) => s.name === "shutdown"));
});
