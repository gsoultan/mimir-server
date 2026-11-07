import type { RunReport } from "../api/gen/mimir/v1/training_pb";

import { number } from "./report";

// Verdict says what a run's scores mean in plain words: how the model
// compares with the dumbest guess, and the one thing that would help it
// most. It is for people who brought their own data and don't read F1.
export type Verdict = {
  // level: "good" learned something clear, "weak" barely beat guessing,
  // "none" didn't.
  level: "good" | "weak" | "none";
  title: string;
  body: string;
  // next is one step to take, when there is one.
  next?: string;
};

const pct = (x: number) => Math.round(x * 100);

export function verdict(r: RunReport): Verdict | undefined {
  if (r.kind === "class" && r.classes.length > 0) return classes(r);
  if (r.kind === "number" && r.examples > 0n) return numbers(r);
  return undefined;
}

function classes(r: RunReport): Verdict {
  const total = r.classes.reduce((n, c) => n + Number(c.support), 0);
  const top = [...r.classes].sort((a, b) => Number(b.support) - Number(a.support))[0]!;
  const base = total > 0 ? Number(top.support) / total : 0;
  const acc = r.accuracy;
  const always = `Always guessing ${top.label} would get ${pct(base)} of 100 right`;
  // The answer it misses most, among those with enough examples to judge.
  const judged = r.classes.filter((c) => Number(c.support) >= 5);
  const worst = [...judged].sort((a, b) => a.recall - b.recall)[0];
  const miss = worst && worst.recall < 0.7 ? worst : undefined;
  const few = miss && Number(miss.support) < 50;
  const next = miss
    ? few
      ? `It misses ${miss.label} most: it finds it ${pct(miss.recall)} of 100 times, from only ${Number(miss.support).toLocaleString()} examples kept aside. More rows that say ${miss.label} would help most.`
      : `It misses ${miss.label} most: it finds it ${pct(miss.recall)} of 100 times. A column that tells ${miss.label} apart from the others would help most.`
    : undefined;
  if (r.classes.length > 1 && acc >= 0.995 && total >= 50) {
    return {
      level: "weak",
      title: "It gets every answer right, which is suspicious",
      body: `${always}; the model gets ${pct(acc)}. Real data is rarely this clean.`,
      next: "A column probably gives the answer away: one only known once the answer is, or the answer under another name. The data check names it. Take it out of the file and teach again, or the model will do far worse in use.",
    };
  }
  if (r.classes.length > 1 && acc <= base + 0.02) {
    return {
      level: "none",
      title: "It hasn't learned much yet",
      body: `${always}; the model gets ${pct(acc)}. It is doing about as well as that guess.`,
      next: next ?? "The columns it has may not say anything about the answer. Add a column that does, or check that the answer column is the right one.",
    };
  }
  if (r.classes.length > 1 && acc < base + 0.1) {
    return { level: "weak", title: "It has learned a little", body: `${always}; the model gets ${pct(acc)}.`, next: next ?? "More rows, or a Thorough run, usually lifts a score like this." };
  }
  return {
    level: "good",
    title: `It gets ${pct(acc)} of 100 right`,
    body: r.classes.length > 1 ? `${always}, so the model has clearly learned from the columns.` : "",
    next,
  };
}

function numbers(r: RunReport): Verdict {
  const off = `It is usually off by about ${number(r.mae)}`;
  if (r.r2 <= 0.05) {
    return { level: "none", title: "It hasn't learned much yet", body: `${off}, about as far as always guessing the average would be.`, next: "The columns it has may not say anything about the number. Add a column that does, or check that the answer column is the right one." };
  }
  if (r.r2 < 0.3) {
    return { level: "weak", title: "It has learned a little", body: `${off}. It explains ${pct(r.r2)} of 100 of what makes the number vary; the rest it can't see from these columns.`, next: "More rows, or a column that explains the rest, usually helps most." };
  }
  return { level: "good", title: `${off}`, body: `${pct(r.within10)} of 100 answers land within a tenth of the real number, and it explains ${pct(r.r2)} of 100 of what makes it vary.` };
}
