// What the page and the device worker say to each other, and where they
// keep things in the browser's cache.

import type { Model } from "../api/gen/mimir/v1/model_pb";

export const modelsCache = "mimir-models";
export const runtimeCache = "mimir-runtime";

// Ask is a question for a saved model: fields by column name, a photo, or
// the numbers themselves.
export type Ask = {
  type: "ask";
  seq: number;
  id: string;
  fields?: Record<string, string>;
  photo?: Uint8Array;
  sound?: Uint8Array;
  input?: number[];
  topK: number;
};

export type Request = Ask | { type: "forget"; seq: number; id: string };

export type Reply =
  | { number?: boolean; value?: number; guesses?: { label: string; probability: number }[]; values?: number[]; boxes?: { label: string; score: number; left: number; top: number; width: number; height: number }[]; ms: number }
  | { error: string };

// device is what the worker needs to know about a model; see
// internal/device.Model.
export function device(m: Model) {
  return {
    id: m.id,
    version: m.version,
    architecture: m.architecture,
    inputShape: m.inputShape.map(Number),
    labels: m.labels,
    features: m.features,
    columns: m.columns.map((c) => ({ name: c.name, kind: c.kind, values: c.values, fill: c.fill })),
  };
}
