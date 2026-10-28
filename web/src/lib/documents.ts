import type { Document } from "../api/gen/mimir/v1/document_pb";
import { JobStatus } from "../api/gen/mimir/v1/training_pb";

// The files a collection can read, as the server's docread.KindOf does.
export const documentTypes = [".txt", ".md", ".markdown", ".html", ".htm", ".docx", ".pdf", ".csv", ".json"];
export const maxDocumentBytes = 64 << 20;

export const readable = (name: string) => documentTypes.some((ext) => name.toLowerCase().endsWith(ext));

export const reading = (d: Document) => d.status === JobStatus.QUEUED || d.status === JobStatus.RUNNING;

// documentStatus says where a document is, in words.
export function documentStatus(d: Document): string {
  switch (d.status) {
    case JobStatus.QUEUED: return "Waiting to be read";
    case JobStatus.RUNNING: return "Reading";
    case JobStatus.SUCCEEDED: return `${Number(d.passages).toLocaleString()} ${d.passages === 1n ? "passage" : "passages"}`;
    case JobStatus.FAILED: return "Couldn't be read";
    default: return "Stopped";
  }
}

export type Piece = { text: string } | { source: number };

// cited splits an answer into its text and the sources it cites, [1] or
// [1, 3], so each citation can be shown as a link to its source. Numbers
// beyond the sources given stay as text.
export function cited(answer: string, sources: number): Piece[] {
  const out: Piece[] = [];
  let last = 0;
  for (const m of answer.matchAll(/\[(\d+(?:\s*,\s*\d+)*)\]/g)) {
    const nums = (m[1] ?? "").split(",").map((n) => Number(n.trim()));
    if (nums.some((n) => n < 1 || n > sources)) continue;
    if (m.index > last) out.push({ text: answer.slice(last, m.index) });
    for (const n of nums) out.push({ source: n });
    last = m.index + m[0].length;
  }
  if (last < answer.length) out.push({ text: answer.slice(last) });
  return out;
}
