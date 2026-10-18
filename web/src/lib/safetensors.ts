// A quick look inside a .safetensors or .mim file, so the upload screen
// can say what's in it before sending anything. The server does the real
// check.

export type Inspection =
  | { ok: true; tensors: number; parameters: number }
  | { ok: false; reason: string };

const maxHeader = 100 << 20;

export function inspect(bytes: Uint8Array): Inspection {
  if (bytes.length < 8) return bad();
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  if (isMim(bytes)) return inspectMim(view);
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
  return counted(tensors, parameters);
}

// A Mimir Model file starts with "MIM\0", or "MMF\0" from before the
// format's rename, and lists its tensors in an index (spec/container.md
// in github.com/gsoultan/mmf).
function isMim(bytes: Uint8Array): boolean {
  return (bytes[0] === 0x4d && bytes[1] === 0x49 && bytes[2] === 0x4d && bytes[3] === 0)
    || (bytes[0] === 0x4d && bytes[1] === 0x4d && bytes[2] === 0x46 && bytes[3] === 0);
}

const mimPreamble = 64;
const mimIndexHeader = 16;
const mimSectionEntry = 64;
const mimTensorEntry = 96;
const mimMaxRank = 8;

function inspectMim(view: DataView): Inspection {
  if (view.byteLength < mimPreamble) return bad();
  const index = Number(view.getBigUint64(16, true));
  if (index + mimIndexHeader > view.byteLength) return bad();
  const sections = view.getUint32(index, true);
  const tensors = view.getUint32(index + 4, true);
  const table = index + mimIndexHeader + sections * mimSectionEntry;
  if (table + tensors * mimTensorEntry > view.byteLength) return bad();

  let parameters = 0;
  for (let i = 0; i < tensors; i++) {
    const entry = table + i * mimTensorEntry;
    const rank = view.getUint8(entry + 12);
    if (rank > mimMaxRank) return bad();
    let size = 1;
    for (let d = 0; d < rank; d++) size *= Number(view.getBigUint64(entry + 16 + 8 * d, true));
    parameters += size;
  }
  return counted(tensors, parameters);
}

function counted(tensors: number, parameters: number): Inspection {
  if (tensors === 0) return { ok: false, reason: "This file has no weights in it." };
  return { ok: true, tensors, parameters };
}

function bad(): Inspection {
  return { ok: false, reason: "This isn't a .safetensors or .mim model file." };
}
