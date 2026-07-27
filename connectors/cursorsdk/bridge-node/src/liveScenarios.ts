/**
 * Opt-in live scenario runner for @cursor/sdk 1.0.23.
 *
 * Requires:
 *   CURSOR_SDK_LIVE=1
 *   CURSOR_API_KEY=<key from environment only>
 *
 * hard_bridge_restart / canonical_rebootstrap are unavailable in this Node CLI
 * (no processLifecycle hooks) → overall status blocked, ok=false.
 * Fake platform lifecycle is proven by `make test-cursor-sdk-platform` only.
 * Default npm test does not run this against the network.
 */
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";
import {
  LiveScenariosDisabledError,
  runLiveScenarios,
  sanitizeLiveScenariosError,
  type LiveScenarioWorkspace,
  type LiveScenariosSdk,
  type ScenarioAgent,
  type ScenarioModelRow,
} from "./liveScenariosLib.js";
import {
  assertPinnedSdkVersion,
  readInstalledCursorSdkVersion,
} from "./sdk_runtime.js";

function normalizeModelRows(rows: unknown[]): ScenarioModelRow[] {
  const out: ScenarioModelRow[] = [];
  for (const row of rows) {
    const r = row as {
      id?: string;
      displayName?: string;
      parameters?: Array<{
        id?: string;
        values?: Array<string | { value?: string }>;
      }>;
    };
    const normalized: ScenarioModelRow = {};
    if (typeof r.id === "string") normalized.id = r.id;
    if (typeof r.displayName === "string") normalized.displayName = r.displayName;
    if (Array.isArray(r.parameters)) {
      normalized.parameters = r.parameters.map((p) => {
        const param: { id?: string; values?: string[] } = {};
        if (typeof p.id === "string") param.id = p.id;
        if (Array.isArray(p.values)) {
          param.values = p.values
            .map((v) => (typeof v === "string" ? v : v?.value))
            .filter((v): v is string => typeof v === "string" && v.length > 0);
        }
        return param;
      });
    }
    out.push(normalized);
  }
  return out;
}

async function loadPublishedSdk(): Promise<LiveScenariosSdk> {
  const version = readInstalledCursorSdkVersion();
  assertPinnedSdkVersion(version);
  const sdk = await import("@cursor/sdk");
  return {
    packageVersion: version,
    platform: process.platform,
    Cursor: {
      models: {
        list: async (opts) => {
          const listed = await sdk.Cursor.models.list({ apiKey: opts.apiKey });
          return normalizeModelRows(Array.isArray(listed) ? listed : []);
        },
      },
    },
    Agent: {
      create: async (opts) => {
        const agent = await sdk.Agent.create({
          apiKey: opts.apiKey,
          model: {
            id: opts.model.id,
            ...(opts.model.params ? { params: opts.model.params } : {}),
          },
          local: {
            cwd: opts.local.cwd,
            store: opts.local.store,
            settingSources: (opts.local.settingSources ?? []) as never,
            sandboxOptions: { enabled: opts.local.sandboxOptions?.enabled ?? false },
            autoReview: opts.local.autoReview ?? false,
            enableAgentRetries: opts.local.enableAgentRetries ?? false,
          },
          ...(opts.mcpServers ? { mcpServers: opts.mcpServers as never } : {}),
        });
        return agent as unknown as ScenarioAgent;
      },
    },
  };
}

async function mkScenarioWorkspace(label: string): Promise<LiveScenarioWorkspace> {
  const root = await mkdtemp(join(tmpdir(), `lip-live-${label}-`));
  return {
    cwd: root,
    cleanup: async () => {
      await rm(root, { recursive: true, force: true });
    },
  };
}

async function main(): Promise<void> {
  try {
    const summary = await runLiveScenarios({
      env: {
        liveEnabled: process.env.CURSOR_SDK_LIVE === "1",
        apiKey: process.env.CURSOR_API_KEY,
        nodeVersion: process.version,
      },
      loadSdk: loadPublishedSdk,
      createStore: createBridgeInMemoryLocalAgentStore,
      mkWorkspace: mkScenarioWorkspace,
      log: (line) => console.log(line),
      logError: (line) => console.error(line),
    });
    if (summary.status === "complete" && summary.ok) {
      process.exit(0);
    }
    if (summary.status === "blocked") {
      process.exit(3);
    }
    process.exit(1);
  } catch (err) {
    if (err instanceof LiveScenariosDisabledError) {
      console.error(sanitizeLiveScenariosError(err));
      process.exit(err.exitCode);
    }
    // Library already emits one sanitized JSON for failed/blocked runs; keep a
    // last-resort single envelope only for unexpected throws outside that path.
    console.error(
      JSON.stringify({
        ok: false,
        status: "failed",
        error: sanitizeLiveScenariosError(err),
        scenarios: [],
      }),
    );
    process.exit(1);
  }
}

void main();
