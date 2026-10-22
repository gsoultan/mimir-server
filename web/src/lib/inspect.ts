// A quick look inside a model file, so the upload screen can say what's in
// it before sending anything: a Mimir Model (.mim) file, or an ONNX or GGUF
// model the server converts to one. The server does the real check.

export type Format = "mim" | "onnx" | "gguf";

export type Inspection =
  | { ok: true; format: Format; tensors?: number; parameters?: number }
  | { ok: false; reason: string };

// headLength returns how much of the start of a file inspect needs, given
// its first 64 bytes: a .mim file's preamble and index, or just those 64
// bytes for the other formats. The rest is weights, which inspect never
// reads, so a large model isn't read into memory to look at it.
export function headLength(first: Uint8Array): number {
  if (first.length < mimPreamble || !isMim(first)) return mimPreamble;
  const view = new DataView(first.buffer, first.byteOffset, first.byteLength);
  return Number(view.getBigUint64(16, true) + view.getBigUint64(24, true));
}

export function inspect(bytes: Uint8Array, name = ""): Inspection {
  if (bytes.length >= mimPreamble && isMim(bytes)) {
    return inspectMim(new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength));
  }
  if (bytes.length >= ggufHeader && isGGUF(bytes)) {
    const tensors = Number(new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getBigUint64(8, true));
    return tensors > 0 ? { ok: true, format: "gguf", tensors } : noWeights;
  }
  // ONNX has no magic number; a serialized model starts with its
  // ir_version field.
  if (bytes.length > 1 && (bytes[0] === 0x08 || /\.onnx$/i.test(name))) return { ok: true, format: "onnx" };
  return bad();
}

// A Mimir Model file starts with "MIM\0", or "MMF\0" from before the
// format's rename, and lists its tensors in an index (spec/container.md
// in github.com/gsoultan/mimir-model).
function isMim(bytes: Uint8Array): boolean {
  return (bytes[0] === 0x4d && bytes[1] === 0x49 && bytes[2] === 0x4d && bytes[3] === 0)
    || (bytes[0] === 0x4d && bytes[1] === 0x4d && bytes[2] === 0x46 && bytes[3] === 0);
}

// A GGUF file starts with "GGUF", a version and its tensor count.
function isGGUF(bytes: Uint8Array): boolean {
  return bytes[0] === 0x47 && bytes[1] === 0x47 && bytes[2] === 0x55 && bytes[3] === 0x46;
}

const ggufHeader = 24;
const mimPreamble = 64;
const mimIndexHeader = 16;
const mimSectionEntry = 64;
const mimTensorEntry = 96;
const mimMaxRank = 8;

function inspectMim(view: DataView): Inspection {
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

const noWeights: Inspection = { ok: false, reason: "This file has no weights in it." };

function counted(tensors: number, parameters: number): Inspection {
  if (tensors === 0) return noWeights;
  return { ok: true, format: "mim", tensors, parameters };
}

function bad(): Inspection {
  return { ok: false, reason: "This isn't a .mim, ONNX or GGUF model file." };
}
