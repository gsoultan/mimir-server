// Answers with models saved on this device, using the browser runtime
// (cmd/mimir-wasm). It keeps the runtime in the browser's cache, so it
// starts with no network once it has been downloaded.
import "./wasm_exec.js";

import { modelsCache, runtimeCache, type Ask, type Reply, type Request } from "../lib/device-shared";

declare const Go: new () => { importObject: WebAssembly.Imports; run(i: WebAssembly.Instance): Promise<void> };
type Mimir = {
  load(model: string, weights: Uint8Array): string;
  predict(id: string, question: string, photo: Uint8Array | null): string;
  unload(id: string): string;
};

let started: Promise<Mimir> | undefined;
const loaded = new Map<string, string>(); // model id to the version loaded

// runtime finds the browser runtime's name: from the server when it can be
// reached, otherwise the one downloaded last.
async function runtime(cache: Cache): Promise<string> {
  try {
    const res = await fetch("/wasm/runtime.json", { cache: "no-cache" });
    if (!res.ok) throw new Error(res.statusText);
    const { wasm } = (await res.json()) as { wasm: string };
    await cache.put("/wasm/runtime.json", new Response(JSON.stringify({ wasm }), { headers: { "content-type": "application/json" } }));
    return wasm;
  } catch {
    const res = await cache.match("/wasm/runtime.json");
    if (!res) throw new Error("This device hasn't downloaded the runtime yet. Connect to the server once, then try again.");
    return ((await res.json()) as { wasm: string }).wasm;
  }
}

async function start(): Promise<Mimir> {
  const cache = await caches.open(runtimeCache);
  const name = await runtime(cache);
  const url = `/wasm/${name}`;
  let res = await cache.match(url);
  if (!res) {
    res = await fetch(url);
    if (!res.ok) throw new Error("Couldn't download the runtime. Check that you're online.");
    await cache.put(url, res.clone());
    // An older runtime isn't needed anymore.
    for (const req of await cache.keys()) {
      const path = new URL(req.url).pathname;
      if (path !== url && path !== "/wasm/runtime.json") await cache.delete(req);
    }
  }
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(await res.arrayBuffer(), go.importObject);
  void go.run(instance);
  const mimir = (globalThis as unknown as { mimir?: Mimir }).mimir;
  if (!mimir) throw new Error("The runtime didn't start.");
  return mimir;
}

function parse<T>(json: string): T {
  const v = JSON.parse(json) as T & { error?: string };
  if (v.error) throw new Error(v.error);
  return v;
}

async function load(mimir: Mimir, id: string): Promise<void> {
  const res = await (await caches.open(modelsCache)).match(`/device/models/${id}`);
  if (!res) throw new Error("This model isn't saved on this device anymore.");
  const model = decodeURIComponent(res.headers.get("x-mimir-device") ?? "");
  const { version } = JSON.parse(model) as { version: string };
  if (loaded.get(id) === version) return;
  parse(mimir.load(model, new Uint8Array(await res.arrayBuffer())));
  loaded.set(id, version);
}

async function ask(q: Ask): Promise<Reply> {
  started ??= start().catch((err: unknown) => { started = undefined; throw err; });
  const mimir = await started;
  await load(mimir, q.id);
  const t = performance.now();
  const answer = parse<Omit<Extract<Reply, { ms: number }>, "ms">>(mimir.predict(q.id, JSON.stringify({ fields: q.fields, input: q.input, topK: q.topK }), q.photo ?? null));
  return { ...answer, ms: performance.now() - t };
}

self.onmessage = async (e: MessageEvent<Request>) => {
  const r = e.data;
  try {
    if (r.type === "forget") {
      loaded.delete(r.id);
      if (started) (await started).unload(r.id);
      return;
    }
    self.postMessage({ seq: r.seq, reply: await ask(r) });
  } catch (err) {
    self.postMessage({ seq: r.seq, reply: { error: err instanceof Error ? err.message : String(err) } });
  }
};
