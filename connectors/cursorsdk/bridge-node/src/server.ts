import { createInterface } from "node:readline";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { platform } from "node:os";
import { fileURLToPath } from "node:url";
import { AgentPool, CancelTimeoutError, type SdkAgentModule } from "./agents.js";
import {
  bridgeErrorBody,
  formatMethodError,
  formatSdkLoadError,
  writeBoundedStderr,
} from "./errors.js";
import { normalizeModelRows } from "./models.js";
import { decodeMethodParams, formatParamError, type AgentCreateParams, type AgentDisposeParams, type AgentSendParams, type RunCancelParams } from "./params.js";
import {
  MAX_FRAME_BYTES,
  decodeLine,
  encodeFrame,
  METHODS,
  ProtocolError,
  SCHEMA_VERSION,
  TYPE_REQUEST,
  TYPE_RESPONSE,
  type Frame,
  type Method,
} from "./protocol.js";
import { loadPublishedSdk } from "./sdk_runtime.js";
import type { SdkRuntime } from "./sdk_runtime.js";

export type { SdkRuntime } from "./sdk_runtime.js";

const here = dirname(fileURLToPath(import.meta.url));

function readBridgeImplVersion(): string {
  try {
    const pkg = JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8")) as {
      version?: string;
    };
    return typeof pkg.version === "string" ? pkg.version : "0.0.0";
  } catch {
    return "0.0.0";
  }
}

export interface BridgeServerOptions {
  loadSdk?: () => Promise<SdkRuntime>;
  implVersion?: string;
  nodeVersion?: string;
  stdin?: NodeJS.ReadableStream;
  stdout?: NodeJS.WritableStream;
  stderr?: NodeJS.WritableStream;
  logDiagnostic?: (line: string) => string;
  agentPool?: AgentPool;
  cancelTimeoutMs?: number;
  detectPlatform?: () => string;
  probeSandboxSupported?: (sdk: SdkRuntime) => Promise<boolean>;
}

export interface BridgeServer {
  run(): Promise<void>;
}

const NOT_IMPLEMENTED_METHODS = new Set<Method>([]);
export const MAX_INFLIGHT_REQUESTS = 8;
const CONTROL_METHODS = new Set<string>(["run/cancel", "agent/dispose", "bridge/shutdown", "bridge/health"]);

