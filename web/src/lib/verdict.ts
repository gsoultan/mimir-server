import type { RunReport } from "../api/gen/mimir/v1/training_pb";

import { number } from "./report";
import { t } from "../i18n";

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
  const always = t("Always guessing {label} would get {base} of 100 right", { label: top.label, base: pct(base) });
  // The answer it misses most, among those with enough examples to judge.
  const judged = r.classes.filter((c) => Number(c.support) >= 5);
  const worst = [...judged].sort((a, b) => a.recall - b.recall)[0];
  const miss = worst && worst.recall < 0.7 ? worst : undefined;
  const few = miss && Number(miss.support) < 50;
  const next = miss
    ? few
      ? t("It misses {label} most: it finds it {recall} of 100 times, from only {n3} examples kept aside. More rows that say {label2} would help most.", { label: miss.label, recall: pct(miss.recall), n3: Number(miss.support).toLocaleString(), label2: miss.label })
      : t("It misses {label} most: it finds it {recall} of 100 times. A column that tells {label2} apart from the others would help most.", { label: miss.label, recall: pct(miss.recall), label2: miss.label })
    : undefined;
  if (r.classes.length > 1 && acc >= 0.995 && total >= 50) {
    return {
      level: "weak",
      title: t("It gets every answer right, which is suspicious"),
      body: t("{always}; the model gets {acc}. Real data is rarely this clean.", { always, acc: pct(acc) }),
      next: t("A column probably gives the answer away: one only known once the answer is, or the answer under another name. The data check names it. Take it out of the file and teach again, or the model will do far worse in use."),
    };
  }
  if (r.classes.length > 1 && acc <= base + 0.02) {
    return {
      level: "none",
      title: t("It hasn't learned much yet"),
      body: t("{always}; the model gets {acc}. It is doing about as well as that guess.", { always, acc: pct(acc) }),
      next: next ?? t("The columns it has may not say anything about the answer. Add a column that does, or check that the answer column is the right one."),
    };
  }
  if (r.classes.length > 1 && acc < base + 0.1) {
    return { level: "weak", title: t("It has learned a little"), body: t("{always}; the model gets {acc}.", { always, acc: pct(acc) }), next: next ?? t("More rows, or a Thorough run, usually lifts a score like this.") };
  }
  return {
    level: "good",
    title: t("It gets {acc} of 100 right", { acc: pct(acc) }),
    body: r.classes.length > 1 ? t("{always}, so the model has clearly learned from the columns.", { always }) : "",
    next,
  };
}

function numbers(r: RunReport): Verdict {
  const off = t("It is usually off by about {mae}", { mae: number(r.mae) });
  if (r.r2 <= 0.05) {
    return { level: "none", title: t("It hasn't learned much yet"), body: t("{off}, about as far as always guessing the average would be.", { off }), next: t("The columns it has may not say anything about the number. Add a column that does, or check that the answer column is the right one.") };
  }
  if (r.r2 < 0.3) {
    return { level: "weak", title: t("It has learned a little"), body: t("{off}. It explains {r2} of 100 of what makes the number vary; the rest it can't see from these columns.", { off, r2: pct(r.r2) }), next: t("More rows, or a column that explains the rest, usually helps most.") };
  }
  return { level: "good", title: `${off}`, body: t("{within10} of 100 answers land within a tenth of the real number, and it explains {r2} of 100 of what makes it vary.", { within10: pct(r.within10), r2: pct(r.r2) }) };
}
