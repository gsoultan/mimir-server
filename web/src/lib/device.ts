// Models saved on this device. They live in the browser's cache, so they
// answer with no network; the device worker runs them.
import { fromJsonString, toJsonString } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { useEffect, useState } from "react";

import { transport } from "../api/client";
import { ArchitectureSchema, ModelSchema, ModelService, type Architecture, type Model } from "../api/gen/mimir/v1/model_pb";
import { device, modelsCache, runtimeCache, type Ask, type Reply, type Request } from "./device-shared";

export { device };

export type Saved = { model: Model; arch: Architecture; bytes: number; saved: Date };
export type Answer = Exclude<Reply, { error: string }>;

// MaxBytes matches the server's limit on models a browser takes.
export const maxBytes = 50 << 20;

// supported says whether this browser can run models itself.
export const supported = typeof caches !== "undefined" && typeof WebAssembly !== "undefined" && typeof Worker !== "undefined";

const key = (id: string) => `/device/models/${id}`;
const changed = "mimir-device-changed";

async function read(res: Response): Promise<Saved | undefined> {
  try {
    const h = res.headers;
    return {
      model: fromJsonString(ModelSchema, decodeURIComponent(h.get("x-mimir-model") ?? "")),
      arch: fromJsonString(ArchitectureSchema, decodeURIComponent(h.get("x-mimir-arch") ?? "")),
      bytes: Number(h.get("x-mimir-bytes") ?? 0),
      saved: new Date(h.get("x-mimir-saved") ?? 0),
    };
  } catch {
    return undefined; // saved by an older version of this page
  }
}

// list returns the models saved on this device, most recently saved first.
export async function list(): Promise<Saved[]> {
  if (!supported) return [];
  const cache = await caches.open(modelsCache);
  const out: Saved[] = [];
  for (const req of await cache.keys()) {
    const res = await cache.match(req);
    const s = res && (await read(res));
    if (s) out.push(s);
  }
  return out.sort((a, b) => b.saved.getTime() - a.saved.getTime());
}

export async function get(id: string): Promise<Saved | undefined> {
  if (!supported) return undefined;
  const res = await (await caches.open(modelsCache)).match(key(id));
  return res && read(res);
}

// save downloads model m to this device, or the newer version of it.
export async function save(m: Model, arch: Architecture): Promise<Saved> {
  const client = createClient(ModelService, transport);
  const res = await client.getDeviceModel({ id: m.id });
  const model = res.model ?? m;
  const saved = new Date();
  const headers = {
    "content-type": "application/octet-stream",
    "x-mimir-model": encodeURIComponent(toJsonString(ModelSchema, model)),
    "x-mimir-arch": encodeURIComponent(toJsonString(ArchitectureSchema, arch)),
    "x-mimir-device": encodeURIComponent(JSON.stringify(device(model))),
    "x-mimir-bytes": String(res.weights.byteLength),
    "x-mimir-saved": saved.toISOString(),
  };
  await (await caches.open(modelsCache)).put(key(m.id), new Response(new Blob([res.weights as Uint8Array<ArrayBuffer>]), { headers }));
  // Ask the browser not to clear saved models when space runs low.
  void navigator.storage?.persist?.();
  post({ type: "forget", seq: 0, id: m.id });
  dispatchEvent(new Event(changed));
  return { model, arch, bytes: res.weights.byteLength, saved };
}

export async function remove(id: string): Promise<void> {
  await (await caches.open(modelsCache)).delete(key(id));
  post({ type: "forget", seq: 0, id });
  dispatchEvent(new Event(changed));
}

// removeAll deletes every saved model and the runtime.
export async function removeAll(): Promise<void> {
  await caches.delete(modelsCache);
  await caches.delete(runtimeCache);
  worker?.terminate();
  worker = undefined;
  dispatchEvent(new Event(changed));
}

// usage is how much this site keeps on the device, and how much it may.
export async function usage(): Promise<{ used: number; quota: number } | undefined> {
  const e = await navigator.storage?.estimate?.();
  return e && { used: e.usage ?? 0, quota: e.quota ?? 0 };
}

// useSaved lists the saved models and keeps the list current.
export function useSaved(): Saved[] | undefined {
  const [saved, setSaved] = useState<Saved[]>();
  useEffect(() => {
    const load = () => void list().then(setSaved, () => setSaved([]));
    load();
    addEventListener(changed, load);
    return () => removeEventListener(changed, load);
  }, []);
  return saved;
}

let worker: Worker | undefined;
let seq = 0;
const waiting = new Map<number, (r: Reply) => void>();

function post(r: Request) {
  worker?.postMessage(r);
}

// answer asks a saved model a question on this device.
export function answer(q: Omit<Ask, "type" | "seq">): Promise<Answer> {
  if (!worker) {
    worker = new Worker(new URL("../workers/device.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (e: MessageEvent<{ seq: number; reply: Reply }>) => {
      waiting.get(e.data.seq)?.(e.data.reply);
      waiting.delete(e.data.seq);
    };
    worker.onerror = () => {
      for (const done of waiting.values()) done({ error: "The model stopped on this device. Try again." });
      waiting.clear();
      worker = undefined;
    };
  }
  const n = ++seq;
  return new Promise((resolve, reject) => {
    waiting.set(n, (r) => ("error" in r ? reject(new Error(r.error)) : resolve(r)));
    worker!.postMessage({ ...q, type: "ask", seq: n } satisfies Ask);
  });
}
