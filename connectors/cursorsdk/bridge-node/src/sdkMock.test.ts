import assert from "node:assert/strict";
import { test, beforeEach } from "node:test";
import {
  Agent,
  Cursor,
  assertNoOpenHandles,
  openHandles,
  resetSdkMock,
  setRunScript,
} from "./sdkMock.js";
import { PINNED_SDK_VERSION } from "./protocol.js";
import { readFixtureJSON } from "./fixtures.js";

beforeEach(() => {
  resetSdkMock();
});

test("mock models cover reasoning distinctions from fixtures", async () => {
  const models = await Cursor.models.list({ apiKey: "secret-value" });
  const fixture = readFixtureJSON<{ distinctions: Record<string, string> }>("models_sanitized.json");
  const byId = new Map(models.map((m) => [m.id, m]));
  const reasoningId = fixture.distinctions.reasoningParam;
  const effortId = fixture.distinctions.effortPlusThinking;
  const boolOnlyId = fixture.distinctions.booleanThinkingOnly;
  assert.ok(reasoningId && effortId && boolOnlyId);
  assert.ok(byId.get(reasoningId));
  assert.ok(byId.get(effortId));
  assert.ok(byId.get(boolOnlyId));
  assert.equal(PINNED_SDK_VERSION, "1.0.23");
});

test("mock covers ordered deltas, cancel, dispose, and no open handles", async () => {
  setRunScript("ordered", {
    deltas: [
      { type: "thinking-delta", text: "plan" },
      { type: "text-delta", text: "answer" },
      { type: "turn-ended", usage: { inputTokens: 2, outputTokens: 3, totalTokens: 5 } },
    ],
  });
  const agent = await Agent.create({
    apiKey: "crsr_test_key",
    model: { id: "gpt-5.3-codex" },
    local: {
      cwd: "/tmp/ws",
      settingSources: [],
      sandboxOptions: { enabled: false },
      autoReview: false,
      enableAgentRetries: false,
    },
  });
  assert.equal((agent.options as { apiKey?: string }).apiKey, "[REDACTED]");
  assert.equal(agent.options.local?.enableAgentRetries, false);

  const run = await agent.send("please keep ordered");
  const kinds: string[] = [];
  for await (const delta of run.stream((d) => kinds.push(d.type))) {
    void delta;
  }
  assert.deepEqual(kinds, ["thinking-delta", "text-delta", "turn-ended"]);
  assert.equal(run.status, "finished");

  const cancelAgent = await Agent.create({
    apiKey: "k",
    local: { enableAgentRetries: false },
  });
  setRunScript("cancel-me", {
    deltas: [
      { type: "text-delta", text: "partial" },
      { type: "text-delta", text: "more" },
    ],
    blockCancel: false,
  });
  const cancelRun = await cancelAgent.send("cancel-me now");
  const iter = cancelRun.stream();
  const first = await iter.next();
  assert.equal(first.value?.type, "text-delta");
  await cancelRun.cancel();
  assert.equal(cancelRun.status, "cancelled");
  // drain remaining
  for await (const _ of iter) {
    // ignore
  }

  await agent[Symbol.asyncDispose]();
  await cancelAgent[Symbol.asyncDispose]();
  assert.deepEqual(openHandles(), { agents: 0, runs: 0, listeners: 0 });
  assertNoOpenHandles();
});

test("mock shutdown/open handles and error redaction", async () => {
  setRunScript("boom", {
    status: "error",
    errorMessage: "upstream failed",
    deltas: [{ type: "text-delta", text: "x" }],
  });
  const agent = await Agent.create({ apiKey: "sk-secret", local: { enableAgentRetries: false } });
  const run = await agent.send("boom");
  await assert.rejects(async () => {
    for await (const _ of run) {
      // drain until error
    }
  }, /upstream failed/);
  assert.equal(openHandles().runs, 0);
  await agent[Symbol.asyncDispose]();
  assertNoOpenHandles("after error dispose");

  await assert.rejects(() => Agent.resume(), /excluded by design/);
});

test("blocked cancel surfaces error", async () => {
  setRunScript("block", { blockCancel: true, deltas: [{ type: "text-delta", text: "x" }] });
  const agent = await Agent.create({ apiKey: "k", local: { enableAgentRetries: false } });
  const run = await agent.send("block");
  await assert.rejects(() => run.cancel(), /cancel blocked/);
  for await (const _ of run) {
    // drain
  }
  agent.close();
  assertNoOpenHandles();
});
