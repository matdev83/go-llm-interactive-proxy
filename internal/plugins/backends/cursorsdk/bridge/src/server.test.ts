import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { test, beforeEach } from "node:test";
import { readFixtureJSON } from "./fixtures.js";
import {
  decodeLine,
  encodeFrame,
  matchResponse,
  METHODS,
  PINNED_SDK_VERSION,
  SCHEMA_VERSION,
  TYPE_REQUEST,
  TYPE_RESPONSE,
} from "./protocol.js";
import { createBridgeServer, type BridgeServer, type SdkRuntime } from "./server.js";
import { resetSdkMock, Cursor as MockCursor, Agent as MockAgent, setRunScript } from "./sdkMock.js";

interface CapturedIO {
  stdin: PassThrough;
  stdout: PassThrough;
  stderr: PassThrough;
  stdoutText: string;
  stderrText: string;
}

function captureIO(): CapturedIO {
  const stdin = new PassThrough();
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  let stdoutText = "";
  let stderrText = "";
  stdout.on("data", (chunk: Buffer | string) => {
    stdoutText += chunk.toString("utf8");
  });
  stderr.on("data", (chunk: Buffer | string) => {
    stderrText += chunk.toString("utf8");
  });
  return { stdin, stdout, stderr, get stdoutText() { return stdoutText; }, get stderrText() { return stderrText; } };
}

async function startServer(
  opts: Parameters<typeof createBridgeServer>[0] = {},
): Promise<{ server: BridgeServer; io: CapturedIO; done: Promise<void> }> {
  const io = captureIO();
  const server = createBridgeServer({
    implVersion: "0.1.0",
    nodeVersion: "22.13.0",
    ...opts,
    stdin: io.stdin,
    stdout: io.stdout,
    stderr: io.stderr,
  });
  const done = server.run();
  return { server, io, done };
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

async function writeLine(io: CapturedIO, line: string): Promise<void> {
  io.stdin.write(`${line}\n`);
  await new Promise((resolve) => setImmediate(resolve));
}

async function readStdoutLines(io: CapturedIO): Promise<string[]> {
  return io.stdoutText
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
}

/** Extract request id without strict protocol validation (for incompatible-input tests). */
function requestIdFromLine(requestLine: string): string {
  const parsed = JSON.parse(requestLine) as { id?: unknown };
  assert.equal(typeof parsed.id, "string");
  return parsed.id as string;
}

async function awaitResponseFrame(
  io: CapturedIO,
  before: number,
  requestId: string,
  timeoutMs = 2000,
): Promise<ReturnType<typeof decodeLine>> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const added = io.stdoutText.slice(before);
    const lines = added
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);
    const responseLine = lines.find((line) => {
      try {
        const frame = decodeLine(line);
        return frame.type === TYPE_RESPONSE && frame.id === requestId;
      } catch {
        return false;
      }
    });
    if (responseLine) {
      const frame = decodeLine(responseLine);
      assert.equal(frame.type, TYPE_RESPONSE);
      return frame;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`missing response for ${requestId}`);
}

async function roundTrip(
  io: CapturedIO,
  requestLine: string,
  timeoutMs = 2000,
): Promise<ReturnType<typeof decodeLine>> {
  const before = io.stdoutText.length;
  const request = decodeLine(requestLine);
  await writeLine(io, requestLine);
  return awaitResponseFrame(io, before, request.id!, timeoutMs);
}

/**
 * Round-trip for deliberately invalid requests: do not decode the request with
 * production validateFrame, but still decode the schema v1 error response strictly.
 */
async function roundTripIncompatibleRequest(
  io: CapturedIO,
  requestLine: string,
): Promise<ReturnType<typeof decodeLine>> {
  const before = io.stdoutText.length;
  const requestId = requestIdFromLine(requestLine);
  await writeLine(io, requestLine);
  const frame = await awaitResponseFrame(io, before, requestId);
  assert.equal(frame.schemaVersion, SCHEMA_VERSION);
  return frame;
}

