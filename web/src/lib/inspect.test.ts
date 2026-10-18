import { describe, expect, test } from "bun:test";
import { inspect } from "./inspect";

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
  test("rejects junk", () => {
    expect(inspect(new TextEncoder().encode("hello world")).ok).toBe(false);
    expect(inspect(new Uint8Array(3)).ok).toBe(false);
  });

  test("rejects an empty model", () => {
    expect(inspect(mim())).toEqual({ ok: false, reason: "This file has no weights in it." });
  });

  test("rejects a safetensors file", () => {
    const json = new TextEncoder().encode(JSON.stringify({ "0.bias": { dtype: "F32", shape: [10], data_offsets: [0, 40] } }));
    const out = new Uint8Array(8 + json.length + 40);
    new DataView(out.buffer).setBigUint64(0, BigInt(json.length), true);
    out.set(json, 8);
    expect(inspect(out)).toEqual({ ok: false, reason: "This isn't a .mim, ONNX or GGUF model file." });
  });

  test("counts the tensors and parameters of a .mim file", () => {
    expect(inspect(mim([10, 784], [10], []))).toEqual({ ok: true, format: "mim", tensors: 3, parameters: 7840 + 10 + 1 });
  });

  test("takes a GGUF file and counts its tensors", () => {
    const out = new Uint8Array(24);
    out.set([0x47, 0x47, 0x55, 0x46, 3]);
    new DataView(out.buffer).setBigUint64(8, 201n, true);
    expect(inspect(out)).toEqual({ ok: true, format: "gguf", tensors: 201 });
    expect(inspect(out.subarray(0, 12)).ok).toBe(false);
  });

  test("takes an ONNX model by its first field or its name", () => {
    expect(inspect(new Uint8Array([0x08, 0x07, 0x12]))).toEqual({ ok: true, format: "onnx" });
    expect(inspect(new Uint8Array([0x0a, 0x07]), "net.ONNX")).toEqual({ ok: true, format: "onnx" });
  });

  test("rejects a cut-off .mim file", () => {
    expect(inspect(mim([10, 784]).subarray(0, 100)).ok).toBe(false);
  });
});
