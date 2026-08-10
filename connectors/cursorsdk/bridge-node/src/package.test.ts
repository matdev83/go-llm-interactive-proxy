import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { MIN_NODE_ENGINE, PINNED_SDK_VERSION } from "./protocol.js";

const here = dirname(fileURLToPath(import.meta.url));
const bridgeRoot = join(here, "..");

function readJSON(path: string): Record<string, unknown> {
  return JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
}

test("package.json pins exact SDK, engines, bin, and pack files", () => {
  const pkg = readJSON(join(bridgeRoot, "package.json"));
  assert.equal(pkg.name, "lip-cursor-sdk-bridge");
  assert.equal(typeof pkg.version, "string");
  assert.deepEqual(pkg.engines, { node: MIN_NODE_ENGINE });

  const deps = pkg.dependencies as Record<string, string>;
  assert.equal(deps["@cursor/sdk"], PINNED_SDK_VERSION);
  assert.ok(!deps["@cursor/sdk"].startsWith("^"));
  assert.ok(!deps["@cursor/sdk"].startsWith("~"));

  const bin = pkg.bin as Record<string, string>;
  assert.equal(bin["lip-cursor-sdk-bridge"], "bin/lip-cursor-sdk-bridge.js");
  assert.ok(existsSync(join(bridgeRoot, "bin", "lip-cursor-sdk-bridge.js")));

  const files = pkg.files as string[];
  assert.ok(files.includes("dist/**/*.js"));
  assert.ok(files.includes("dist/**/*.d.ts"));
  assert.ok(files.includes("bin/**"));
  assert.ok(files.includes("README.md"));

  const scripts = (pkg.scripts ?? {}) as Record<string, string>;
  for (const hook of ["preinstall", "install", "postinstall", "prepare", "prepublishOnly"]) {
    assert.equal(scripts[hook], undefined, `${hook} must not run product behavior`);
  }
  assert.ok(
    typeof scripts.test === "string" && scripts.test.includes("src/*.test.ts"),
    "npm test must glob src/*.test.ts so new test files are auto-discovered",
  );
});

test("package-lock.json pins @cursor/sdk exact version", () => {
  const lock = readJSON(join(bridgeRoot, "package-lock.json"));
  const packages = lock.packages as Record<string, { version?: string; dependencies?: Record<string, string> }>;
  const root = packages[""];
  assert.equal(root?.dependencies?.["@cursor/sdk"], PINNED_SDK_VERSION);
  const sdk = packages["node_modules/@cursor/sdk"];
  assert.ok(sdk, "lockfile must contain node_modules/@cursor/sdk");
  assert.equal(sdk.version, PINNED_SDK_VERSION);
});

test("--version reports bridge and pinned SDK without loading credentials", () => {
  const bin = join(bridgeRoot, "bin", "lip-cursor-sdk-bridge.js");
  const result = spawnSync(process.execPath, [bin, "--version"], {
    cwd: bridgeRoot,
    encoding: "utf8",
    env: {
      ...process.env,
      CURSOR_API_KEY: "crsr_must_not_be_loaded",
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /lip-cursor-sdk-bridge\/\d+\.\d+\.\d+/);
  assert.match(result.stdout, new RegExp(`@cursor/sdk@${PINNED_SDK_VERSION}`));
  assert.ok(!result.stdout.includes("crsr_must_not_be_loaded"));
  assert.ok(!result.stderr.includes("crsr_must_not_be_loaded"));
});

test("doctor reports runtime readiness without creating agents or using credentials", () => {
  const bin = join(bridgeRoot, "bin", "lip-cursor-sdk-bridge.js");
  const result = spawnSync(process.execPath, [bin, "doctor"], {
    cwd: bridgeRoot,
    encoding: "utf8",
    env: {
      ...process.env,
      CURSOR_API_KEY: "crsr_doctor_must_not_use",
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /ok/);
  assert.match(result.stdout, /node=/);
  assert.match(result.stdout, new RegExp(`sdk=${PINNED_SDK_VERSION}`));
  assert.ok(!result.stdout.toLowerCase().includes("agent"));
  assert.ok(!result.stdout.includes("crsr_doctor_must_not_use"));
  assert.ok(!result.stderr.includes("crsr_doctor_must_not_use"));
});

test("npm pack dry-run includes bin and dist, excludes tests and secrets", () => {
  const build = spawnSync("npm", ["run", "build"], {
    cwd: bridgeRoot,
    encoding: "utf8",
    shell: process.platform === "win32",
  });
  assert.equal(build.status, 0, build.stderr || build.stdout);

  const result = spawnSync("npm", ["pack", "--dry-run", "--json"], {
    cwd: bridgeRoot,
    encoding: "utf8",
    shell: process.platform === "win32",
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  const parsed = JSON.parse(result.stdout) as
    | Array<{ files?: Array<{ path: string }> }>
    | Record<string, { files?: Array<{ path: string }> }>;
  const entry = Array.isArray(parsed)
    ? parsed[0]
    : (parsed["lip-cursor-sdk-bridge"] ?? Object.values(parsed)[0]);
  const files = (entry?.files ?? []).map((f) => f.path.replace(/\\/g, "/"));
  assert.ok(files.length > 0, "pack dry-run returned no files");
  assert.ok(files.some((p) => p === "bin/lip-cursor-sdk-bridge.js" || p.endsWith("bin/lip-cursor-sdk-bridge.js")));
  assert.ok(files.some((p) => p.includes("package.json")));
  assert.ok(files.some((p) => p === "dist/main.js" || p.endsWith("dist/main.js")));
  assert.ok(files.some((p) => p === "dist/server.js" || p.endsWith("dist/server.js")));
  assert.ok(!files.some((p) => p.includes(".test.")));
  assert.ok(!files.some((p) => p.includes("sdkMock")));
  assert.ok(!files.some((p) => p.includes("liveProbe")));
  assert.ok(!files.some((p) => p.includes("node_modules/")));
  assert.ok(!files.some((p) => /secret|credential|\.env/i.test(p)));
});
