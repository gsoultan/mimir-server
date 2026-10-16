// A quick look inside a .safetensors file, so the upload screen can say
// what's in it before sending anything. The server does the real check.

export type Inspection =
  | { ok: true; tensors: number; parameters: number }
  | { ok: false; reason: string };

const maxHeader = 100 << 20;

export function inspect(bytes: Uint8Array): Inspection {
  if (bytes.length < 8) return bad();
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const size = Number(view.getBigUint64(0, true));
  if (size < 2 || size > maxHeader || 8 + size > bytes.length) return bad();

  let header: unknown;
  try {
    header = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(8, 8 + size)));
  } catch {
    return bad();
  }
  if (typeof header !== "object" || header === null || Array.isArray(header)) return bad();

  let tensors = 0;
  let parameters = 0;
  for (const [name, info] of Object.entries(header)) {
    if (name === "__metadata__") continue;
    const shape = (info as { shape?: unknown }).shape;
    if (!Array.isArray(shape) || !shape.every((d) => Number.isInteger(d) && d >= 0)) return bad();
    tensors++;
    parameters += shape.reduce((a: number, b: number) => a * b, 1);
  }
  if (tensors === 0) return { ok: false, reason: "This file has no weights in it." };
  return { ok: true, tensors, parameters };
}

function bad(): Inspection {
  return { ok: false, reason: "This isn't a .safetensors model file." };
}
