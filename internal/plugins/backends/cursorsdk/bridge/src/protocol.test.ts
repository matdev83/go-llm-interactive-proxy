import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { readFixture, readFixtureJSON, fixtureRoot } from "./fixtures.js";
import {
  decodeLine,
  encodeFrame,
  matchResponse,
  MAX_FRAME_BYTES,
  MIN_NODE_ENGINE,
  METHODS,
  EVENT_KINDS,
  PINNED_SDK_VERSION,
  ProtocolError,
  RunSequencer,
  SCHEMA_VERSION,
  TYPE_EVENT,
  TYPE_REQUEST,
  TYPE_RESPONSE,
} from "./protocol.js";
import {
  collectParamSecrets,
  decodeMethodParams,
  formatParamError,
  safeErrorBody,
} from "./params.js";

test("pinned SDK contract fixture", () => {
  const contract = readFixtureJSON<{
    sdkVersion: string;
    nodeEngine: string;
    apis: string[];
    deltaKinds: string[];
    excluded: string[];
    unsupportedPlatforms: string[];
    defaultsToOverride: { enableAgentRetries: boolean };
  }>("sdk_contract.json");
  assert.equal(contract.sdkVersion, PINNED_SDK_VERSION);
  assert.equal(contract.nodeEngine, MIN_NODE_ENGINE);
  assert.ok(contract.apis.includes("Cursor.models.list"));
  assert.ok(contract.deltaKinds.includes("text-delta"));
  assert.ok(contract.excluded.includes("Agent.resume"));
  assert.equal(contract.defaultsToOverride.enableAgentRetries, false);
  assert.ok(contract.unsupportedPlatforms.includes("win32-arm64"));
});

test("sanitized model fixtures preserve reasoning distinctions", () => {
  const fixture = readFixtureJSON<{
    sdkVersion: string;
    models: Array<{
      id: string;
      displayName: string;
      parameters: Array<{ id: string; values?: string[] }>;
      variants: Array<{ id: string; params: Record<string, unknown> }>;
    }>;
    distinctions: Record<string, string>;
  }>("models_sanitized.json");
  assert.equal(fixture.sdkVersion, PINNED_SDK_VERSION);
  const byId = new Map(fixture.models.map((m) => [m.id, m]));
  const reasoningId = fixture.distinctions.reasoningParam;
  const effortId = fixture.distinctions.effortPlusThinking;
  const boolOnlyId = fixture.distinctions.booleanThinkingOnly;
  assert.ok(reasoningId && effortId && boolOnlyId);
  const reasoning = byId.get(reasoningId);
  const effort = byId.get(effortId);
  const boolOnly = byId.get(boolOnlyId);
  assert.ok(reasoning && effort && boolOnly);
  assert.ok(reasoning.parameters.some((p) => p.id === "reasoning" && p.values?.includes("xhigh")));
  assert.ok(!reasoning.parameters.some((p) => p.values?.includes("extra-high")));
  assert.ok(effort.parameters.some((p) => p.id === "effort" && p.values?.includes("extra-high")));
  assert.ok(effort.variants.some((v) => v.params.thinking === true && v.params.effort === "extra-high"));
  assert.ok(boolOnly.parameters.some((p) => p.id === "thinking"));
  assert.ok(!boolOnly.parameters.some((p) => p.id === "reasoning" || p.id === "effort"));
  assert.notEqual(fixture.distinctions.xhighValue, fixture.distinctions.extraHighValue);
});

test("methods fixture matches protocol constants", () => {
  const fixture = readFixtureJSON<{
    schemaVersion: number;
    maxFrameBytes: number;
    methods: string[];
    eventKinds: string[];
  }>("protocol/methods.json");
  assert.equal(fixture.schemaVersion, SCHEMA_VERSION);
  assert.equal(fixture.maxFrameBytes, MAX_FRAME_BYTES);
  assert.deepEqual(fixture.methods, [...METHODS]);
  assert.deepEqual(fixture.eventKinds, [...EVENT_KINDS]);
});

