import assert from "node:assert/strict";
import { test } from "node:test";
import type { LocalAgentStore } from "@cursor/sdk";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";
import {
  LiveProbeDisabledError,
  LiveProbeTimeoutError,
  PINNED_LIVE_SDK_VERSION,
  runLiveProbe,
  type LiveProbeSdk,
  type ProbeAgent,
  type ProbeAgentCreateOptions,
  type ProbeRun,
} from "./liveProbeLib.js";

function mockSdk(overrides?: {
  deltas?: Array<{ type: string; text?: string; usage?: Record<string, number> }>;
  cancelSupported?: boolean;
  hangPhase?: "models.list" | "Agent.create" | "agent.send" | "run.wait";
  onCreate?: (opts: ProbeAgentCreateOptions) => void;
}): LiveProbeSdk {
  const deltas = overrides?.deltas ?? [
    { type: "thinking-delta", text: "plan" },
    { type: "text-delta", text: "ok" },
    {
      type: "turn-ended",
      usage: { inputTokens: 2, outputTokens: 3, totalTokens: 5, cacheReadTokens: 0, cacheWriteTokens: 0 },
    },
  ];
  let disposed = false;

  const makeRun = (mode: "normal" | "cancel"): ProbeRun => {
    let status: string = "running";
    const usage = {
      inputTokens: 4,
      outputTokens: 6,
      totalTokens: 10,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
    };
    return {
      id: mode === "cancel" ? "run-cancel" : "run-1",
      get status() {
        return status;
      },
      get usage() {
        return status === "finished" ? usage : undefined;
      },
      supports(op: string) {
        if (op === "cancel") return overrides?.cancelSupported !== false;
        return true;
      },
      async *stream(signal?: AbortSignal) {
        if (signal?.aborted) return;
        yield { type: "assistant" };
      },
      async wait(signal?: AbortSignal) {
        if (overrides?.hangPhase === "run.wait" && mode === "normal") {
          await new Promise(() => undefined);
        }
        if (signal?.aborted) throw new Error("aborted");
        if (status === "running") status = mode === "cancel" ? "cancelled" : "finished";
        return { status, usage: status === "finished" ? usage : undefined };
      },
      async cancel() {
        status = "cancelled";
      },
    };
  };

  const agent: ProbeAgent = {
    agentId: "agent-1",
    async send(prompt, options) {
      assert.ok(!/sk-|crsr_/.test(prompt));
      if (overrides?.hangPhase === "agent.send" && !prompt.includes("cancel")) {
        await new Promise(() => undefined);
      }
      if (prompt.includes("cancel")) {
        return makeRun("cancel");
      }
      await options?.onStep?.({ step: { type: "assistantMessage" } });
      for (const d of deltas) {
        await options?.onDelta?.({ update: d });
      }
      return makeRun("normal");
    },
    close() {},
    async [Symbol.asyncDispose]() {
      disposed = true;
    },
  };

  return {
    packageVersion: PINNED_LIVE_SDK_VERSION,
    Cursor: {
      models: {
        async list(opts) {
          assert.equal(typeof opts.apiKey, "string");
          assert.ok(opts.apiKey.length > 0);
          if (overrides?.hangPhase === "models.list") {
            await new Promise(() => undefined);
          }
          return [
            { id: "gpt-5.3-codex", displayName: "GPT-5.3 Codex" },
            { id: "composer-2-fast", displayName: "Composer 2 Fast" },
          ];
        },
      },
    },
    Agent: {
      async create(opts) {
        overrides?.onCreate?.(opts);
        if (overrides?.hangPhase === "Agent.create") {
          await new Promise(() => undefined);
        }
        assert.equal(opts.local.enableAgentRetries, false);
        assert.deepEqual(opts.local.settingSources, []);
        assert.equal(opts.local.sandboxOptions?.enabled, false);
        assert.equal(opts.local.autoReview, false);
        assert.ok(opts.local.cwd);
        assert.ok(opts.local.store);
        assert.ok(opts.local.store.agents);
        assert.ok(opts.local.store.runs);
        assert.ok(opts.local.store.checkpoints);
        assert.ok(opts.local.store.runEvents);
        assert.ok(opts.model.id);
        assert.ok(opts.apiKey);
        return agent;
      },
    },
    get disposed() {
      return disposed;
    },
  } as LiveProbeSdk & { disposed: boolean };
}

