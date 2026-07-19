export const SCHEMA_VERSION = 1;
export const MAX_FRAME_BYTES = 16 * 1024 * 1024;
export const PINNED_SDK_VERSION = "1.0.23";
export const MIN_NODE_ENGINE = ">=22.13";

export const TYPE_REQUEST = "request";
export const TYPE_RESPONSE = "response";
export const TYPE_EVENT = "event";

export const METHODS = [
  "bridge/initialize",
  "bridge/health",
  "models/list",
  "agent/create",
  "agent/send",
  "run/cancel",
  "agent/dispose",
  "bridge/shutdown",
] as const;

export type Method = (typeof METHODS)[number];

export const EVENT_KINDS = [
  "text_delta",
  "reasoning_delta",
  "usage",
  "warning",
  "activity",
  "finished",
  "error",
] as const;

export type EventKind = (typeof EVENT_KINDS)[number];

export const TERMINAL_KINDS = new Set<EventKind>(["finished", "error"]);

export type ErrorClass =
  | "frame_too_large"
  | "invalid_json"
  | "incompatible_version"
  | "unknown_type"
  | "unknown_method"
  | "invalid_request"
  | "response_mismatch"
  | "invalid_event"
  | "unknown_event_kind"
  | "sequence_regression"
  | "duplicate_terminal";

export class ProtocolError extends Error {
  readonly className: ErrorClass;

  constructor(className: ErrorClass, message: string) {
    super(`${className}: ${message}`);
    this.name = "ProtocolError";
    this.className = className;
  }
}

export interface ErrorBody {
  code: string;
  message: string;
}

export interface Frame {
  schemaVersion: number;
  implVersion?: string;
  type: string;
  id?: string;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: ErrorBody;
  runId?: string;
  seq?: number;
  kind?: string;
  payload?: unknown;
}

export function isRequiredMethod(method: string): method is Method {
  return (METHODS as readonly string[]).includes(method);
}

export function isEventKind(kind: string): kind is EventKind {
  return (EVENT_KINDS as readonly string[]).includes(kind);
}

export function isTerminalKind(kind: string): boolean {
  return TERMINAL_KINDS.has(kind as EventKind);
}

export function validateFrame(frame: Frame): void {
  if (frame.schemaVersion !== SCHEMA_VERSION) {
    throw new ProtocolError(
      "incompatible_version",
      `schemaVersion=${frame.schemaVersion}`,
    );
  }
  switch (frame.type) {
    case TYPE_REQUEST:
      if (!frame.id) throw new ProtocolError("invalid_request", "missing id");
      if (!frame.method) throw new ProtocolError("invalid_request", "missing method");
      if (!isRequiredMethod(frame.method)) {
        throw new ProtocolError("unknown_method", frame.method);
      }
      return;
    case TYPE_RESPONSE:
      if (!frame.id) throw new ProtocolError("response_mismatch", "missing id");
      if (frame.method && !isRequiredMethod(frame.method)) {
        throw new ProtocolError("unknown_method", frame.method);
      }
      {
        const hasResult = frame.result !== undefined;
        const hasError = frame.error != null;
        if (!hasResult && !hasError) {
          throw new ProtocolError("response_mismatch", "missing result and error");
        }
        if (hasResult && hasError) {
          throw new ProtocolError(
            "response_mismatch",
            "result and error are mutually exclusive",
          );
        }
      }
      return;
    case TYPE_EVENT:
      if (!frame.runId) throw new ProtocolError("invalid_event", "missing runId");
      if (frame.seq == null) throw new ProtocolError("invalid_event", "missing seq");
      if (typeof frame.seq !== "number" || !Number.isInteger(frame.seq)) {
        throw new ProtocolError("invalid_event", "seq must be an integer");
      }
      if (frame.seq < 1) throw new ProtocolError("invalid_event", "seq must be >= 1");
      if (!frame.kind) throw new ProtocolError("invalid_event", "missing kind");
      if (!isEventKind(frame.kind)) {
        throw new ProtocolError("unknown_event_kind", frame.kind);
      }
      return;
    default:
      throw new ProtocolError("unknown_type", frame.type);
  }
}

export function decodeLine(line: string | Buffer): Frame {
  const bytes = typeof line === "string" ? Buffer.byteLength(line, "utf8") : line.length;
  if (bytes > MAX_FRAME_BYTES) {
    throw new ProtocolError("frame_too_large", `${bytes} > ${MAX_FRAME_BYTES}`);
  }
  const text = (typeof line === "string" ? line : line.toString("utf8")).trim();
  if (!text) throw new ProtocolError("invalid_json", "empty line");
  if (!text.startsWith("{")) {
    throw new ProtocolError("invalid_json", "frame must be a JSON object");
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    throw new ProtocolError("invalid_json", err instanceof Error ? err.message : String(err));
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new ProtocolError("invalid_json", "frame must be a JSON object");
  }
  const frame = parsed as Frame;
  if (typeof frame.schemaVersion !== "number") {
    throw new ProtocolError("incompatible_version", "missing schemaVersion");
  }
  validateFrame(frame);
  return frame;
}

export function encodeFrame(frame: Frame): string {
  // Non-mutating: copy before applying default schemaVersion.
  const withVersion: Frame = {
    ...frame,
    schemaVersion: frame.schemaVersion || SCHEMA_VERSION,
  };
  validateFrame(withVersion);
  const raw = JSON.stringify(withVersion);
  if (Buffer.byteLength(raw, "utf8") > MAX_FRAME_BYTES) {
    throw new ProtocolError("frame_too_large", "encoded frame exceeds limit");
  }
  return raw;
}

export function matchResponse(req: Frame, resp: Frame): void {
  validateFrame(req);
  validateFrame(resp);
  if (req.type !== TYPE_REQUEST) {
    throw new ProtocolError("response_mismatch", "expected request");
  }
  if (resp.type !== TYPE_RESPONSE) {
    throw new ProtocolError("response_mismatch", "expected response");
  }
  if (req.id !== resp.id) {
    throw new ProtocolError("response_mismatch", "id mismatch");
  }
  if (resp.method && resp.method !== req.method) {
    throw new ProtocolError("response_mismatch", "method mismatch");
  }
}

export class RunSequencer {
  private readonly lastSeq = new Map<string, number>();
  private readonly terminal = new Set<string>();

  accept(frame: Frame): void {
    validateFrame(frame);
    if (frame.type !== TYPE_EVENT) {
      throw new ProtocolError("invalid_event", "expected event");
    }
    const runId = frame.runId!;
    if (this.terminal.has(runId)) {
      throw new ProtocolError("duplicate_terminal", "run already terminated");
    }
    const seq = frame.seq!;
    const prev = this.lastSeq.get(runId) ?? 0;
    if (seq <= prev) {
      throw new ProtocolError("sequence_regression", "seq must increase");
    }
    this.lastSeq.set(runId, seq);
    if (isTerminalKind(frame.kind!)) {
      this.terminal.add(runId);
    }
  }

  terminated(runId: string): boolean {
    return this.terminal.has(runId);
  }
}