test("valid frames fixture round-trip and correlation", () => {
  const lines = readFixture("protocol/valid_frames.ndjson")
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  const pending = new Map<string, ReturnType<typeof decodeLine>>();
  const seq = new RunSequencer();
  for (const line of lines) {
    const frame = decodeLine(line);
    const encoded = encodeFrame(frame);
    assert.equal(decodeLine(encoded).type, frame.type);
    if (frame.type === TYPE_REQUEST) {
      pending.set(frame.id!, frame);
    } else if (frame.type === TYPE_RESPONSE) {
      const req = pending.get(frame.id!);
      assert.ok(req);
      matchResponse(req, frame);
      pending.delete(frame.id!);
    } else if (frame.type === TYPE_EVENT) {
      seq.accept(frame);
    }
  }
});

test("invalid frames fixture", () => {
  const fixture = readFixtureJSON<{
    cases: Array<{ name: string; raw: string; errorClass: string }>;
  }>("protocol/invalid_frames.json");
  for (const tc of fixture.cases) {
    let raw = tc.raw;
    if (raw === "__OVERSIZE__") {
      raw = `{"schemaVersion":1,"type":"request","id":"r","method":"bridge/health","params":{"pad":"${"x".repeat(MAX_FRAME_BYTES)}"}}`;
    }
    assert.throws(
      () => decodeLine(raw),
      (err: unknown) => err instanceof ProtocolError && err.className === tc.errorClass,
      tc.name,
    );
  }
});

test("event sequence fixture", () => {
  const fixture = readFixtureJSON<{
    cases: Array<{ name: string; expect: string; events: unknown[] }>;
  }>("protocol/event_sequences.json");
  for (const tc of fixture.cases) {
    const seq = new RunSequencer();
    let lastErr: unknown;
    for (const event of tc.events) {
      const frame = decodeLine(JSON.stringify(event));
      try {
        seq.accept(frame);
        lastErr = undefined;
      } catch (err) {
        lastErr = err;
        break;
      }
    }
    if (tc.expect === "ok") {
      assert.equal(lastErr, undefined, tc.name);
      assert.equal(seq.terminated("run-1"), true);
    } else {
      assert.ok(lastErr instanceof ProtocolError, tc.name);
      assert.equal((lastErr as ProtocolError).className, tc.expect, tc.name);
    }
  }
});

test("unknown optional fields ignored", () => {
  const frame = decodeLine(
    `{"schemaVersion":1,"type":"request","id":"r1","method":"bridge/health","params":{},"futureField":1}`,
  );
  assert.equal(frame.id, "r1");
});

test("fixtures contain no secret material", () => {
  const root = fixtureRoot();
  const files = [
    "sdk_contract.json",
    "models_sanitized.json",
    "protocol/methods.json",
    "protocol/valid_frames.ndjson",
    "protocol/invalid_frames.json",
    "protocol/event_sequences.json",
  ];
  for (const rel of files) {
    const text = readFileSync(join(root, rel), "utf8").toLowerCase();
    assert.ok(!text.includes("sk-"));
    assert.ok(!text.includes("cursor_api_key="));
    assert.ok(!text.includes("-----begin"));
  }
});

test("typed request params decode for all required methods", () => {
  assert.equal(
    (decodeMethodParams("bridge/initialize", { implVersion: "1.0.0", future: 1 }) as { implVersion: string })
      .implVersion,
    "1.0.0",
  );
  assert.deepEqual(decodeMethodParams("bridge/health", {}), {});
  assert.equal(
    (decodeMethodParams("models/list", { apiKey: "k", extra: true }) as { apiKey: string }).apiKey,
    "k",
  );
  const create = decodeMethodParams("agent/create", {
    apiKey: "k",
    model: { id: "m1" },
    local: { cwd: "/tmp/ws" },
    settingSources: [],
    sandboxOptions: { enabled: false },
    autoReview: false,
    enableAgentRetries: false,
    mcpServers: { s: { command: "echo" } },
    futureOpt: 1,
  }) as { local: { cwd: string }; model: { id: string }; enableAgentRetries: boolean };
  assert.equal(create.local.cwd, "/tmp/ws");
  assert.equal(create.model.id, "m1");
  assert.equal(create.enableAgentRetries, false);
  assert.equal(
    (decodeMethodParams("agent/send", { agentId: "a1", prompt: "hi", future: 2 }) as { agentId: string })
      .agentId,
    "a1",
  );
  assert.equal(
    (decodeMethodParams("run/cancel", { runId: "r1" }) as { runId: string }).runId,
    "r1",
  );
  assert.equal(
    (decodeMethodParams("agent/dispose", { agentId: "a1" }) as { agentId: string }).agentId,
    "a1",
  );
  assert.deepEqual(decodeMethodParams("bridge/shutdown", {}), {});
});

