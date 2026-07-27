import type {
  LocalAgentDocument,
  LocalAgentRunDocument,
  LocalAgentRunEventDocument,
  LocalAgentStore,
  LocalAgentStoreListResult,
} from "@cursor/sdk";

/**
 * Adapter-private in-memory LocalAgentStore for the bridge/probe.
 * Implements the exact exported @cursor/sdk 1.0.23 LocalAgentStore surface
 * because createInMemoryLocalAgentStore is not part of the public package exports.
 */
export function createBridgeInMemoryLocalAgentStore(): LocalAgentStore {
  const agents = new Map<string, LocalAgentDocument>();
  const checkpoints = new Map<string, Uint8Array>();
  const runs = new Map<string, LocalAgentRunDocument>();
  const runEvents = new Map<string, LocalAgentRunEventDocument[]>();
  let eventSeq = 0;

  function checkpointKey(agentId: string, blobId: string): string {
    return `${agentId}\0${blobId}`;
  }

  function runKey(agentId: string, runId: string): string {
    return `${agentId}\0${runId}`;
  }

  return {
    agents: {
      async get({ agentId }) {
        return agents.get(agentId) ?? null;
      },
      async create({ agent }) {
        agents.set(agent.agentId, agent);
        return agent;
      },
      async update({ agent }) {
        agents.set(agent.agentId, agent);
        return agent;
      },
      async delete({ filter }) {
        const ids = filter.agentIds;
        for (const [id, doc] of [...agents.entries()]) {
          if (ids && ids.length > 0 && !ids.includes(id)) continue;
          if (filter.cwd && doc.cwd !== filter.cwd) continue;
          agents.delete(id);
        }
      },
      async list(input) {
        const filter = input?.filter;
        let items = [...agents.values()];
        if (filter?.cwd) items = items.filter((a) => a.cwd === filter.cwd);
        items.sort((a, b) => a.agentId.localeCompare(b.agentId));
        return page(items, filter?.cursor, filter?.limit);
      },
    },
    checkpoints: {
      async get({ agentId, blobId }) {
        return checkpoints.get(checkpointKey(agentId, blobId)) ?? null;
      },
      async create({ agentId, blobId, data }) {
        checkpoints.set(checkpointKey(agentId, blobId), data);
      },
      async update({ agentId, blobId, data }) {
        checkpoints.set(checkpointKey(agentId, blobId), data);
      },
      async delete({ filter }) {
        for (const key of [...checkpoints.keys()]) {
          const [agentId, blobId] = key.split("\0");
          if (filter.agentIds && filter.agentIds.length > 0 && !filter.agentIds.includes(agentId!)) {
            continue;
          }
          if (filter.blobIds && filter.blobIds.length > 0 && !filter.blobIds.includes(blobId!)) {
            continue;
          }
          checkpoints.delete(key);
        }
      },
      async list(input) {
        const filter = input?.filter;
        let ids: string[] = [];
        for (const key of checkpoints.keys()) {
          const [agentId, blobId] = key.split("\0");
          if (filter?.agentIds && filter.agentIds.length > 0 && !filter.agentIds.includes(agentId!)) {
            continue;
          }
          if (filter?.blobIds && filter.blobIds.length > 0 && !filter.blobIds.includes(blobId!)) {
            continue;
          }
          ids.push(blobId!);
        }
        ids = [...new Set(ids)].sort();
        return page(ids, filter?.cursor, filter?.limit);
      },
    },
    runs: {
      async get({ agentId, runId }) {
        return runs.get(runKey(agentId, runId)) ?? null;
      },
      async create({ run }) {
        runs.set(runKey(run.agentId, run.runId), run);
        return run;
      },
      async update({ run }) {
        runs.set(runKey(run.agentId, run.runId), run);
        return run;
      },
      async delete({ filter }) {
        for (const [key, run] of [...runs.entries()]) {
          if (filter.agentIds && filter.agentIds.length > 0 && !filter.agentIds.includes(run.agentId)) {
            continue;
          }
          if (filter.runIds && filter.runIds.length > 0 && !filter.runIds.includes(run.runId)) {
            continue;
          }
          runs.delete(key);
        }
      },
      async list(input) {
        const filter = input?.filter;
        let items = [...runs.values()];
        if (filter?.agentIds && filter.agentIds.length > 0) {
          items = items.filter((r) => filter.agentIds!.includes(r.agentId));
        }
        if (filter?.runIds && filter.runIds.length > 0) {
          items = items.filter((r) => filter.runIds!.includes(r.runId));
        }
        items.sort((a, b) => a.runId.localeCompare(b.runId));
        return page(items, filter?.cursor, filter?.limit);
      },
    },
    runEvents: {
      async append({ runId, eventType, payload, payloadRef, idempotencyKey }) {
        eventSeq += 1;
        const doc: LocalAgentRunEventDocument = {
          runId,
          seq: eventSeq,
          offset: String(eventSeq),
          eventType,
          payload: payload ?? null,
          payloadRef: payloadRef ?? null,
          idempotencyKey: idempotencyKey ?? null,
          createdAt: Date.now(),
        };
        const list = runEvents.get(runId) ?? [];
        list.push(doc);
        runEvents.set(runId, list);
        return doc;
      },
      async list({ runId, afterOffset, limit }) {
        const all = runEvents.get(runId) ?? [];
        const after = afterOffset ? Number(afterOffset) : 0;
        let items = all.filter((e) => e.seq > after);
        const lim = limit && limit > 0 ? limit : items.length;
        const pageItems = items.slice(0, lim);
        const next = pageItems.length < items.length ? pageItems[pageItems.length - 1]?.offset : undefined;
        return next ? { items: pageItems, nextOffset: next } : { items: pageItems };
      },
      async delete({ filter }) {
        if (!filter.runIds || filter.runIds.length === 0) {
          runEvents.clear();
          return;
        }
        for (const id of filter.runIds) runEvents.delete(id);
      },
    },
  };
}

function page<T extends { agentId?: string } | string>(
  items: T[],
  cursor: string | undefined,
  limit: number | undefined,
): LocalAgentStoreListResult<T> {
  let start = 0;
  if (cursor) {
    const idx = items.findIndex((item) => {
      const id = typeof item === "string" ? item : (item as { agentId?: string; runId?: string }).agentId
        ?? (item as { runId?: string }).runId
        ?? "";
      return id === cursor;
    });
    start = idx >= 0 ? idx + 1 : 0;
  }
  const lim = limit && limit > 0 ? limit : items.length;
  const slice = items.slice(start, start + lim);
  const next = start + lim < items.length ? idOf(slice[slice.length - 1]!) : undefined;
  return next ? { items: slice, nextCursor: next } : { items: slice };
}

function idOf(item: unknown): string {
  if (typeof item === "string") return item;
  const obj = item as { agentId?: string; runId?: string };
  return obj.agentId ?? obj.runId ?? "";
}
