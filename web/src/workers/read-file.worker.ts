// Reads a picked file off the main thread, so a large model never freezes
// the page, and takes a first look at what's inside.
import { inspect, type Inspection } from "../lib/safetensors";

export type Reply = { bytes: Uint8Array; inspection: Inspection } | { error: string };

self.onmessage = async (e: MessageEvent<File>) => {
  try {
    const bytes = new Uint8Array(await e.data.arrayBuffer());
    const reply: Reply = { bytes, inspection: inspect(bytes) };
    self.postMessage(reply, { transfer: [bytes.buffer] });
  } catch {
    self.postMessage({ error: "Couldn't read that file." } satisfies Reply);
  }
};
