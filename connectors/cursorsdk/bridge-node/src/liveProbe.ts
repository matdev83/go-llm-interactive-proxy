/**
 * Opt-in live contract probe for exact @cursor/sdk 1.0.23.
 *
 * Requires:
 *   CURSOR_SDK_LIVE_PROBE=1
 *   CURSOR_API_KEY=<key from environment only>
 *
 * Never writes raw account/workspace content. Default npm test does not run this
 * against the network; see liveProbe.test.ts for mocked coverage.
 */
import { tmpdir } from "node:os";
import { createBridgeInMemoryLocalAgentStore } from "./inMemoryLocalAgentStore.js";
import {
  LiveProbeDisabledError,
  runLiveProbe,
  type LiveProbeSdk,
  type ProbeAgent,
} from "./liveProbeLib.js";
import {
  assertPinnedSdkVersion,
  readInstalledCursorSdkVersion,
} from "./sdk_runtime.js";

async function loadPublishedSdk(): Promise<LiveProbeSdk> {
  const version = readInstalledCursorSdkVersion();
  assertPinnedSdkVersion(version);
  const sdk = await import("@cursor/sdk");
  return {
    packageVersion: version,
    Cursor: {
      models: sdk.Cursor.models,
    },
    Agent: {
      create: async (opts) => {
        const agent = await sdk.Agent.create({
          apiKey: opts.apiKey,
          model: opts.model,
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
        return agent as unknown as ProbeAgent;
      },
    },
  };
}

async function main(): Promise<void> {
  try {
    await runLiveProbe({
      env: {
        liveProbeEnabled: process.env.CURSOR_SDK_LIVE_PROBE === "1",
        apiKey: process.env.CURSOR_API_KEY,
        cwd: process.env.CURSOR_SDK_LIVE_PROBE_CWD?.trim() || tmpdir(),
        nodeVersion: process.version,
      },
      loadSdk: loadPublishedSdk,
      createStore: createBridgeInMemoryLocalAgentStore,
      log: (line) => console.log(line),
      logError: (line) => console.error(line),
    });
  } catch (err) {
    if (err instanceof LiveProbeDisabledError) {
      console.error(err.message);
      process.exit(err.exitCode);
    }
    const message = err instanceof Error ? err.message : String(err);
    console.error(JSON.stringify({ ok: false, error: message.slice(0, 200) }));
    process.exit(1);
  }
}

void main();
