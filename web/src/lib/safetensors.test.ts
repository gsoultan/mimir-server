import { describe, expect, test } from "bun:test";
import { inspect } from "./safetensors";

function file(header: object): Uint8Array {
  const json = new TextEncoder().encode(JSON.stringify(header));
  const out = new Uint8Array(8 + json.length);
  new DataView(out.buffer).setBigUint64(0, BigInt(json.length), true);
  out.set(json, 8);
  return out;
}

describe("inspect", () => {
  test("counts tensors and parameters", () => {
    const r = inspect(file({
      __metadata__: { format: "pt" },
      "0.weight": { dtype: "F32", shape: [256, 784], data_offsets: [0, 0] },
      "0.bias": { dtype: "F32", shape: [256], data_offsets: [0, 0] },
    }));
    expect(r).toEqual({ ok: true, tensors: 2, parameters: 256 * 784 + 256 });
  });

  test("rejects junk", () => {
    expect(inspect(new TextEncoder().encode("hello world")).ok).toBe(false);
    expect(inspect(new Uint8Array(3)).ok).toBe(false);
  });

  test("rejects an empty model", () => {
    expect(inspect(file({ __metadata__: {} }))).toEqual({ ok: false, reason: "This file has no weights in it." });
  });
});
