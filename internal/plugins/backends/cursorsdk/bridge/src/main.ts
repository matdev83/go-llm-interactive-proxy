import { createBridgeServer } from "./server.js";

async function main(): Promise<void> {
  const server = createBridgeServer();
  await server.run();
}

main().catch((err) => {
  const message = err instanceof Error ? err.message : String(err);
  process.stderr.write(`${message.slice(0, 8192)}\n`);
  process.exitCode = 1;
});
