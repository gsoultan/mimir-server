import type { Architecture, Contribution, Model } from "../api/gen/mimir/v1/model_pb";

// explains says whether a model's answers can be explained, as the
// server's newExplainer does: it reads table rows with named columns and
// picks a label or answers with a number.
export function explains(m: Model, a: Architecture | undefined): boolean {
  if (!a || a.input !== "table" || a.inputShape.length > 0 || a.custom) return false;
  return m.columns.length > 0 && (a.output === "" || a.output === "class" || a.output === "number");
}

const num = (v: number) => v.toLocaleString(undefined, { maximumFractionDigits: 2 });

// effectText says how far a column moved an answer: in points of chance
// for a label, in the number's own units otherwise.
export function effectText(effect: number, number: boolean): string {
  const sign = effect > 0 ? "+" : effect < 0 ? "−" : "";
  if (number) return `${sign}${num(Math.abs(effect))}`;
  const pts = Math.abs(effect) * 100;
  return `${sign}${pts < 1 && pts > 0 ? "<1" : Math.round(pts)} pts`;
}

// because says in words what one column did.
export function because(c: Contribution, label: string, number: boolean): string {
  const what = number ? "the answer" : `the chance of ${label}`;
  const how = c.effect >= 0 ? "raised" : "lowered";
  const by = number ? num(Math.abs(c.effect)) : `${Math.round(Math.abs(c.effect) * 100)} points`;
  return `${c.column} ${c.value === "" ? "left empty" : `of ${c.value}`} ${how} ${what} by ${by}, compared with ${c.typical}.`;
}

// answerText is an explained answer as people read it.
export const answerText = (v: number, number: boolean) => (number ? num(v) : `${Math.round(v * 100)}%`);