export function createBridgeServer(options: BridgeServerOptions = {}): BridgeServer {
  const stdin = options.stdin ?? process.stdin;
  const stdout = options.stdout ?? process.stdout;
  const stderr = options.stderr ?? process.stderr;
  const implVersion = options.implVersion ?? readBridgeImplVersion();
  const nodeVersion = options.nodeVersion ?? process.versions.node;
  const loadSdk = options.loadSdk ?? loadPublishedSdk;
  const detectPlatform = options.detectPlatform ?? (() => platform());
  const probeSandboxSupported = options.probeSandboxSupported;
  const stderrBytes = { value: 0 };

  let initialized = false;
  let protocolBroken = false;
  let shuttingDown = false;
  let generation = 0;
  let sandboxSupported = false;
  let sdkPromise: Promise<SdkRuntime> | undefined;
  let agentPool = options.agentPool;
  const cancelTimeoutMs = options.cancelTimeoutMs;
  const inflight = new Set<Promise<void>>();

  const writeStdout = (text: string): void => {
    stdout.write(text);
  };

  const ensureAgentPool = (sdk: SdkRuntime): AgentPool => {
    if (!agentPool) {
      agentPool = new AgentPool({
        Agent: sdk.Agent as unknown as SdkAgentModule,
        emitEvent: writeStdout,
        ...(cancelTimeoutMs !== undefined ? { cancelTimeoutMs } : {}),
      });
    }
    return agentPool;
  };

  const logDiagnostic = (line: string): void => {
    const rendered = options.logDiagnostic ? options.logDiagnostic(line) : line;
    writeBoundedStderr(stderr, rendered, stderrBytes);
  };

  const writeResponse = (
    req: Pick<Frame, "id" | "method">,
    result?: unknown,
    error?: { code: string; message: string },
  ): void => {
    const frame: Frame = {
      schemaVersion: SCHEMA_VERSION,
      type: TYPE_RESPONSE,
      id: req.id ?? "",
      ...(req.method ? { method: req.method } : {}),
    };
    if (error) frame.error = error;
    else frame.result = result;
    writeStdout(`${encodeFrame(frame)}\n`);
  };

  const resolveSandboxSupported = async (sdk: SdkRuntime): Promise<boolean> => {
    try {
      if (probeSandboxSupported) {
        return Boolean(await probeSandboxSupported(sdk));
      }
      if (typeof sdk.sandboxSupported === "boolean") {
        return sdk.sandboxSupported;
      }
      return false;
    } catch {
      return false;
    }
  };

  const rejectProtocol = (req: Pick<Frame, "id" | "method">, code: string, message: string): void => {
    protocolBroken = true;
    logDiagnostic(`protocol error: ${code}: ${message}`);
    writeResponse(req, undefined, bridgeErrorBody(code, message));
  };

  const parseRequestLine = (line: string): Frame | undefined => {
    const bytes = Buffer.byteLength(line, "utf8");
    if (bytes > MAX_FRAME_BYTES) {
      logDiagnostic("decode error: frame_too_large");
      return undefined;
    }
    const text = line.trim();
    if (!text || !text.startsWith("{")) {
      logDiagnostic("decode error: invalid_json");
      return undefined;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (err) {
      logDiagnostic(`decode error: invalid_json: ${err instanceof Error ? err.message : String(err)}`);
      return undefined;
    }
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      logDiagnostic("decode error: invalid_json");
      return undefined;
    }
    const frame = parsed as Frame;
    if (frame.type === TYPE_REQUEST && frame.id && frame.method && frame.schemaVersion !== SCHEMA_VERSION) {
      rejectProtocol(frame, "incompatible_version", `schemaVersion=${frame.schemaVersion}`);
      return undefined;
    }
    return decodeLine(line);
  };

  const ensureSdk = async (req: Frame, rawParams: unknown): Promise<SdkRuntime | undefined> => {
    if (sdkPromise) {
      try {
        return await sdkPromise;
      } catch (err) {
        writeResponse(req, undefined, formatSdkLoadError(err, collectSecrets(rawParams)));
        return undefined;
      }
    }
    sdkPromise = loadSdk();
    try {
      return await sdkPromise;
    } catch (err) {
      sdkPromise = undefined;
      writeResponse(req, undefined, formatSdkLoadError(err, collectSecrets(rawParams)));
      return undefined;
    }
  };

  const handleRequest = async (req: Frame): Promise<void> => {
    if (protocolBroken) {
      writeResponse(
        req,
        undefined,
        bridgeErrorBody("incompatible_version", "connection rejected after protocol error"),
      );
      return;
    }

    if (shuttingDown && req.method !== "bridge/shutdown") {
      writeResponse(req, undefined, bridgeErrorBody("bridge_shutting_down", "bridge is shutting down"));
      return;
    }

    if (req.schemaVersion !== SCHEMA_VERSION) {
      rejectProtocol(req, "incompatible_version", `schemaVersion=${req.schemaVersion}`);
      return;
    }

    if (!req.method || !req.id) {
      rejectProtocol(req, "invalid_request", "missing method or id");
      return;
    }

    let params: unknown;
    try {
      params = decodeMethodParams(req.method, req.params ?? {});
    } catch (err) {
      writeResponse(req, undefined, formatParamError(req.method, req.params ?? {}, err));
      return;
    }

    switch (req.method as Method) {
      case "bridge/initialize": {
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        initialized = true;
        generation = 1;
        sandboxSupported = await resolveSandboxSupported(sdk);
        logDiagnostic("bridge initialized");
        writeResponse(req, {
          schemaVersion: SCHEMA_VERSION,
          implVersion,
          sdkVersion: sdk.packageVersion,
          nodeVersion,
          capabilities: [...METHODS],
          sandboxSupported,
        });
        return;
      }
      case "bridge/health": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        writeResponse(req, {
          ok: true,
          generation,
          sandboxSupported,
        });
        return;
      }
      case "models/list": {
        if (protocolBroken) return;
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        try {
          const listed = await sdk.Cursor.models.list({
            apiKey: (params as { apiKey: string }).apiKey,
          });
          const models = normalizeModelRows(listed);
          writeResponse(req, { models });
        } catch (err) {
          writeResponse(req, undefined, formatMethodError("models/list", params, err, "model_discovery_failed"));
        }
        return;
      }
      case "agent/create": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        try {
          const pool = ensureAgentPool(sdk);
          const result = await pool.createAgent(params as AgentCreateParams);
          writeResponse(req, result);
        } catch (err) {
          writeResponse(req, undefined, formatMethodError("agent/create", params, err, "agent_create_failed"));
        }
        return;
      }
      case "agent/send": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        try {
          const pool = ensureAgentPool(sdk);
          const sendParams = params as AgentSendParams;
          const result = await pool.send(sendParams.agentId, sendParams.prompt);
          writeResponse(req, result);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err ?? "");
          if (message.includes("bridge_shutting_down")) {
            writeResponse(req, undefined, bridgeErrorBody("bridge_shutting_down", "bridge is shutting down"));
            return;
          }
          const code = message.includes("agent_busy") ? "agent_busy" : "agent_send_failed";
          writeResponse(req, undefined, formatMethodError("agent/send", params, err, code));
        }
        return;
      }
      case "run/cancel": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        try {
          const pool = ensureAgentPool(sdk);
          const result = await pool.cancelRun((params as RunCancelParams).runId);
          writeResponse(req, result);
        } catch (err) {
          if (err instanceof CancelTimeoutError) {
            writeResponse(
              req,
              undefined,
              bridgeErrorBody("cursor_sdk_cancel_timeout", "run/cancel timed out waiting for SDK cancellation"),
            );
            return;
          }
          writeResponse(req, undefined, formatMethodError("run/cancel", params, err, "run_cancel_failed"));
        }
        return;
      }
      case "agent/dispose": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        const sdk = await ensureSdk(req, params);
        if (!sdk) return;
        try {
          const pool = ensureAgentPool(sdk);
          const result = await pool.disposeAgent((params as AgentDisposeParams).agentId);
          writeResponse(req, result);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err ?? "");
          const code = message.includes("unknown agentId")
            ? "unknown_agent"
            : message.includes("cursor_sdk_dispose_timeout")
              ? "cursor_sdk_dispose_timeout"
              : "agent_dispose_failed";
          writeResponse(req, undefined, formatMethodError("agent/dispose", params, err, code));
        }
        return;
      }
      case "bridge/shutdown": {
        if (!initialized) {
          writeResponse(req, undefined, bridgeErrorBody("not_initialized", "bridge/initialize required"));
          return;
        }
        shuttingDown = true;
        try {
          const sdk = await ensureSdk(req, params);
          if (!sdk) return;
          const pool = ensureAgentPool(sdk);
          const result = await pool.shutdown();
          writeResponse(req, result);
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err ?? "");
          const code = message.includes("cursor_sdk_shutdown_timeout")
            ? "cursor_sdk_shutdown_timeout"
            : "cursor_sdk_shutdown_failed";
          writeResponse(req, undefined, formatMethodError("bridge/shutdown", params, err, code));
        }
        return;
      }
      default: {
        if (NOT_IMPLEMENTED_METHODS.has(req.method as Method)) {
          writeResponse(
            req,
            undefined,
            bridgeErrorBody("not_implemented", `${req.method} is not implemented yet`),
          );
          return;
        }
        writeResponse(req, undefined, bridgeErrorBody("unknown_method", req.method));
      }
    }
  };

  return {
    async run(): Promise<void> {
      const rl = createInterface({ input: stdin, crlfDelay: Infinity });
      try {
        for await (const line of rl) {
          if (!line.trim()) continue;
          let frame: Frame | undefined;
          try {
            frame = parseRequestLine(line);
          } catch (err) {
            if (err instanceof ProtocolError) {
              logDiagnostic(`decode error: ${err.className}: ${err.message}`);
              if (err.className === "incompatible_version") {
                protocolBroken = true;
              }
              continue;
            }
            logDiagnostic(`decode error: ${String(err)}`);
            continue;
          }
          if (!frame) continue;
          if (frame.type !== TYPE_REQUEST) {
            logDiagnostic(`ignored non-request frame type=${frame.type}`);
            continue;
          }
          const method = frame.method ?? "";
          const isControl = CONTROL_METHODS.has(method);
          if (shuttingDown && method !== "bridge/shutdown") {
            writeResponse(frame, undefined, bridgeErrorBody("bridge_shutting_down", "bridge is shutting down"));
            continue;
          }
          if (!isControl && inflight.size >= MAX_INFLIGHT_REQUESTS) {
            writeResponse(
              frame,
              undefined,
              bridgeErrorBody("agent_busy", `in-flight request limit ${MAX_INFLIGHT_REQUESTS}`),
            );
            continue;
          }
          const task = handleRequest(frame)
            .catch((err: unknown) => {
              logDiagnostic(`request handler error: ${err instanceof Error ? err.message : String(err)}`);
            })
            .finally(() => {
              inflight.delete(task);
            });
          inflight.add(task);
        }
        await Promise.allSettled([...inflight]);
      } finally {
        rl.close();
        await Promise.allSettled([...inflight]);
        if (agentPool && !agentPool.isShutDown()) {
          try {
            await agentPool.shutdown();
          } catch (err) {
            logDiagnostic(
              `shutdown cleanup error: ${err instanceof Error ? err.message : String(err)}`,
            );
          }
        }
      }
    },
  };
}

function collectSecrets(raw: unknown): string[] {
  try {
    const obj = raw as { apiKey?: string };
    return typeof obj?.apiKey === "string" && obj.apiKey ? [obj.apiKey] : [];
  } catch {
    return [];
  }
}
