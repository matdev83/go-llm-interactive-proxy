import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { test, beforeEach } from "node:test";
import {
  AgentPool,
  MAX_MCP_CONFIG_BYTES,
  mapSdkDelta,
  mapSdkStep,
  normalizeTurnUsage,
  validateAgentCreateParams,
} from "./agents.js";
import type { AgentCreateParams } from "./params.js";
import {
  RunSequencer,
  SCHEMA_VERSION,
  TYPE_EVENT,
  TYPE_RESPONSE,
  decodeLine,
  encodeFrame,
  TYPE_REQUEST,
} from "./protocol.js";
import { createBridgeServer, type SdkRuntime } from "./server.js";
import {
  resetSdkMock,
  Cursor as MockCursor,
  Agent as MockAgent,
  setRunScript,
  assertNoOpenHandles,
  openHandles,
  type MockAgentOptions,
} from "./sdkMock.js";

function baseCreateParams(overrides: Partial<AgentCreateParams> = {}): AgentCreateParams {
  return {
    apiKey: "crsr_test_key",
    model: { id: "gpt-5.3-codex", params: [{ id: "reasoning", value: "high" }] },
    local: { cwd: "/tmp/workspace" },
    settingSources: ["project", "user"],
    sandboxOptions: { enabled: true },
    autoReview: true,
    enableAgentRetries: true,
    mcpServers: { docs: { command: "echo", args: ["mcp"] } },
    ...overrides,
  };
}

beforeEach(() => {
  resetSdkMock();
});

test("validateAgentCreateParams rejects unknown settingSources and oversized MCP", () => {
  assert.throws(
    () => validateAgentCreateParams(baseCreateParams({ settingSources: ["ambient-home"] })),
    /unknown settingSources/,
  );
  const huge = "x".repeat(MAX_MCP_CONFIG_BYTES + 1);
  assert.throws(
    () => validateAgentCreateParams(baseCreateParams({ mcpServers: { pad: huge } })),
    /mcpServers exceeds/,
  );
});

test("mapSdkDelta maps text, reasoning, and turn-ended usage only", () => {
  assert.deepEqual(mapSdkDelta({ type: "text-delta", text: "Hi" }), {
    kind: "text_delta",
    payload: { text: "Hi" },
  });
  assert.deepEqual(mapSdkDelta({ type: "thinking-delta", text: "plan" }), {
    kind: "reasoning_delta",
    payload: { text: "plan" },
  });
  assert.deepEqual(
    mapSdkDelta({
      type: "turn-ended",
      usage: {
        inputTokens: 2,
        outputTokens: 3,
        totalTokens: 5,
        cacheReadTokens: 0,
        cacheWriteTokens: 0,
      },
    }),
    {
      kind: "usage",
      payload: {
        inputTokens: 2,
        outputTokens: 3,
        totalTokens: 5,
        cacheReadTokens: 0,
        cacheWriteTokens: 0,
      },
    },
  );
  assert.equal(mapSdkDelta({ type: "turn-ended" }), undefined);
});

test("mapSdkStep maps tool activity and warnings without leaking tool content", () => {
  assert.deepEqual(
    mapSdkStep({
      type: "toolCall",
      message: { type: "read", args: { path: "/secret/path" } },
    }),
    { kind: "activity", payload: { name: "read" } },
  );
  assert.deepEqual(
    mapSdkStep({
      type: "warning",
      message: { text: "slow tool" },
    }),
    { kind: "warning", payload: { message: "slow tool" } },
  );
  assert.equal(mapSdkStep({ type: "assistantMessage", message: { text: "hi" } }), undefined);
});

test("normalizeTurnUsage ignores cumulative run.wait usage shapes", () => {
  assert.deepEqual(
    normalizeTurnUsage({
      inputTokens: 1,
      outputTokens: 1,
      totalTokens: 2,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
    }),
    {
      inputTokens: 1,
      outputTokens: 1,
      totalTokens: 2,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
    },
  );
  assert.equal(normalizeTurnUsage(undefined), undefined);
});

