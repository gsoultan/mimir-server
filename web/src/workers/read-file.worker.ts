// Takes a first look at a picked model file off the main thread. Only the
// start of the file is read, the part that says what's in it, so a model
// of gigabytes is looked at as quickly as a small one; the file itself is
// sent from disk when it is added.
import { headLength, inspect, type Inspection } from "../lib/inspect";

export type Reply = { inspection: Inspection } | { error: string };

// maxHead bounds what is read of a .mim file's index, which names every
// tensor: far more than any model has.
const maxHead = 64 << 20;

self.onmessage = async (e: MessageEvent<File>) => {
  try {
    const file = e.data;
    const first = new Uint8Array(await file.slice(0, 64).arrayBuffer());
    const n = Math.min(headLength(first), file.size, maxHead);
    const head = n <= first.length ? first : new Uint8Array(await file.slice(0, n).arrayBuffer());
    self.postMessage({ inspection: inspect(head, file.name) } satisfies Reply);
  } catch {
    self.postMessage({ error: "Couldn't read that file." } satisfies Reply);
  }
};
