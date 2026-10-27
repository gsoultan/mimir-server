import type { RunReport } from "../api/gen/mimir/v1/training_pb";

// number shows a measurement with sensible digits: "12.4", "0.913",
// "15,737".
export function number(n: number, digits = 2) {
  return n.toLocaleString(undefined, { maximumFractionDigits: Math.abs(n) >= 1000 ? 0 : digits });
}

// short shows an axis value briefly: "160K", "1.2M", "0.5".
export function short(n: number) {
  return n.toLocaleString(undefined, { notation: "compact", maximumFractionDigits: 1 });
}

// balanced says one answer is rare (under a fifth of the examples), so
// macro F1 is the fairer score: a model that never says it still gets
// high accuracy.
export function balanced(r: RunReport) {
  const total = r.classes.reduce((a, c) => a + Number(c.support), 0);
  return total > 0 && r.classes.length > 1 && r.classes.some((c) => Number(c.support) / total < 0.2);
}

// headline is a run's score in words, for its kind of answer.
export function headline(r: RunReport) {
  switch (r.kind) {
    case "number":
      return `usually off by about ${number(r.mae)}`;
    case "ranked":
      return `the right one is in its top ${r.k} for ${Math.round((r.hitAt.at(-1) ?? 0) * 100)} of 100`;
    default:
      return `gets ${Math.round(r.accuracy * 100)} of 100 right`;
  }
}
