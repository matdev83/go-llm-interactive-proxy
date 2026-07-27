import assert from "node:assert/strict";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { PINNED_SDK_VERSION } from "./protocol.js";
import {
  loadPublishedSdk,
  readInstalledCursorSdkVersion,
  type SdkPackageLocator,
} from "./sdk_runtime.js";

function fixturePackage(version: string): { root: string; entry: string } {
  const root = mkdtempSync(join(tmpdir(), "cursor-sdk-meta-"));
  mkdirSync(join(root, "dist", "cjs"), { recursive: true });
  writeFileSync(
    join(root, "package.json"),
    JSON.stringify({
      name: "@cursor/sdk",
      version,
      exports: {
        ".": {
          require: "./dist/cjs/index.js",
          import: "./dist/esm/index.js",
        },
      },
    }),
    "utf8",
  );
  const entry = join(root, "dist", "cjs", "index.js");
  writeFileSync(entry, "module.exports = {};\n", "utf8");
  return { root, entry };
}

function locatorForEntry(entry: string, overrides: Partial<SdkPackageLocator> = {}): SdkPackageLocator {
  return {
    resolveFilename: (request: string) => {
      if (request === "@cursor/sdk") return entry;
      if (request === "@cursor/sdk/package.json") {
        const err = new Error(
          `Package subpath './package.json' is not defined by "exports"`,
        ) as NodeJS.ErrnoException;
        err.code = "ERR_PACKAGE_PATH_NOT_EXPORTED";
        throw err;
      }
      throw new Error(`unexpected resolve ${request}`);
    },
    readFileSync: (path, encoding) => readFileSync(path, encoding),
    existsSync: (path) => existsSync(path),
    dirname,
    join,
    ...overrides,
  };
}

test("readInstalledCursorSdkVersion finds version via entry resolve + filesystem walk", () => {
  const { entry } = fixturePackage(PINNED_SDK_VERSION);
  const version = readInstalledCursorSdkVersion(locatorForEntry(entry));
  assert.equal(version, PINNED_SDK_VERSION);
});

test("readInstalledCursorSdkVersion works when package.json is not an export subpath", () => {
  const { entry } = fixturePackage(PINNED_SDK_VERSION);
  const locator = locatorForEntry(entry);
  assert.throws(() => locator.resolveFilename("@cursor/sdk/package.json"), /ERR_PACKAGE_PATH_NOT_EXPORTED|exports/);
  assert.equal(readInstalledCursorSdkVersion(locator), PINNED_SDK_VERSION);
});

test("readInstalledCursorSdkVersion rejects missing package metadata", () => {
  const orphan = join(mkdtempSync(join(tmpdir(), "cursor-sdk-orphan-")), "index.js");
  writeFileSync(orphan, "", "utf8");
  assert.throws(
    () => readInstalledCursorSdkVersion(locatorForEntry(orphan)),
    /package metadata not found/,
  );
});

test("readInstalledCursorSdkVersion rejects unreadable metadata without leaking absolute paths", () => {
  const { root, entry } = fixturePackage(PINNED_SDK_VERSION);
  const pkgPath = join(root, "package.json");
  const locator = locatorForEntry(entry, {
    readFileSync: () => {
      throw new Error(`EACCES: permission denied, open '${pkgPath}'`);
    },
  });
  try {
    readInstalledCursorSdkVersion(locator);
    assert.fail("expected throw");
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : String(caught);
    assert.match(message, /unreadable|metadata/i);
    assert.equal(message.includes(pkgPath), false);
    assert.equal(message.includes(root), false);
    assert.match(message, /\[path\]/);
  }
});

test("loadPublishedSdk rejects version mismatch before importing SDK", async () => {
  const { entry } = fixturePackage("9.9.9");
  let imported = false;
  await assert.rejects(
    () =>
      loadPublishedSdk({
        locator: locatorForEntry(entry),
        importSdk: async () => {
          imported = true;
          return { Cursor: { models: { list: async () => [] } }, Agent: {} };
        },
      }),
    /version mismatch.*1\.0\.23.*9\.9\.9/,
  );
  assert.equal(imported, false);
});

test("loadPublishedSdk rejects unreadable metadata before importing SDK", async () => {
  const { entry } = fixturePackage(PINNED_SDK_VERSION);
  let imported = false;
  await assert.rejects(
    () =>
      loadPublishedSdk({
        locator: locatorForEntry(entry, {
          readFileSync: () => {
            throw new Error("EIO boom");
          },
        }),
        importSdk: async () => {
          imported = true;
          return { Cursor: { models: { list: async () => [] } }, Agent: {} };
        },
      }),
    /unreadable|metadata/i,
  );
  assert.equal(imported, false);
});

test("loadPublishedSdk returns pinned version when metadata matches", async () => {
  const { entry } = fixturePackage(PINNED_SDK_VERSION);
  const runtime = await loadPublishedSdk({
    locator: locatorForEntry(entry),
    importSdk: async () => ({
      Cursor: { models: { list: async () => [{ id: "m" }] } },
      Agent: { create: async () => ({}) },
    }),
  });
  assert.equal(runtime.packageVersion, PINNED_SDK_VERSION);
  assert.equal(typeof runtime.Cursor.models.list, "function");
});
