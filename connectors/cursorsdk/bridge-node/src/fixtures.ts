import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

/** Shared Go/TS fixture root: ../../internal/product/testdata/fixtures */
export function fixtureRoot(): string {
  return join(here, "..", "..", "internal", "product", "testdata", "fixtures");
}

export function readFixture(rel: string): string {
  return readFileSync(join(fixtureRoot(), rel), "utf8");
}

export function readFixtureJSON<T>(rel: string): T {
  return JSON.parse(readFixture(rel)) as T;
}