function mockRuntime(overrides: Partial<SdkRuntime> = {}): SdkRuntime {
  return {
    packageVersion: PINNED_SDK_VERSION,
    Cursor: MockCursor,
    Agent: MockAgent,
    ...overrides,
  };
}

beforeEach(() => {
  resetSdkMock();
});

test("lazy SDK load: loader not called until initialize", async () => {
  let loadCalls = 0;
  const { io, done } = await startServer({
    loadSdk: async () => {
      loadCalls += 1;
      return mockRuntime();
    },
  });
  assert.equal(loadCalls, 0);

  const initResp = await roundTrip(
    io,
    req("init-1", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  assert.equal(loadCalls, 1);
  assert.ok(initResp.result);
  assert.equal((initResp.result as { sdkVersion: string }).sdkVersion, PINNED_SDK_VERSION);

  const healthResp = await roundTrip(io, req("health-1", "bridge/health", {}));
  assert.equal(loadCalls, 1);
  assert.equal((healthResp.result as { ok: boolean }).ok, true);

  io.stdin.end();
  await done;
});

test("initialize reports exact package and runtime versions with capabilities", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ packageVersion: "1.0.23", sandboxSupported: true }),
    implVersion: "0.1.0",
    nodeVersion: "22.14.1",
    detectPlatform: () => "linux",
  });

  const resp = await roundTrip(
    io,
    req("init-2", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  const result = resp.result as {
    schemaVersion: number;
    implVersion: string;
    sdkVersion: string;
    nodeVersion: string;
    capabilities: string[];
    sandboxSupported: boolean;
  };
  assert.equal(result.schemaVersion, SCHEMA_VERSION);
  assert.equal(result.implVersion, "0.1.0");
  assert.equal(result.sdkVersion, "1.0.23");
  assert.equal(result.nodeVersion, "22.14.1");
  assert.deepEqual(result.capabilities, [...METHODS]);
  assert.equal(result.sandboxSupported, true);

  io.stdin.end();
  await done;
});

test("initialize sandboxSupported is false without verified SDK capability even on non-win32", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ packageVersion: "1.0.23" }),
    detectPlatform: () => "linux",
  });

  const resp = await roundTrip(
    io,
    req("init-no-cap", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  const result = resp.result as { sandboxSupported: boolean };
  assert.equal(result.sandboxSupported, false);

  io.stdin.end();
  await done;
});

test("initialize sandboxSupported follows verified SDK capability on win32", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ packageVersion: "1.0.23", sandboxSupported: true }),
    detectPlatform: () => "win32",
  });

  const resp = await roundTrip(
    io,
    req("init-win-cap", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  const result = resp.result as { sandboxSupported: boolean };
  assert.equal(result.sandboxSupported, true);

  io.stdin.end();
  await done;
});

test("initialize sandboxSupported is false when probe fails", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ packageVersion: "1.0.23", sandboxSupported: true }),
    detectPlatform: () => "linux",
    probeSandboxSupported: async () => {
      throw new Error("probe exploded");
    },
  });

  const resp = await roundTrip(
    io,
    req("init-probe-fail", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  const result = resp.result as { sandboxSupported: boolean };
  assert.equal(result.sandboxSupported, false);

  io.stdin.end();
  await done;
});