test("AgentPool create maps SDK options with in-memory store and forced enableAgentRetries false", async () => {
  let captured: MockAgentOptions | undefined;
  const originalCreate = MockAgent.create;
  MockAgent.create = async (opts) => {
    captured = opts as MockAgentOptions;
    return originalCreate(opts);
  };

  const events: string[] = [];
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => {
      const store = {
        agents: {},
        checkpoints: {},
        runs: {},
        runEvents: {},
      };
      return store;
    },
    emitEvent: (line) => events.push(line),
  });

  const { agentId } = await pool.createAgent(baseCreateParams());
  assert.match(agentId, /^agent-/);
  assert.ok(captured);
  assert.equal(captured!.apiKey, "crsr_test_key");
  assert.equal(captured!.model?.id, "gpt-5.3-codex");
  assert.deepEqual((captured!.model as { params?: unknown }).params, [{ id: "reasoning", value: "high" }]);
  assert.equal(captured!.local?.cwd, "/tmp/workspace");
  assert.deepEqual(captured!.local?.settingSources, ["project", "user"]);
  assert.deepEqual(captured!.local?.sandboxOptions, { enabled: true });
  assert.equal(captured!.local?.autoReview, true);
  assert.equal(captured!.local?.enableAgentRetries, false);
  assert.ok(captured!.local?.store);
  assert.deepEqual(captured!.mcpServers, { docs: { command: "echo", args: ["mcp"] } });
  assert.equal(Object.prototype.hasOwnProperty.call(captured!, "customTools"), false);
  assert.equal(events.length, 0);
});

test("AgentPool send emits monotonic events and one finished terminal", async () => {
  setRunScript("stream-me", {
    steps: [
      { type: "toolCall", message: { type: "read_file", args: { path: "x" } } },
      { type: "warning", message: { text: "slow" } },
    ],
    deltas: [
      { type: "thinking-delta", text: "plan" },
      { type: "text-delta", text: "answer" },
      {
        type: "turn-ended",
        usage: { inputTokens: 1, outputTokens: 2, totalTokens: 3, cacheReadTokens: 0, cacheWriteTokens: 0 },
      },
    ],
  });

  const lines: string[] = [];
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: (line) => lines.push(line),
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "please stream-me now");
  assert.match(runId, /^run-/);

  await pool.waitForRun(runId);

  const events = lines.map((line) => decodeLine(line));
  assert.ok(events.every((f) => f.type === TYPE_EVENT));
  assert.deepEqual(
    events.map((f) => f.kind),
    ["reasoning_delta", "text_delta", "activity", "warning", "usage", "finished"],
  );
  const seq = new RunSequencer();
  for (const ev of events) seq.accept(ev);
  assert.equal(events.at(-1)?.kind, "finished");
  assert.equal((events.at(-1)?.payload as { status?: string }).status, "finished");
});

test("AgentPool send rejects busy agent until run completes", async () => {
  setRunScript("hold", {
    deltas: [{ type: "text-delta", text: "partial" }],
  });
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: () => {},
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const first = await pool.send(agentId, "hold");
  await assert.rejects(() => pool.send(agentId, "again"), /agent_busy/);
  await pool.waitForRun(first.runId);
  const second = await pool.send(agentId, "hold");
  assert.match(second.runId, /^run-/);
});

test("AgentPool does not emit cumulative usage from run.wait", async () => {
  setRunScript("usage-trap", {
    deltas: [
      {
        type: "turn-ended",
        usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2, cacheReadTokens: 0, cacheWriteTokens: 0 },
      },
    ],
    waitUsage: {
      inputTokens: 99,
      outputTokens: 99,
      totalTokens: 198,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
    },
  });

  const lines: string[] = [];
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: (line) => lines.push(line),
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "usage-trap");
  await pool.waitForRun(runId);
  const usageEvents = lines
    .map((line) => decodeLine(line))
    .filter((f) => f.kind === "usage")
    .map((f) => f.payload);
  assert.deepEqual(usageEvents, [
    { inputTokens: 1, outputTokens: 1, totalTokens: 2, cacheReadTokens: 0, cacheWriteTokens: 0 },
  ]);
});

