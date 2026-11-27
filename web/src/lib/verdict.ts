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
  if (r.kind === "class" && (r.classes.length > 0 || r.baselineLabel !== "")) return classes(r);
  if (r.kind === "number" && r.examples > 0n) return numbers(r);
  if (r.kind === "boxes" && r.classes.length > 0) return boxes(r);
  if (r.kind === "text" && r.beforeLoss > 0 && !r.onTraining) return writing(r);
  return undefined;
}

function classes(r: RunReport): Verdict {
  // The most common answer and how often it is right. The server works it
  // out for any number of answers; older reports only have the per-answer
  // table, which is left out when there are many answers.
  const total = r.classes.reduce((n, c) => n + Number(c.support), 0);
  const top = [...r.classes].sort((a, b) => Number(b.support) - Number(a.support))[0];
  const label = r.baselineLabel || top?.label || "";
  const base = r.baselineLabel ? r.baseline : top && total > 0 ? Number(top.support) / total : 0;
  // Several answers to tell apart; one alone has nothing to learn.
  const several = r.classes.length > 1 || (r.classes.length === 0 && base < 1);
  const acc = r.accuracy;
  const always = t("Always guessing {label} would get {base} of 100 right", { label, base: pct(base) });
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
  if (several && acc >= 0.995 && total >= 50) {
    return {
      level: "weak",
      title: t("It gets every answer right, which is suspicious"),
      body: t("{always}; the model gets {acc}. Real data is rarely this clean.", { always, acc: pct(acc) }),
      next: t("A column probably gives the answer away: one only known once the answer is, or the answer under another name. The data check names it. Take it out of the file and teach again, or the model will do far worse in use."),
    };
  }
  if (several && acc <= base + 0.02) {
    return {
      level: "none",
      title: t("It hasn't learned much yet"),
      body: t("{always}; the model gets {acc}. It is doing about as well as that guess.", { always, acc: pct(acc) }),
      next: next ?? t("The columns it has may not say anything about the answer. Add a column that does, or check that the answer column is the right one."),
    };
  }
  if (several && acc < base + 0.1) {
    return { level: "weak", title: t("It has learned a little"), body: t("{always}; the model gets {acc}.", { always, acc: pct(acc) }), next: next ?? t("More rows, or a Thorough run, usually lifts a score like this.") };
  }
  return {
    level: "good",
    title: t("It gets {acc} of 100 right", { acc: pct(acc) }),
    body: several ? t("{always}, so the model has clearly learned from the columns.", { always }) : "",
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

function boxes(r: RunReport): Verdict {
  // The thing it misses most, among those with enough boxes to judge.
  const judged = r.classes.filter((c) => Number(c.support) >= 5);
  const worst = [...judged].sort((a, b) => a.recall - b.recall)[0];
  const next = worst && worst.recall < 0.7
    ? t("It misses {label} most: it finds {recall} of 100 of them. More photos with {label2} boxed would help most.", { label: worst.label, recall: pct(worst.recall), label2: worst.label })
    : undefined;
  if (r.ap50 < 0.2) {
    return { level: "none", title: t("It hasn't learned to find things yet"), body: t("On photos it never saw, it rarely puts a box in the right place."), next: next ?? t("More photos, with every thing in them boxed, usually help most.") };
  }
  if (r.ap50 < 0.5) {
    return { level: "weak", title: t("It finds some things"), body: t("On photos it never saw, it finds some things but misses or misplaces many."), next: next ?? t("More photos, with every thing in them boxed, usually help most.") };
  }
  return { level: "good", title: t("It finds things well"), body: t("On photos it never saw, most of its boxes land on the right thing."), next };
}

function writing(r: RunReport): Verdict {
  const now = pct(r.accuracy), was = pct(r.beforeAccuracy);
  if (r.logLoss >= r.beforeLoss - 0.01) {
    return {
      level: "none",
      title: t("This run didn't teach it much"),
      body: t("On examples it never saw, it guesses {now} of 100 word pieces of the answers right; it guessed {was} before.", { now, was }),
      next: t("More examples, or examples that show the same way of answering, usually help most."),
    };
  }
  return {
    level: "good",
    title: t("It learned to answer more like your examples"),
    body: t("On examples it never saw, it guesses {now} of 100 word pieces of the answers right, up from {was} before this run.", { now, was }),
  };
}
