#!/usr/bin/env node
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const PINNED_SDK_VERSION = "1.0.23";
const MIN_NODE_MAJOR = 22;
const MIN_NODE_MINOR = 13;

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");

function readPackageJSON() {
  return JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
}

function nodeMeetsEngine(version = process.versions.node) {
  const parts = version.split(".").map((p) => Number(p));
  const major = parts[0] ?? 0;
  const minor = parts[1] ?? 0;
  return major > MIN_NODE_MAJOR || (major === MIN_NODE_MAJOR && minor >= MIN_NODE_MINOR);
}

function printVersion() {
  const pkg = readPackageJSON();
  process.stdout.write(
    `lip-cursor-sdk-bridge/${pkg.version} @cursor/sdk@${PINNED_SDK_VERSION} node/${process.versions.node}\n`,
  );
}

function runDoctor() {
  const pkg = readPackageJSON();
  const issues = [];
  if (!nodeMeetsEngine()) {
    issues.push(`node ${process.versions.node} < ${MIN_NODE_MAJOR}.${MIN_NODE_MINOR}`);
  }
  const sdkPkgPath = join(root, "node_modules", "@cursor", "sdk", "package.json");
  let sdkVersion = "";
  if (!existsSync(sdkPkgPath)) {
    issues.push("@cursor/sdk not installed in companion package");
  } else {
    sdkVersion = JSON.parse(readFileSync(sdkPkgPath, "utf8")).version ?? "";
    if (sdkVersion !== PINNED_SDK_VERSION) {
      issues.push(`@cursor/sdk ${sdkVersion} != pinned ${PINNED_SDK_VERSION}`);
    }
  }
  if (issues.length > 0) {
    process.stdout.write(
      `doctor: fail bridge=${pkg.version} sdk=${sdkVersion || "missing"} node=${process.versions.node}\n`,
    );
    for (const issue of issues) {
      process.stderr.write(`${issue}\n`);
    }
    process.exitCode = 1;
    return;
  }
  process.stdout.write(
    `doctor: ok bridge=${pkg.version} sdk=${PINNED_SDK_VERSION} node=${process.versions.node}\n`,
  );
}

async function runServer() {
  const entry = join(root, "dist", "main.js");
  if (!existsSync(entry)) {
    process.stderr.write(
      "lip-cursor-sdk-bridge: missing dist/main.js; run npm run build in the companion package\n",
    );
    process.exitCode = 1;
    return;
  }
  await import(pathToFileURL(entry).href);
}

const args = process.argv.slice(2);
if (args.includes("--version") || args.includes("-v")) {
  printVersion();
} else if (args[0] === "doctor") {
  runDoctor();
} else if (args.length > 0 && args[0] !== "--") {
  process.stderr.write(`lip-cursor-sdk-bridge: unknown argument ${args[0]}\n`);
  process.exitCode = 1;
} else {
  await runServer();
}
