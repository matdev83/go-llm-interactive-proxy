import { createRequire } from "node:module";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { PINNED_SDK_VERSION } from "./protocol.js";

const require = createRequire(import.meta.url);

export interface SdkRuntime {
  packageVersion: string;
  Cursor: {
    models: {
      list(opts: { apiKey: string }): Promise<unknown[]>;
    };
  };
  Agent: Record<string, unknown>;
  sandboxSupported?: boolean;
}

export interface SdkPackageLocator {
  resolveFilename(request: string): string;
  readFileSync(path: string, encoding: "utf8"): string;
  existsSync(path: string): boolean;
  dirname(path: string): string;
  join(...paths: string[]): string;
}

export interface LoadPublishedSdkOptions {
  locator?: SdkPackageLocator;
  importSdk?: () => Promise<{
    Cursor?: SdkRuntime["Cursor"];
    Agent?: Record<string, unknown>;
  }>;
}

const defaultLocator: SdkPackageLocator = {
  resolveFilename: (request) => require.resolve(request),
  readFileSync: (path, encoding) => readFileSync(path, encoding),
  existsSync,
  dirname,
  join,
};

/** Strip absolute/home paths and noisy Node detail from metadata errors. */
export function sanitizePackageMetaError(err: unknown): string {
  let message = err instanceof Error ? err.message : String(err ?? "unknown");
  message = message.replace(/[A-Za-z]:\\[^\s"']+/g, "[path]");
  message = message.replace(/\/(?:home|Users|tmp|var|private)\/[^\s"']+/g, "[path]");
  message = message.replace(/open '[^']+'/gi, "open '[path]'");
  message = message.replace(/open "[^"]+"/gi, 'open "[path]"');
  return message.slice(0, 200);
}

/**
 * Discover the installed `@cursor/sdk` version despite package export maps that
 * omit `./package.json`. Resolve the package entry, then walk parents for the
 * package root metadata. Never invent a version when metadata is missing.
 */
export function readInstalledCursorSdkVersion(
  locator: SdkPackageLocator = defaultLocator,
): string {
  let entry: string;
  try {
    entry = locator.resolveFilename("@cursor/sdk");
  } catch (err) {
    throw new Error(`@cursor/sdk is not installed: ${sanitizePackageMetaError(err)}`);
  }

  let dir = locator.dirname(entry);
  for (let i = 0; i < 12; i += 1) {
    const pkgPath = locator.join(dir, "package.json");
    if (locator.existsSync(pkgPath)) {
      let raw: string;
      try {
        raw = locator.readFileSync(pkgPath, "utf8");
      } catch (err) {
        throw new Error(
          `@cursor/sdk package metadata unreadable: ${sanitizePackageMetaError(err)}`,
        );
      }
      let parsed: { name?: unknown; version?: unknown };
      try {
        parsed = JSON.parse(raw) as { name?: unknown; version?: unknown };
      } catch {
        throw new Error("@cursor/sdk package metadata unreadable: invalid JSON");
      }
      if (parsed.name === "@cursor/sdk") {
        if (typeof parsed.version !== "string" || parsed.version.trim() === "") {
          throw new Error("@cursor/sdk package metadata missing version");
        }
        return parsed.version;
      }
    }
    const parent = locator.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error("@cursor/sdk package metadata not found");
}

export function assertPinnedSdkVersion(version: string): void {
  if (version !== PINNED_SDK_VERSION) {
    throw new Error(
      `@cursor/sdk version mismatch: expected ${PINNED_SDK_VERSION}, found ${version}`,
    );
  }
}

/** Lazy-load the pinned official SDK without top-level import side effects. */
export async function loadPublishedSdk(
  options: LoadPublishedSdkOptions = {},
): Promise<SdkRuntime> {
  const locator = options.locator ?? defaultLocator;
  const packageVersion = readInstalledCursorSdkVersion(locator);
  assertPinnedSdkVersion(packageVersion);

  const importSdk = options.importSdk ?? (() => import("@cursor/sdk"));
  const mod = (await importSdk()) as {
    Cursor?: SdkRuntime["Cursor"];
    Agent?: Record<string, unknown>;
  };
  if (!mod.Cursor?.models?.list) {
    throw new Error("@cursor/sdk missing Cursor.models.list");
  }
  return {
    packageVersion,
    Cursor: mod.Cursor,
    Agent: mod.Agent ?? {},
  };
}
