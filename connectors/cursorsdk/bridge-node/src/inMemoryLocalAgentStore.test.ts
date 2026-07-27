import assert from "node:assert/strict";
import { test } from "node:test";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";

test("in-memory LocalAgentStore supports agent/run/checkpoint/event lifecycle", async () => {
  const store = createBridgeInMemoryLocalAgentStore();
  const now = Date.now();
  const agent = await store.agents.create({
    agent: {
      agentId: "a1",
      cwd: "/tmp/ws",
      status: "idle",
      createdAt: now,
      updatedAt: now,
    },
  });
  assert.equal(agent.agentId, "a1");
  assert.equal((await store.agents.get({ agentId: "a1" }))?.cwd, "/tmp/ws");

  await store.checkpoints.create({
    agentId: "a1",
    blobId: "b1",
    data: new Uint8Array([1, 2, 3]),
  });
  const blob = await store.checkpoints.get({ agentId: "a1", blobId: "b1" });
  assert.ok(blob);
  assert.equal(blob!.length, 3);

  const run = await store.runs.create({
    run: {
      runId: "r1",
      agentId: "a1",
      turnNumber: 1,
      status: "running",
      createdAt: now,
      updatedAt: now,
    },
  });
  assert.equal(run.runId, "r1");
  assert.equal((await store.runs.get({ agentId: "a1", runId: "r1" }))?.status, "running");

  const ev = await store.runEvents.append({
    runId: "r1",
    eventType: "text-delta",
    payload: { text: "x" },
  });
  assert.equal(ev.runId, "r1");
  const listed = await store.runEvents.list({ runId: "r1" });
  assert.equal(listed.items.length, 1);

  await store.runs.delete({ filter: { runIds: ["r1"] } });
  assert.equal(await store.runs.get({ agentId: "a1", runId: "r1" }), null);
  await store.agents.delete({ filter: { agentIds: ["a1"] } });
  assert.equal(await store.agents.get({ agentId: "a1" }), null);
});
