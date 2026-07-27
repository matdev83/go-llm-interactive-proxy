import { collectParamSecrets, redactSecrets, safeErrorBody } from "./params.js";
import type { ErrorBody } from "./protocol.js";

export const MAX_STDERR_BYTES = 8 * 1024;

export function redactMessage(message: string, secrets: string[] = []): string {
  let out = redactSecrets(message, secrets);
  out = out.replace(/sk-[A-Za-z0-9_-]+/g, "[REDACTED]");
  out = out.replace(/crsr_[A-Za-z0-9_-]+/g, "[REDACTED]");
  out = out.replace(/apiKey["']?\s*[:=]\s*["']?[^"',\s}]+/gi, "apiKey=[REDACTED]");
  return out;
}

export function bridgeErrorBody(
  code: string,
  message: string,
  secrets: string[] = [],
): ErrorBody {
  return safeErrorBody(code, redactMessage(message, secrets), secrets);
}

export function formatSdkLoadError(cause: unknown, secrets: string[] = []): ErrorBody {
  const detail = cause instanceof Error ? cause.message : String(cause ?? "unknown");
  return bridgeErrorBody("sdk_load_failed", `SDK load failed: ${detail}`, secrets);
}

export function formatMethodError(
  method: string,
  rawParams: unknown,
  cause: unknown,
  code = "invalid_request",
): ErrorBody {
  const secrets = collectParamSecrets(rawParams);
  const detail = cause instanceof Error ? cause.message : String(cause ?? "");
  return bridgeErrorBody(`${code}`, `${method} failed: ${detail}`, secrets);
}

export function writeBoundedStderr(
  stream: NodeJS.WritableStream,
  text: string,
  currentBytes: { value: number },
): void {
  if (!text || currentBytes.value >= MAX_STDERR_BYTES) return;
  const sanitized = redactMessage(text);
  const room = MAX_STDERR_BYTES - currentBytes.value;
  const slice = sanitized.slice(0, room);
  currentBytes.value += Buffer.byteLength(slice, "utf8");
  stream.write(slice.endsWith("\n") ? slice : `${slice}\n`);
}