test("typed params reject structural issues and apiKey on non-key methods", () => {
  const cases: Array<{ method: string; raw: unknown }> = [
    {
      method: "agent/create",
      raw: {
        apiKey: "k",
        model: { id: "m" },
        cwd: "/tmp",
        settingSources: [],
        sandboxOptions: { enabled: false },
        autoReview: false,
        enableAgentRetries: false,
      },
    },
    { method: "agent/send", raw: { agentId: "a1" } },
    { method: "models/list", raw: {} },
    { method: "bridge/health", raw: { apiKey: "secret-key-value" } },
    { method: "agent/send", raw: { agentId: "a", prompt: "p", apiKey: "secret-key-value" } },
    { method: "run/cancel", raw: { runId: "r", apiKey: "secret-key-value" } },
    { method: "agent/dispose", raw: { agentId: "a", apiKey: "secret-key-value" } },
    { method: "bridge/shutdown", raw: { apiKey: "secret-key-value" } },
    { method: "bridge/initialize", raw: { implVersion: "1", apiKey: "secret-key-value" } },
  ];
  for (const tc of cases) {
    assert.throws(
      () => decodeMethodParams(tc.method, tc.raw),
      (err: unknown) => err instanceof ProtocolError && err.className === "invalid_request",
    );
  }
});

test("safe error envelopes redact apiKey secrets", () => {
  const raw = { apiKey: "super-secret-key-xyz", agentId: "a" };
  const secrets = collectParamSecrets(raw);
  assert.deepEqual(secrets, ["super-secret-key-xyz"]);
  const leaky = safeErrorBody("invalid_request", "rejected apiKey=super-secret-key-xyz", secrets);
  assert.ok(!leaky.message.includes("super-secret-key-xyz"));
  assert.ok(leaky.message.includes("[REDACTED]"));
  try {
    decodeMethodParams("agent/send", raw);
    assert.fail("expected throw");
  } catch (err) {
    const body = formatParamError("agent/send", raw, err);
    assert.equal(body.code, "invalid_request");
    assert.ok(!body.message.includes("super-secret-key-xyz"));
  }
});

test("encodeFrame does not mutate input and matchResponse catches mismatches", () => {
  const frame = {
    type: TYPE_REQUEST,
    id: "r1",
    method: "bridge/health",
    params: {},
  };
  const encoded = encodeFrame(frame as never);
  assert.ok(encoded.includes('"schemaVersion":1'));
  assert.equal((frame as { schemaVersion?: number }).schemaVersion, undefined);

  const req = decodeLine(
    `{"schemaVersion":1,"type":"request","id":"req-a","method":"bridge/health","params":{}}`,
  );
  assert.throws(
    () =>
      matchResponse(
        req,
        decodeLine(
          `{"schemaVersion":1,"type":"response","id":"req-b","method":"bridge/health","result":{"ok":true}}`,
        ),
      ),
    (err: unknown) =>
      err instanceof ProtocolError &&
      err.className === "response_mismatch" &&
      err.message.includes("id mismatch"),
  );
  assert.throws(
    () =>
      matchResponse(
        req,
        decodeLine(
          `{"schemaVersion":1,"type":"response","id":"req-a","method":"models/list","result":{"models":[]}}`,
        ),
      ),
    (err: unknown) =>
      err instanceof ProtocolError &&
      err.className === "response_mismatch" &&
      err.message.includes("method mismatch"),
  );
});

test("initialize capabilities fixture matches METHODS", () => {
  const lines = readFixture("protocol/valid_frames.ndjson")
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  const initResp = decodeLine(lines[1]!);
  const caps = (initResp.result as { capabilities: string[] }).capabilities;
  assert.deepEqual(caps, [...METHODS]);
});
