import { METHODS, type Method, ProtocolError } from "./protocol.js";

export interface InitializeParams {
  implVersion: string;
}

export interface HealthParams {}

export interface ModelsListParams {
  apiKey: string;
}

export interface ModelSelection {
  id: string;
  params?: unknown;
}

export interface SandboxOptions {
  enabled: boolean;
}

export interface AgentCreateLocal {
  cwd: string;
}

export interface AgentCreateParams {
  apiKey: string;
  model: ModelSelection;
  local: AgentCreateLocal;
  settingSources: string[];
  sandboxOptions: SandboxOptions;
  autoReview: boolean;
  enableAgentRetries: boolean;
  mcpServers?: unknown;
}

export interface AgentSendParams {
  agentId: string;
  prompt: string;
}

export interface RunCancelParams {
  runId: string;
}

export interface AgentDisposeParams {
  agentId: string;
}

export interface ShutdownParams {}

function asObject(raw: unknown): Record<string, unknown> {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    throw new ProtocolError("invalid_request", "params must be a JSON object");
  }
  return raw as Record<string, unknown>;
}

function rejectAPIKey(obj: Record<string, unknown>): void {
  if (Object.prototype.hasOwnProperty.call(obj, "apiKey")) {
    throw new ProtocolError("invalid_request", "apiKey not allowed for this method");
  }
}

function requireString(obj: Record<string, unknown>, key: string): string {
  const v = obj[key];
  if (typeof v !== "string" || v.trim() === "") {
    throw new ProtocolError("invalid_request", `missing ${key}`);
  }
  return v;
}

export function decodeMethodParams(method: Method | string, raw: unknown): unknown {
  const obj = asObject(raw ?? {});
  switch (method) {
    case "bridge/initialize": {
      rejectAPIKey(obj);
      const implVersion = requireString(obj, "implVersion");
      return { implVersion } satisfies InitializeParams;
    }
    case "bridge/health": {
      rejectAPIKey(obj);
      return {} satisfies HealthParams;
    }
    case "models/list": {
      const apiKey = requireString(obj, "apiKey");
      return { apiKey } satisfies ModelsListParams;
    }
    case "agent/create": {
      for (const key of [
        "apiKey",
        "model",
        "local",
        "settingSources",
        "sandboxOptions",
        "autoReview",
        "enableAgentRetries",
      ]) {
        if (!Object.prototype.hasOwnProperty.call(obj, key)) {
          throw new ProtocolError("invalid_request", `missing ${key}`);
        }
      }
      const apiKey = requireString(obj, "apiKey");
      const modelObj = asObject(obj.model);
      const modelId = requireString(modelObj, "id");
      const localObj = asObject(obj.local);
      const cwd = requireString(localObj, "cwd");
      if (!Array.isArray(obj.settingSources)) {
        throw new ProtocolError("invalid_request", "missing settingSources");
      }
      const sandbox = asObject(obj.sandboxOptions);
      if (typeof sandbox.enabled !== "boolean") {
        throw new ProtocolError("invalid_request", "missing sandboxOptions.enabled");
      }
      if (typeof obj.autoReview !== "boolean") {
        throw new ProtocolError("invalid_request", "missing autoReview");
      }
      if (typeof obj.enableAgentRetries !== "boolean") {
        throw new ProtocolError("invalid_request", "missing enableAgentRetries");
      }
      const out: AgentCreateParams = {
        apiKey,
        model: { id: modelId, params: modelObj.params },
        local: { cwd },
        settingSources: obj.settingSources as string[],
        sandboxOptions: { enabled: sandbox.enabled },
        autoReview: obj.autoReview,
        enableAgentRetries: obj.enableAgentRetries,
      };
      if (Object.prototype.hasOwnProperty.call(obj, "mcpServers")) {
        out.mcpServers = obj.mcpServers;
      }
      return out;
    }
    case "agent/send": {
      rejectAPIKey(obj);
      return {
        agentId: requireString(obj, "agentId"),
        prompt: requireString(obj, "prompt"),
      } satisfies AgentSendParams;
    }
    case "run/cancel": {
      rejectAPIKey(obj);
      return { runId: requireString(obj, "runId") } satisfies RunCancelParams;
    }
    case "agent/dispose": {
      rejectAPIKey(obj);
      return { agentId: requireString(obj, "agentId") } satisfies AgentDisposeParams;
    }
    case "bridge/shutdown": {
      rejectAPIKey(obj);
      return {} satisfies ShutdownParams;
    }
    default:
      if (!(METHODS as readonly string[]).includes(method)) {
        throw new ProtocolError("unknown_method", method);
      }
      throw new ProtocolError("invalid_request", `unsupported method ${method}`);
  }
}

export function collectParamSecrets(raw: unknown): string[] {
  try {
    const obj = asObject(raw);
    const key = obj.apiKey;
    return typeof key === "string" && key ? [key] : [];
  } catch {
    return [];
  }
}

export function redactSecrets(message: string, secrets: string[]): string {
  let out = message;
  for (const secret of secrets) {
    if (!secret) continue;
    out = out.split(secret).join("[REDACTED]");
  }
  return out;
}

export function safeErrorBody(
  code: string,
  message: string,
  secrets: string[] = [],
): { code: string; message: string } {
  return { code, message: redactSecrets(message, secrets) };
}

export function formatParamError(
  method: string,
  raw: unknown,
  cause: unknown,
): { code: string; message: string } {
  const secrets = collectParamSecrets(raw);
  const detail = cause instanceof Error ? cause.message : String(cause ?? "");
  return safeErrorBody("invalid_request", `${method} params invalid: ${detail}`, secrets);
}