function captureIO() {
  const stdin = new PassThrough();
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  let stdoutText = "";
  let stderrText = "";
  stdout.on("data", (chunk) => {
    stdoutText += chunk.toString("utf8");
  });
  stderr.on("data", (chunk) => {
    stderrText += chunk.toString("utf8");
  });
  return {
    stdin,
    stdout,
    stderr,
    get stdoutText() {
      return stdoutText;
    },
    get stderrText() {
      return stderrText;
    },
  };
}

async function startServer(loadSdk: () => Promise<SdkRuntime>) {
  const io = captureIO();
  const server = createBridgeServer({
    implVersion: "0.1.0",
    loadSdk,
    stdin: io.stdin,
    stdout: io.stdout,
    stderr: io.stderr,
  });
  const done = server.run();
  return { io, done };
}

function req(id: string, method: string, params: unknown): string {
  return encodeFrame({
    schemaVersion: SCHEMA_VERSION,
    type: TYPE_REQUEST,
    id,
    method,
    params,
  });
}

async function writeLine(io: ReturnType<typeof captureIO>, line: string): Promise<void> {
  io.stdin.write(`${line}\n`);
  await new Promise((resolve) => setImmediate(resolve));
}

async function roundTrip(io: ReturnType<typeof captureIO>, requestLine: string) {
  const before = io.stdoutText.length;
  const req = decodeLine(requestLine);
  await writeLine(io, requestLine);
  await new Promise((resolve) => setImmediate(resolve));
  const added = io.stdoutText.slice(before);
  const lines = added
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  const responseLine = lines.find((line) => {
    try {
      const frame = decodeLine(line);
      return frame.type === TYPE_RESPONSE && frame.id === req.id;
    } catch {
      return false;
    }
  });
  assert.ok(responseLine, `missing response for ${req.id}`);
  return decodeLine(responseLine);
}

async function waitForRunEvents(
  io: ReturnType<typeof captureIO>,
  runId: string,
  expectedKinds: string[],
): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    const events = io.stdoutText
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean)
      .map((line) => decodeLine(line))
      .filter((f) => f.type === TYPE_EVENT && f.runId === runId);
    if (events.length >= expectedKinds.length) {
      assert.deepEqual(
        events.map((f) => f.kind),
        expectedKinds,
      );
      const seq = new RunSequencer();
      for (const ev of events) seq.accept(ev);
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail(`timed out waiting for run events runId=${runId}`);
}

test("server agent/create and agent/send wire through AgentPool with protocol events", async () => {
  let captured: MockAgentOptions | undefined;
  const originalCreate = MockAgent.create;
  MockAgent.create = async (opts) => {
    captured = opts as MockAgentOptions;
    return originalCreate(opts);
  };

  setRunScript("hello-bridge", {
    deltas: [
      { type: "text-delta", text: "Hi" },
      {
        type: "turn-ended",
        usage: { inputTokens: 10, outputTokens: 2, totalTokens: 12, cacheReadTokens: 0, cacheWriteTokens: 0 },
      },
    ],
  });

  const { io, done } = await startServer(async () => ({
    packageVersion: "1.0.23",
    Cursor: MockCursor,
    Agent: MockAgent,
  }));

  await roundTrip(io, req("init-agents", "bridge/initialize", { implVersion: "0.1.0" }));

  const createResp = await roundTrip(
    io,
    req("create-1", "agent/create", {
      apiKey: "crsr_test_key",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: true,
    }),
  );
  assert.ok(createResp.result);
  const agentId = (createResp.result as { agentId: string }).agentId;
  assert.match(agentId, /^agent-/);
  assert.equal(captured?.local?.enableAgentRetries, false);
  assert.ok(captured?.local?.store);

  const sendResp = await roundTrip(
    io,
    req("send-1", "agent/send", { agentId, prompt: "hello-bridge" }),
  );
  assert.ok(sendResp.result);
  const runId = (sendResp.result as { runId: string }).runId;
  assert.match(runId, /^run-/);

  await waitForRunEvents(io, runId, ["text_delta", "usage", "finished"]);
  assert.ok(!io.stderrText.includes("crsr_test_key"));

  io.stdin.end();
  await done;
});

