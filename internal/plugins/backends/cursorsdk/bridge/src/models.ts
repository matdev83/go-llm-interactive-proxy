export interface ModelParameter {
  id: string;
  type?: string;
  values?: string[];
}

export interface ModelVariant {
  id?: string;
  displayName?: string;
  params?: Record<string, unknown>;
}

export interface ModelRow {
  id: string;
  displayName: string;
  parameters?: ModelParameter[];
  variants?: ModelVariant[];
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
  return value as Record<string, unknown>;
}

function normalizeParameterValue(raw: unknown): string | undefined {
  if (typeof raw === "string" && raw.trim()) return raw.trim();
  const obj = asRecord(raw);
  if (obj && typeof obj.value === "string" && obj.value.trim()) {
    return obj.value.trim();
  }
  return undefined;
}

function normalizeParameter(raw: unknown, index: number): ModelParameter {
  const obj = asRecord(raw);
  if (!obj) throw new Error(`models[${index}].parameters: expected object`);
  const id = typeof obj.id === "string" ? obj.id.trim() : "";
  if (!id) throw new Error(`models[${index}].parameters: missing id`);
  const out: ModelParameter = { id };
  if (typeof obj.type === "string" && obj.type.trim()) {
    out.type = obj.type.trim();
  }
  if (Array.isArray(obj.values)) {
    const values = obj.values
      .map(normalizeParameterValue)
      .filter((v): v is string => typeof v === "string");
    if (values.length > 0) out.values = values;
  }
  return out;
}

function coerceVariantParamValue(paramID: string, value: unknown): unknown {
  if (typeof value === "boolean" || typeof value === "number") return value;
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  if (paramID === "thinking") {
    if (trimmed === "true") return true;
    if (trimmed === "false") return false;
  }
  return trimmed;
}

function normalizeVariantParams(raw: unknown, index: number): Record<string, unknown> {
  if (Array.isArray(raw)) {
    const out: Record<string, unknown> = {};
    for (const entry of raw) {
      const obj = asRecord(entry);
      if (!obj) throw new Error(`models[${index}].variants: params entry must be object`);
      const pid = typeof obj.id === "string" ? obj.id.trim() : "";
      if (!pid) throw new Error(`models[${index}].variants: params entry missing id`);
      if (!Object.prototype.hasOwnProperty.call(obj, "value")) {
        throw new Error(`models[${index}].variants: params entry missing value`);
      }
      const coerced = coerceVariantParamValue(pid, obj.value);
      if (coerced === undefined) {
        throw new Error(`models[${index}].variants: params entry bad value`);
      }
      if (Object.prototype.hasOwnProperty.call(out, pid) && out[pid] !== coerced) {
        throw new Error(`models[${index}].variants: conflicting params for ${pid}`);
      }
      out[pid] = coerced;
    }
    return out;
  }
  const obj = asRecord(raw);
  if (!obj) throw new Error(`models[${index}].variants: params must be object or array`);
  return { ...obj };
}

function normalizeVariant(raw: unknown, index: number): ModelVariant | undefined {
  const obj = asRecord(raw);
  if (!obj) throw new Error(`models[${index}].variants: expected object`);
  const id = typeof obj.id === "string" ? obj.id.trim() : "";
  const displayName =
    typeof obj.displayName === "string" && obj.displayName.trim()
      ? obj.displayName.trim()
      : undefined;

  let params: Record<string, unknown> | undefined;
  if (obj.params !== undefined) {
    params = normalizeVariantParams(obj.params, index);
  }

  if (!id) {
    if (!params || Object.keys(params).length === 0) return undefined;
    const out: ModelVariant = { params };
    if (displayName) out.displayName = displayName;
    return out;
  }

  const out: ModelVariant = { id };
  if (displayName) out.displayName = displayName;
  if (params !== undefined) out.params = params;
  return out;
}

export function normalizeModelRows(raw: unknown): ModelRow[] {
  if (!Array.isArray(raw)) throw new Error("models.list returned non-array");
  return raw.map((entry, index) => {
    const obj = asRecord(entry);
    if (!obj) throw new Error(`models[${index}]: expected object`);
    const id = typeof obj.id === "string" ? obj.id.trim() : "";
    if (!id) throw new Error(`models[${index}]: missing id`);
    const displayName =
      typeof obj.displayName === "string" && obj.displayName.trim()
        ? obj.displayName.trim()
        : id;
    const row: ModelRow = { id, displayName };
    if (obj.parameters !== undefined) {
      if (!Array.isArray(obj.parameters)) {
        throw new Error(`models[${index}].parameters: expected array`);
      }
      row.parameters = obj.parameters.map((p, pi) => normalizeParameter(p, index * 1000 + pi));
    }
    if (obj.variants !== undefined) {
      if (!Array.isArray(obj.variants)) {
        throw new Error(`models[${index}].variants: expected array`);
      }
      row.variants = obj.variants
        .map((v, vi) => normalizeVariant(v, index * 1000 + vi))
        .filter((v): v is ModelVariant => v !== undefined);
    }
    return row;
  });
}