test("rejects incompatible schema without SDK load or model discovery", async () => {
  let loadCalls = 0;
  let listCalls = 0;
  const { io, done } = await startServer({
    loadSdk: async () => {
      loadCalls += 1;
      return mockRuntime({
        Cursor: {
          models: {
            async list() {
              listCalls += 1;
              return [];
            },
          },
        },
      });
    },
  });

  const badReq =
    '{"schemaVersion":2,"type":"request","id":"bad-1","method":"bridge/initialize","params":{"implVersion":"0.1.0"}}';
  assert.throws(() => decodeLine(badReq), /incompatible_version/);
  const resp = await roundTripIncompatibleRequest(io, badReq);
  assert.ok(resp.error);
  assert.equal(resp.error!.code, "incompatible_version");
  assert.equal(loadCalls, 0);

  const modelsResp = await roundTrip(
    io,
    req("models-1", "models/list", { apiKey: "secret-api-key-value" }),
  );
  assert.ok(modelsResp.error);
  assert.equal(listCalls, 0);
  assert.equal(loadCalls, 0);
  assert.ok(!io.stderrText.includes("secret-api-key-value"));

  io.stdin.end();
  await done;
});

test("missing or failed SDK load returns safe error without secrets on stderr", async () => {
  const secret = "super-secret-sdk-key-abc";
  const { io, done } = await startServer({
    loadSdk: async () => {
      throw new Error(`failed to load @cursor/sdk apiKey=${secret}`);
    },
  });

  const resp = await roundTrip(
    io,
    req("init-3", "bridge/initialize", { implVersion: "0.1.0" }),
  );
  assert.ok(resp.error);
  assert.equal(resp.error!.code, "sdk_load_failed");
  assert.ok(!resp.error!.message.includes(secret));
  assert.ok(!io.stderrText.includes(secret));
  if (io.stderrText.length > 0) {
    assert.ok(io.stderrText.length <= 8192);
  }

  io.stdin.end();
  await done;
});

test("models/list uses SDK API and normalizes rows from fixture", async () => {
  const fixture = readFixtureJSON<{
    models: Array<{
      id: string;
      displayName: string;
      parameters: Array<{ id: string; type?: string; values?: string[] }>;
      variants: Array<{ id: string; params: Record<string, unknown> }>;
    }>;
  }>("models_sanitized.json");

  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
  });

  await roundTrip(io, req("init-4", "bridge/initialize", { implVersion: "0.1.0" }));

  const resp = await roundTrip(
    io,
    req("models-2", "models/list", { apiKey: "crsr_test_key" }),
  );
  assert.ok(resp.result);
  const models = (resp.result as { models: typeof fixture.models }).models;
  assert.equal(models.length, fixture.models.length);
  for (const expected of fixture.models) {
    const row = models.find((m) => m.id === expected.id);
    assert.ok(row, expected.id);
    assert.equal(row!.displayName, expected.displayName);
    assert.deepEqual(row!.parameters, expected.parameters);
    assert.deepEqual(row!.variants, expected.variants);
  }
  assert.ok(!io.stderrText.includes("crsr_test_key"));

  io.stdin.end();
  await done;
});

test("stdout stays protocol-only; diagnostics go to bounded stderr", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
    logDiagnostic: (line) => line,
  });

  await roundTrip(io, req("init-5", "bridge/initialize", { implVersion: "0.1.0" }));
  await roundTrip(io, req("health-2", "bridge/health", {}));

  const stdoutLines = await readStdoutLines(io);
  for (const line of stdoutLines) {
    assert.ok(line.startsWith("{"), `non-protocol stdout: ${line.slice(0, 40)}`);
    const frame = decodeLine(line);
    assert.ok(frame.type === TYPE_RESPONSE || frame.type === "event");
  }
  if (io.stderrText.length > 0) {
    assert.ok(io.stderrText.length <= 8192);
    for (const line of stdoutLines) {
      assert.ok(!line.includes("diagnostic"));
    }
  }

  io.stdin.end();
  await done;
});