test("AgentPool cancelRun is idempotent and emits cancelled terminal", async () => {
  setRunScript("pool-cancel", {
    deltas: [
      { type: "text-delta", text: "a" },
      { type: "text-delta", text: "b" },
    ],
  });
  const lines: string[] = [];
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: (line) => lines.push(line),
    cancelTimeoutMs: 1000,
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "pool-cancel now");
  const cancelled = await pool.cancelRun(runId);
  assert.equal(cancelled.cancelled, true);
  await pool.waitForRun(runId);
  const again = await pool.cancelRun(runId);
  assert.equal(again.cancelled, true);
  const events = lines.map((line) => decodeLine(line));
  const terminal = events.find((f) => f.kind === "finished");
  assert.equal((terminal?.payload as { status?: string }).status, "cancelled");
});

test("AgentPool cancelRun timeout keeps stuck run tracked and invalidates agent", async () => {
  setRunScript("pool-hang", {
    deltas: [{ type: "text-delta", text: "x" }],
    hangCancel: true,
  });
  const lines: string[] = [];
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: (line) => lines.push(line),
    cancelTimeoutMs: 25,
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "pool-hang now");
  await assert.rejects(() => pool.cancelRun(runId), /cursor_sdk_cancel_timeout/);
  assert.equal(pool.trackedRunCount(), 1);
  assert.equal((await pool.cancelRun(runId)).cancelled, true);
  await assert.rejects(() => pool.send(agentId, "again"), /agent_invalidated/);
  const events = lines.map((line) => decodeLine(line));
  const terminals = events.filter((f) => f.kind === "error" || f.kind === "finished");
  assert.equal(terminals.length, 1);
  assert.equal((terminals[0]?.payload as { code?: string }).code, "cursor_sdk_cancel_timeout");
  assert.equal(openHandles().runs, 1);
  await assert.rejects(() => pool.disposeAgent(agentId), /cursor_sdk_dispose_timeout/);
  assert.equal(pool.trackedRunCount(), 1);
});

