import type { Job, Layer } from "../api/gen/mimir/v1/training_pb";
import { t } from "../i18n";

// maxCompare is how many runs fit side by side, as the server allows.
export const maxCompare = 6;

// runColor gives the nth run its own color, in a fixed order checked for
// colorblind readers; the dark steps are in styles.css.
export const runColors = ["--run-1", "--run-2", "--run-3", "--run-4", "--run-5", "--run-6"];
export const runColor = (i: number) => `var(${runColors[i % runColors.length]})`;

// The compare page's address holds the runs as one comma-separated list.
export function parseIds(ids: string | undefined): string[] {
  return [...new Set((ids ?? "").split(",").map((s) => s.trim()).filter(Boolean))];
}

// trainingSeconds adds up how long each pass took.
export function trainingSeconds(j: Job): number {
  return j.history.reduce((sum, e) => sum + Number(e.durationMillis), 0) / 1000;
}

// took says a length of time briefly: "42 s", "3 min 5 s", "1 h 20 min".
export function took(seconds: number): string {
  const s = Math.round(seconds);
  if (seconds > 0 && seconds < 0.05) return t("under 0.1 s");
  if (s < 60) return `${seconds < 10 ? seconds.toFixed(1) : s} s`;
  if (s < 3600) return t("{n1} min{n2}", { n1: Math.floor(s / 60), n2: s % 60 ? ` ${s % 60} s` : "" });
  return `${Math.floor(s / 3600)} h${Math.floor((s % 3600) / 60) ? t(" {n1} min", { n1: Math.floor((s % 3600) / 60) }) : ""}`;
}

// shapeWord describes a designed table model's layers.
export function shapeWord(layers: Layer[]): string {
  if (layers.length === 0) return t("Usual");
  return layers.map((l) => (l.kind === "dense" ? t("{units} units", { units: l.units }) : t("forget {n1}%", { n1: Math.round(l.rate * 100) }))).join(" → ");
}

// bestPass is the highest accuracy any pass reached, and which pass.
export function bestPass(j: Job): { epoch: number; accuracy: number } | undefined {
  let top: { epoch: number; accuracy: number } | undefined;
  for (const e of j.history) {
    if (e.accuracy > 0 && (!top || e.accuracy > top.accuracy)) top = { epoch: e.epoch, accuracy: e.accuracy };
  }
  return top;
}

// againName is what a run trained again is called at first.
export function againName(name: string): string {
  const m = /^(.*) \(again(?: (\d+))?\)$/.exec(name);
  if (!m) return t("{name} (again)", { name });
  return t("{n1} (again {n2})", { n1: m[1], n2: m[2] ? Number(m[2]) + 1 : 2 });
}
