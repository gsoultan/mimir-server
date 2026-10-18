import { describe, expect, test } from "bun:test";
import { inspect } from "./safetensors";

function file(header: object): Uint8Array {
  const json = new TextEncoder().encode(JSON.stringify(header));
  const out = new Uint8Array(8 + json.length);
  new DataView(out.buffer).setBigUint64(0, BigInt(json.length), true);
  out.set(json, 8);
  return out;
}

// mim returns the start of a Mimir Model file holding tensors of the shapes
// given: the preamble and the index, with one section entry.
function mim(...shapes: number[][]): Uint8Array {
  const index = 64;
  const out = new Uint8Array(index + 16 + 64 + 96 * shapes.length);
  const view = new DataView(out.buffer);
  out.set([0x4d, 0x49, 0x4d, 0]);
  view.setBigUint64(16, BigInt(index), true);
  view.setUint32(index, 1, true);
  view.setUint32(index + 4, shapes.length, true);
  shapes.forEach((shape, i) => {
    const entry = index + 16 + 64 + 96 * i;
    view.setUint8(entry + 12, shape.length);
    shape.forEach((d, j) => view.setBigUint64(entry + 16 + 8 * j, BigInt(d), true));
  });
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

  test("reads the tensors of a .mim file", () => {
    expect(inspect(mim([10, 784], [10], []))).toEqual({ ok: true, tensors: 3, parameters: 7840 + 10 + 1 });
  });

  test("rejects a cut-off .mim file", () => {
    expect(inspect(mim([10, 784]).subarray(0, 100)).ok).toBe(false);
  });
});