test("AgentPool cancel timeout with real-shaped handle (no abortLocally) supports late settlement", async () => {
  let releaseStream!: () => void;
  let settleCancel!: () => void;
  const streamGate = new Promise<void>((resolve) => {
    releaseStream = resolve;
  });
  const cancelGate = new Promise<void>((resolve) => {
    settleCancel = resolve;
  });
  const lines: string[] = [];
  const realShapedAgent = {
    async send() {
      const handle = {
        id: "run-real-1",
        status: "running" as string,
        async *stream() {
          await streamGate;
        },
        async wait() {
          await streamGate;
          return { status: "finished" };
        },
        async cancel() {
          await cancelGate;
        },
      };
      return handle;
    },
    async [Symbol.asyncDispose]() {},
  };
  const pool = new AgentPool({
    Agent: { create: async () => realShapedAgent },
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: (line) => lines.push(line),
    cancelTimeoutMs: 20,
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "real-shaped");
  assert.equal(runId, "run-real-1");
  await assert.rejects(() => pool.cancelRun(runId), /cursor_sdk_cancel_timeout/);
  assert.equal(pool.trackedRunCount(), 1);
  settleCancel();
  releaseStream();
  await new Promise((resolve) => setTimeout(resolve, 30));
  const terminals = lines.map((line) => decodeLine(line)).filter((f) => f.kind === "error" || f.kind === "finished");
  assert.equal(terminals.length, 1);
  assert.equal((terminals[0]?.payload as { code?: string }).code, "cursor_sdk_cancel_timeout");
  await assert.rejects(() => pool.send(agentId, "nope"), /agent_invalidated/);
});

test("AgentPool shutdown surfaces timeout when stuck run never ends", async () => {
  setRunScript("shutdown-hang", {
    deltas: [{ type: "text-delta", text: "x" }],
    hangCancel: true,
  });
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: () => {},
    cancelTimeoutMs: 20,
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  await pool.send(agentId, "shutdown-hang now");
  await assert.rejects(() => pool.shutdown(), /cursor_sdk_shutdown_timeout/);
  assert.equal(pool.trackedRunCount(), 1);
  await assert.rejects(() => pool.shutdown(), /cursor_sdk_shutdown_timeout/);
  await assert.rejects(() => pool.createAgent(baseCreateParams({ enableAgentRetries: false })), /bridge_shutting_down/);
});

test("AgentPool dispose prefers Symbol.asyncDispose and falls back to close", async () => {
  let asyncDisposed = 0;
  let closed = 0;
  const withAsync = {
    async send() {
      return {
        id: "run-async-disp",
        status: "finished",
        async *stream() {},
        async wait() {
          return { status: "finished" };
        },
        async cancel() {},
      };
    },
    async [Symbol.asyncDispose]() {
      asyncDisposed += 1;
    },
    close() {
      closed += 1;
    },
  };
  const closeOnly = {
    async send() {
      return {
        id: "run-close-only",
        status: "finished",
        async *stream() {},
        async wait() {
          return { status: "finished" };
        },
        async cancel() {},
      };
    },
    close() {
      closed += 1;
    },
  };
  const poolAsync = new AgentPool({
    Agent: { create: async () => withAsync },
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: () => {},
  });
  const { agentId: a1 } = await poolAsync.createAgent(baseCreateParams({ enableAgentRetries: false }));
  assert.equal((await poolAsync.disposeAgent(a1)).disposed, true);
  assert.equal(asyncDisposed, 1);
  assert.equal(closed, 0);

  const poolClose = new AgentPool({
    Agent: { create: async () => closeOnly },
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: () => {},
  });
  const { agentId: a2 } = await poolClose.createAgent(baseCreateParams({ enableAgentRetries: false }));
  assert.equal((await poolClose.disposeAgent(a2)).disposed, true);
  assert.equal(closed, 1);
});

test("AgentPool disposeAgent is idempotent and rejects unknown agents", async () => {
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: () => {},
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  assert.equal((await pool.disposeAgent(agentId)).disposed, true);
  assert.equal((await pool.disposeAgent(agentId)).disposed, true);
  await assert.rejects(() => pool.disposeAgent("agent-missing"), /unknown agentId/);
  assert.deepEqual(openHandles(), { agents: 0, runs: 0, listeners: 0 });
});

test("AgentPool shutdown disposes tracked agents only and leaves no open handles", async () => {
  setRunScript("shutdown-run", {
    deltas: [{ type: "text-delta", text: "hi" }],
  });
  const pool = new AgentPool({
    Agent: MockAgent,
    createStore: () => ({
      agents: {},
      checkpoints: {},
      runs: {},
      runEvents: {},
    }),
    emitEvent: () => {},
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "shutdown-run please");
  await pool.waitForRun(runId);
  await pool.shutdown();
  assertNoOpenHandles("pool shutdown");
  assert.equal((await pool.shutdown()).shutdown, true);
});

test("server run/cancel dispose and shutdown wire through AgentPool", async () => {
  const { io, done } = await startServer(async () => ({
    packageVersion: "1.0.23",
    Cursor: MockCursor,
    Agent: MockAgent,
  }));
  await roundTrip(io, req("init-stub", "bridge/initialize", { implVersion: "0.1.0" }));

  const createResp = await roundTrip(
    io,
    req("create-wire", "agent/create", {
      apiKey: "crsr_test_key",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  const agentId = (createResp.result as { agentId: string }).agentId;

  setRunScript("wire-cancel", {
    deltas: [{ type: "text-delta", text: "x" }],
  });
  const sendResp = await roundTrip(
    io,
    req("send-wire", "agent/send", { agentId, prompt: "wire-cancel" }),
  );
  const runId = (sendResp.result as { runId: string }).runId;

  const cancelResp = await roundTrip(io, req("cancel-wire", "run/cancel", { runId }));
  assert.equal((cancelResp.result as { cancelled: boolean }).cancelled, true);

  const disposeResp = await roundTrip(io, req("dispose-wire", "agent/dispose", { agentId }));
  assert.equal((disposeResp.result as { disposed: boolean }).disposed, true);

  const shutdownResp = await roundTrip(io, req("shutdown-wire", "bridge/shutdown", {}));
  assert.equal((shutdownResp.result as { shutdown: boolean }).shutdown, true);

  io.stdin.end();
  await done;
  assertNoOpenHandles("server wired shutdown");
});

test("late delta after terminal is dropped without crash and emits one terminal", async () => {
  let lateDelta!: (update: { type?: string; text?: string }) => void;
  const lines: string[] = [];
  const agent = {
    async send(
      _prompt: string,
      options?: { onDelta?: (args: { update: { type?: string; text?: string } }) => void },
    ) {
      lateDelta = (update) => options?.onDelta?.({ update });
      return {
        id: "run-late-1",
        status: "finished",
        async *stream() {
          options?.onDelta?.({ update: { type: "text-delta", text: "hi" } });
        },
        async wait() {
          return { status: "finished" };
        },
        async cancel() {},
      };
    },
    async [Symbol.asyncDispose]() {},
  };
  const pool = new AgentPool({
    Agent: { create: async () => agent },
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: (line) => lines.push(line),
  });
  const { agentId } = await pool.createAgent(baseCreateParams({ enableAgentRetries: false }));
  const { runId } = await pool.send(agentId, "late");
  await pool.waitForRun(runId);
  assert.doesNotThrow(() => lateDelta({ type: "text-delta", text: "after-terminal" }));
  const frames = lines.map((line) => decodeLine(line));
  const terminals = frames.filter((f) => f.kind === "finished" || f.kind === "error");
  assert.equal(terminals.length, 1);
  assert.equal(terminals[0]?.kind, "finished");
  assert.ok(!frames.some((f) => f.kind === "text_delta" && (f.payload as { text?: string }).text === "after-terminal"));
});

test("run error events redact exact apiKey and sk/crsr patterns", async () => {
  const apiKey = "exact-secret-api-key-value";
  const sk = "sk-abcdefghijklmnopqrstuvwx";
  const crsr = "crsr_abcdefghijklmnop";
  const lines: string[] = [];
  const agent = {
    async send() {
      return {
        id: "run-redact-1",
        status: "error",
        async *stream() {},
        async wait() {
          return {
            status: "error",
            error: { message: `boom key=${apiKey} token=${sk} cred=${crsr}` },
          };
        },
        async cancel() {},
      };
    },
    async [Symbol.asyncDispose]() {},
  };
  const pool = new AgentPool({
    Agent: { create: async () => agent },
    createStore: () => ({ agents: {}, checkpoints: {}, runs: {}, runEvents: {} }),
    emitEvent: (line) => lines.push(line),
  });
  const { agentId } = await pool.createAgent(
    baseCreateParams({ apiKey, enableAgentRetries: false, sandboxOptions: { enabled: false } }),
  );
  const { runId } = await pool.send(agentId, "redact");
  await pool.waitForRun(runId);
  const joined = lines.join("\n");
  assert.ok(!joined.includes(apiKey));
  assert.ok(!joined.includes(sk));
  assert.ok(!joined.includes("sk-"));
  assert.ok(!joined.includes(crsr));
  assert.ok(!joined.includes("crsr_"));
  const err = lines.map((l) => decodeLine(l)).find((f) => f.kind === "error");
  assert.ok(err);
  assert.ok(((err?.payload as { message?: string }).message ?? "").includes("[REDACTED]"));
});
