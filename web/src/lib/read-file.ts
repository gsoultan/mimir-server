import type { Reply } from "../workers/read-file.worker";

export function readFile(file: File): Promise<Reply> {
  return new Promise((resolve) => {
    const w = new Worker(new URL("../workers/read-file.worker.ts", import.meta.url), { type: "module" });
    w.onmessage = (e: MessageEvent<Reply>) => { resolve(e.data); w.terminate(); };
    w.onerror = () => { resolve({ error: "Couldn't read that file." }); w.terminate(); };
    w.postMessage(file);
  });
}
