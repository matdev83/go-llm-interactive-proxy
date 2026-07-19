import assert from "node:assert/strict";
import { test } from "node:test";
import { readFixtureJSON } from "./fixtures.js";
import { normalizeModelRows } from "./models.js";

test("normalizeModelRows keeps anonymous SDK variants with params maps and no invented id", () => {
  const fixture = readFixtureJSON<{ models: unknown[] }>("models_sdk_variants_missing_id.json");
  const rows = normalizeModelRows(fixture.models);
  assert.equal(rows.length, 2);

  const gpt = rows.find((r) => r.id === "gpt-5.3-codex");
  assert.ok(gpt);
  assert.deepEqual(gpt!.parameters, [
    { id: "reasoning", values: ["low", "medium", "high", "xhigh"] },
  ]);
  assert.ok(gpt!.parameters?.[0]?.values?.includes("xhigh"));
  assert.ok(!gpt!.parameters?.[0]?.values?.includes("extra-high"));
  assert.equal(gpt!.variants?.length, 1);
  assert.equal(gpt!.variants?.[0]?.id, undefined);
  assert.equal(gpt!.variants?.[0]?.displayName, "Extra High");
  assert.deepEqual(gpt!.variants?.[0]?.params, { reasoning: "xhigh" });
  assert.ok(!JSON.stringify(gpt!.variants).includes('"id"'));

  const claude = rows.find((r) => r.id === "claude-4.6-sonnet-thinking");
  assert.ok(claude);
  assert.deepEqual(claude!.parameters?.find((p) => p.id === "effort")?.values, [
    "low",
    "medium",
    "high",
    "extra-high",
  ]);
  assert.ok(!claude!.parameters?.find((p) => p.id === "effort")?.values?.includes("xhigh"));
  assert.equal(claude!.variants?.length, 2);
  for (const v of claude!.variants ?? []) {
    assert.equal(v.id, undefined);
    assert.ok(v.params);
    assert.equal(typeof v.params!.thinking, "boolean");
  }
  assert.deepEqual(claude!.variants?.[0]?.params, { effort: "high", thinking: true });
  assert.deepEqual(claude!.variants?.[1]?.params, { effort: "extra-high", thinking: true });
});

test("normalizeModelRows rejects missing base model id", () => {
  assert.throws(
    () => normalizeModelRows([{ displayName: "No ID", parameters: [], variants: [] }]),
    /models\[0\]: missing id/,
  );
});

test("normalizeModelRows omits empty anonymous variants and rejects conflicting params", () => {
  const omitted = normalizeModelRows([
    {
      id: "m1",
      displayName: "M1",
      variants: [{ displayName: "empty", params: [] }],
    },
  ]);
  assert.deepEqual(omitted[0]?.variants, []);

  assert.throws(
    () =>
      normalizeModelRows([
        {
          id: "m2",
          displayName: "M2",
          variants: [
            {
              displayName: "bad",
              params: [
                { id: "effort", value: "high" },
                { id: "effort", value: "low" },
              ],
            },
          ],
        },
      ]),
    /conflicting/,
  );

  assert.throws(
    () =>
      normalizeModelRows([
        {
          id: "m3",
          displayName: "M3",
          variants: [{ displayName: "bad", params: [{ id: "", value: "x" }] }],
        },
      ]),
    /params entry missing id/,
  );
});

test("normalizeModelRows keeps identified variants and exact reasoning distinctions", () => {
  const fixture = readFixtureJSON<{
    models: unknown[];
    distinctions: Record<string, string>;
  }>("models_sanitized.json");
  const rows = normalizeModelRows(fixture.models);
  const byId = new Map(rows.map((r) => [r.id, r]));

  const reasoningID = fixture.distinctions.reasoningParam;
  assert.ok(typeof reasoningID === "string" && reasoningID.length > 0);
  const reasoning = byId.get(reasoningID);
  assert.ok(reasoning);
  assert.ok(reasoning!.parameters?.some((p) => p.id === "reasoning" && p.values?.includes("xhigh")));
  assert.ok(!reasoning!.parameters?.some((p) => p.id === "reasoning" && p.values?.includes("extra-high")));
  assert.equal(reasoning!.variants?.[0]?.id, "reasoning-xhigh");
  assert.deepEqual(reasoning!.variants?.[0]?.params, { reasoning: "xhigh" });

  const effortID = fixture.distinctions.effortPlusThinking;
  assert.ok(typeof effortID === "string" && effortID.length > 0);
  const effort = byId.get(effortID);
  assert.ok(effort);
  assert.ok(effort!.parameters?.some((p) => p.id === "effort" && p.values?.includes("extra-high")));
  assert.ok(!effort!.parameters?.some((p) => p.id === "effort" && p.values?.includes("xhigh")));
  assert.ok(effort!.variants?.some((v) => v.id === "effort-extra-high-thinking"));
  assert.deepEqual(
    effort!.variants?.find((v) => v.id === "effort-extra-high-thinking")?.params,
    { effort: "extra-high", thinking: true },
  );
});