test("run/cancel agent/dispose and bridge/shutdown are implemented with idempotent semantics", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
    cancelTimeoutMs: 50,
  });

  await roundTrip(io, req("init-6", "bridge/initialize", { implVersion: "0.1.0" }));

  const createResp = await roundTrip(
    io,
    req("create-cancel", "agent/create", {
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

  setRunScript("cancel-flow", {
    deltas: [
      { type: "text-delta", text: "partial" },
      { type: "text-delta", text: "more" },
    ],
  });
  const sendResp = await roundTrip(
    io,
    req("send-cancel", "agent/send", { agentId, prompt: "cancel-flow please" }),
  );
  const runId = (sendResp.result as { runId: string }).runId;

  const cancelResp = await roundTrip(io, req("cancel-1", "run/cancel", { runId }));
  assert.ok(cancelResp.result);
  assert.equal((cancelResp.result as { cancelled: boolean }).cancelled, true);

  const cancelAgain = await roundTrip(io, req("cancel-2", "run/cancel", { runId }));
  assert.equal((cancelAgain.result as { cancelled: boolean }).cancelled, true);

  const disposeResp = await roundTrip(io, req("dispose-1", "agent/dispose", { agentId }));
  assert.equal((disposeResp.result as { disposed: boolean }).disposed, true);

  const disposeAgain = await roundTrip(io, req("dispose-2", "agent/dispose", { agentId }));
  assert.equal((disposeAgain.result as { disposed: boolean }).disposed, true);

  const unknownAgent = await roundTrip(io, req("dispose-unknown", "agent/dispose", { agentId: "agent-missing" }));
  assert.ok(unknownAgent.error);
  assert.equal(unknownAgent.error!.code, "unknown_agent");

  io.stdin.end();
  await done;
});

test("run/cancel returns cursor_sdk_cancel_timeout when SDK cancel hangs", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
    cancelTimeoutMs: 30,
  });

  await roundTrip(io, req("init-timeout", "bridge/initialize", { implVersion: "0.1.0" }));
  const createResp = await roundTrip(
    io,
    req("create-timeout", "agent/create", {
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

  setRunScript("hang-cancel", {
    deltas: [{ type: "text-delta", text: "x" }],
    hangCancel: true,
  });
  const sendResp = await roundTrip(
    io,
    req("send-timeout", "agent/send", { agentId, prompt: "hang-cancel now" }),
  );
  const runId = (sendResp.result as { runId: string }).runId;

  const cancelResp = await roundTrip(io, req("cancel-timeout", "run/cancel", { runId }));
  assert.ok(cancelResp.error, `expected cancel timeout error, got ${JSON.stringify(cancelResp)}`);
  assert.equal(cancelResp.error!.code, "cursor_sdk_cancel_timeout");

  // Late cancel must stay idempotent; hanging SDK cancel must not surface late.
  const cancelAgain = await roundTrip(io, req("cancel-timeout-2", "run/cancel", { runId }));
  assert.equal((cancelAgain.result as { cancelled: boolean }).cancelled, true);

  // Stuck run remains owned; shutdown must surface timeout instead of claiming clean handles.
  const shutdownResp = await roundTrip(io, req("shutdown-timeout", "bridge/shutdown", {}));
  assert.ok(shutdownResp.error, `expected shutdown timeout, got ${JSON.stringify(shutdownResp)}`);
  assert.equal(shutdownResp.error!.code, "cursor_sdk_shutdown_timeout");

  const shutdownAgain = await roundTrip(io, req("shutdown-timeout-2", "bridge/shutdown", {}));
  assert.ok(shutdownAgain.error, `expected idempotent stuck shutdown error, got ${JSON.stringify(shutdownAgain)}`);
  assert.equal(shutdownAgain.error!.code, "cursor_sdk_shutdown_timeout");

  io.stdin.end();
  await done;
  const { openHandles } = await import("./sdkMock.js");
  assert.ok(openHandles().runs >= 1, "stuck cancel-timeout run must remain tracked");
});

test("bridge/shutdown rejects new work disposes tracked resources and is idempotent", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
  });

  await roundTrip(io, req("init-shutdown", "bridge/initialize", { implVersion: "0.1.0" }));
  const createResp = await roundTrip(
    io,
    req("create-shutdown", "agent/create", {
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

  const shutdownResp = await roundTrip(io, req("shutdown-1", "bridge/shutdown", {}));
  assert.equal((shutdownResp.result as { shutdown: boolean }).shutdown, true);

  const shutdownAgain = await roundTrip(io, req("shutdown-2", "bridge/shutdown", {}));
  assert.equal((shutdownAgain.result as { shutdown: boolean }).shutdown, true);

  const rejected = await roundTrip(
    io,
    req("after-shutdown", "agent/create", {
      apiKey: "crsr_test_key",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  assert.ok(rejected.error);
  assert.equal(rejected.error!.code, "bridge_shutting_down");

  io.stdin.end();
  await done;
  const { assertNoOpenHandles } = await import("./sdkMock.js");
  assertNoOpenHandles("after bridge shutdown");
  void agentId;
});

test("unexpected SDK exceptions become safe bounded errors without secrets", async () => {
  const secret = "crsr_super_secret_key_xyz";
  const throwingAgent = {
    async create() {
      throw new Error(`agent boom apiKey=${secret}`);
    },
  };
  const io = captureIO();
  const server = createBridgeServer({
    implVersion: "0.1.0",
    loadSdk: async () => ({
      packageVersion: PINNED_SDK_VERSION,
      Cursor: MockCursor,
      Agent: throwingAgent,
    }),
    stdin: io.stdin,
    stdout: io.stdout,
    stderr: io.stderr,
  });
  const done = server.run();
  await roundTrip(io, req("init-exc", "bridge/initialize", { implVersion: "0.1.0" }));
  const resp = await roundTrip(
    io,
    req("create-exc", "agent/create", {
      apiKey: secret,
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  assert.ok(resp.error);
  assert.equal(resp.error!.code, "agent_create_failed");
  assert.ok(!resp.error!.message.includes(secret));
  assert.ok(!io.stderrText.includes(secret));
  if (io.stderrText.length > 0) {
    assert.ok(io.stderrText.length <= 8192);
  }
  io.stdin.end();
  await done;
});

test("initialize response correlates with request", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
  });

  const requestLine = req("corr-1", "bridge/initialize", { implVersion: "0.1.0" });
  const response = await roundTrip(io, requestLine);
  matchResponse(decodeLine(requestLine), response);

  io.stdin.end();
  await done;
});

test("models/list never invokes Cursor CLI subprocess", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime(),
  });
  await roundTrip(io, req("init-7", "bridge/initialize", { implVersion: "0.1.0" }));
  await roundTrip(io, req("models-3", "models/list", { apiKey: "k" }));

  const combined = `${io.stdoutText}\n${io.stderrText}`.toLowerCase();
  assert.ok(!combined.includes("cursor agent"));
  assert.ok(!combined.includes("--list-models"));
  assert.ok(!combined.includes("spawn("));

  io.stdin.end();
  await done;
});

test("run/cancel reaches runtime while agent/send handler is blocked", async () => {
  let releaseSend!: () => void;
  const sendGate = new Promise<void>((resolve) => {
    releaseSend = resolve;
  });
  let cancelCalls = 0;
  const Agent = {
    async create() {
      return {
        async send() {
          await sendGate;
          return {
            id: "run-blocked-1",
            status: "finished",
            async *stream() {},
            async wait() {
              return { status: "finished" };
            },
            async cancel() {
              cancelCalls += 1;
            },
          };
        },
        async [Symbol.asyncDispose]() {},
      };
    },
  };
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ Agent }),
  });
  await roundTrip(io, req("init-conc", "bridge/initialize", { implVersion: "0.1.0" }));
  const created = await roundTrip(
    io,
    req("create-conc", "agent/create", {
      apiKey: "k",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  const agentId = (created.result as { agentId: string }).agentId;

  const sendBefore = io.stdoutText.length;
  await writeLine(io, req("send-conc", "agent/send", { agentId, prompt: "hang" }));
  await new Promise((r) => setTimeout(r, 30));

  const cancelResp = await roundTrip(io, req("cancel-conc", "run/cancel", { runId: "missing-run" }), 1000);
  assert.ok(cancelResp.result, "run/cancel must respond while agent/send is still pending");
  assert.equal((cancelResp.result as { cancelled: boolean }).cancelled, true);

  const sendStillPending = !io.stdoutText
    .slice(sendBefore)
    .split(/\r?\n/)
    .some((line) => {
      try {
        const f = decodeLine(line);
        return f.type === TYPE_RESPONSE && f.id === "send-conc";
      } catch {
        return false;
      }
    });
  assert.equal(sendStillPending, true);

  releaseSend();
  const sendResp = await awaitResponseFrame(io, sendBefore, "send-conc", 2000);
  assert.ok(sendResp.result);
  assert.equal(cancelCalls, 0);

  io.stdin.end();
  await done;
});

test("bridge/shutdown rejects new work and awaits in-flight drain", async () => {
  let releaseSend!: () => void;
  const sendGate = new Promise<void>((resolve) => {
    releaseSend = resolve;
  });
  const Agent = {
    async create() {
      return {
        async send() {
          await sendGate;
          return {
            id: "run-drain-1",
            status: "finished",
            async *stream() {},
            async wait() {
              return { status: "finished" };
            },
            async cancel() {},
          };
        },
        async [Symbol.asyncDispose]() {},
      };
    },
  };
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ Agent }),
  });
  await roundTrip(io, req("init-drain", "bridge/initialize", { implVersion: "0.1.0" }));
  const created = await roundTrip(
    io,
    req("create-drain", "agent/create", {
      apiKey: "k",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  const agentId = (created.result as { agentId: string }).agentId;
  const sendBefore = io.stdoutText.length;
  await writeLine(io, req("send-drain", "agent/send", { agentId, prompt: "hang" }));
  await new Promise((r) => setTimeout(r, 20));

  const shutBefore = io.stdoutText.length;
  await writeLine(io, req("shut-drain", "bridge/shutdown", {}));
  await new Promise((r) => setTimeout(r, 20));

  const rejected = await roundTrip(
    io,
    req("create-after-shut", "agent/create", {
      apiKey: "k",
      model: { id: "gpt-5.3-codex" },
      local: { cwd: "/tmp/ws" },
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    }),
  );
  assert.ok(rejected.error);
  assert.equal(rejected.error!.code, "bridge_shutting_down");

  releaseSend();
  const shutResp = await awaitResponseFrame(io, shutBefore, "shut-drain", 2000);
  assert.ok(shutResp.result);
  await awaitResponseFrame(io, sendBefore, "send-drain", 2000);

  io.stdin.end();
  await done;
});

test("concurrent responses keep request-id correlation without frame corruption", async () => {
  const { io, done } = await startServer({
    loadSdk: async () => mockRuntime({ sandboxSupported: true }),
  });
  await roundTrip(io, req("init-corr", "bridge/initialize", { implVersion: "0.1.0" }));

  const ids = ["h1", "h2", "h3", "h4", "h5"];
  await Promise.all(ids.map((id) => writeLine(io, req(id, "bridge/health", {}))));
  const seen = new Set<string>();
  const deadline = Date.now() + 2000;
  while (seen.size < ids.length && Date.now() < deadline) {
    for (const line of io.stdoutText.split(/\r?\n/)) {
      if (!line.trim()) continue;
      try {
        const f = decodeLine(line);
        if (f.type === TYPE_RESPONSE && f.id && ids.includes(f.id)) {
          matchResponse(decodeLine(req(f.id, "bridge/health", {})), f);
          seen.add(f.id);
        }
      } catch {
        assert.fail(`corrupt stdout frame: ${line}`);
      }
    }
    await new Promise((r) => setTimeout(r, 5));
  }
  assert.equal(seen.size, ids.length);

  io.stdin.end();
  await done;
});