test("runLiveProbe disabled without opt-in", async () => {
  await assert.rejects(
    () =>
      runLiveProbe({
        env: { liveProbeEnabled: false, apiKey: "k", cwd: "/tmp", nodeVersion: "v22.13.0" },
        loadSdk: async () => mockSdk(),
      }),
    (err: unknown) => err instanceof LiveProbeDisabledError && err.exitCode === 2,
  );
});

test("runLiveProbe requires api key", async () => {
  await assert.rejects(
    () =>
      runLiveProbe({
        env: { liveProbeEnabled: true, cwd: "/tmp", nodeVersion: "v22.13.0" },
        loadSdk: async () => mockSdk(),
      }),
    (err: unknown) => err instanceof LiveProbeDisabledError,
  );
});

test("runLiveProbe covers full mocked workflow without network", async () => {
  const lines: string[] = [];
  let createdStore: LocalAgentStore | undefined;
  const sdk = mockSdk({
    onCreate: (opts) => {
      createdStore = opts.local.store;
    },
  });
  const store = createBridgeInMemoryLocalAgentStore();
  const summary = await runLiveProbe({
    env: {
      liveProbeEnabled: true,
      apiKey: "crsr_test_key_not_for_network",
      cwd: "/tmp/probe-ws",
      nodeVersion: "v22.13.0",
    },
    loadSdk: async () => sdk,
    createStore: () => store,
    log: (line) => lines.push(line),
  });

  assert.equal(summary.ok, true);
  assert.equal(summary.sdkVersion, "1.0.23");
  assert.equal(summary.modelCount, 2);
  assert.ok(summary.observedDeltaKinds.includes("text-delta"));
  assert.ok(summary.observedDeltaKinds.includes("thinking-delta"));
  assert.ok(summary.observedDeltaKinds.includes("turn-ended"));
  assert.ok(summary.observedStepTypes.includes("assistantMessage"));
  assert.ok(summary.turnUsageKeys.includes("inputTokens"));
  assert.ok(summary.cumulativeUsageKeys.includes("totalTokens"));
  assert.equal(summary.cancelStatus, "cancelled");
  assert.equal(summary.disposed, true);
  assert.equal(summary.usedInMemoryStore, true);
  assert.equal(createdStore, store);
  assert.equal((sdk as unknown as { disposed: boolean }).disposed, true);
  assert.equal(lines.length, 1);
  const printed = lines[0]!;
  assert.ok(!printed.includes("crsr_test_key"));
  assert.ok(!printed.includes("probe: reply"));
  assert.ok(!printed.includes("/tmp/probe-ws"));
});

test("runLiveProbe rejects wrong SDK pin", async () => {
  const sdk = mockSdk();
  (sdk as { packageVersion: string }).packageVersion = "9.9.9";
  await assert.rejects(
    () =>
      runLiveProbe({
        env: {
          liveProbeEnabled: true,
          apiKey: "k",
          cwd: "/tmp",
          nodeVersion: "v22.13.0",
        },
        loadSdk: async () => sdk,
      }),
    /expected @cursor\/sdk 1\.0\.23/,
  );
});

test("runLiveProbe times out hung models.list and cleans up", async () => {
  const errors: string[] = [];
  await assert.rejects(
    () =>
      runLiveProbe({
        env: {
          liveProbeEnabled: true,
          apiKey: "k",
          cwd: "/tmp",
          nodeVersion: "v22.13.0",
        },
        loadSdk: async () => mockSdk({ hangPhase: "models.list" }),
        timeouts: { totalMs: 200, phaseMs: 50 },
        logError: (line) => errors.push(line),
      }),
    (err: unknown) => err instanceof LiveProbeTimeoutError && /models\.list/.test(String(err)),
  );
  assert.ok(errors.some((e) => e.includes("timeout") && !e.includes("k=")));
});

test("runLiveProbe times out hung send and disposes agent", async () => {
  const sdk = mockSdk({ hangPhase: "agent.send" });
  await assert.rejects(
    () =>
      runLiveProbe({
        env: {
          liveProbeEnabled: true,
          apiKey: "k",
          cwd: "/tmp",
          nodeVersion: "v22.13.0",
        },
        loadSdk: async () => sdk,
        timeouts: { totalMs: 500, phaseMs: 40 },
      }),
    (err: unknown) => err instanceof LiveProbeTimeoutError,
  );
  assert.equal((sdk as unknown as { disposed: boolean }).disposed, true);
});
