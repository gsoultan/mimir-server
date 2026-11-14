import type { GetModelTrialResponse, ModelTrial } from "../api/gen/mimir/v1/model_pb";

// The shares of questions a version can be tried on, in plain words.
export const shareChoices = [
  { value: "5", label: "5%" },
  { value: "10", label: "10%" },
  { value: "25", label: "25%" },
  { value: "50", label: "50%" },
];

// What a model does with a better version keep-learning makes.
export const newVersionChoices = [
  { value: "0", label: "Put it live at once" },
  { value: "10", label: "Try it on 10% of questions first" },
  { value: "25", label: "Try it on 25% of questions first" },
  { value: "50", label: "Try it on half the questions first" },
];

// trialHeadline says what a trial does, in a line.
export function trialHeadline(t: ModelTrial, tried: number) {
  return t.shadow
    ? `Version ${tried} answers a copy of every question on the side. People still get the live version's answers.`
    : `Version ${tried} answers ${t.share}% of the questions. The rest get the live version.`;
}

const per100 = (x: number) => `${Math.round(x * 100)} of 100`;

// trialVerdict sums up how the version tried does against the live one,
// and whether it looks ready to go live.
export function trialVerdict(r: GetModelTrialResponse, number: boolean): { tone: "ok" | "warn" | "wait"; text: string } {
  const live = r.live, tried = r.tried;
  if (!live || !tried || !r.trial) return { tone: "wait", text: "" };
  const right = number ? "close" : "right";
  if (!r.enoughFeedback) {
    const agree = r.trial.shadow && tried.answers > 0n
      ? `So far it gives the same answer as version ${live.versionNumber} for ${per100(r.agreeShare)} questions. `
      : "";
    return {
      tone: "wait",
      text: `${agree}To see which version is ${right} more often, your apps need to send back the right answers for at least 20 questions each (see Use from your app).`,
    };
  }
  if (tried.accuracy + 0.02 >= live.accuracy) {
    return { tone: "ok", text: `Version ${tried.versionNumber} is ${right} for ${per100(tried.accuracy)} checked answers, version ${live.versionNumber} for ${per100(live.accuracy)}. It looks ready to answer every question.` };
  }
  return { tone: "warn", text: `Version ${tried.versionNumber} is ${right} for ${per100(tried.accuracy)} checked answers, fewer than version ${live.versionNumber} with ${per100(live.accuracy)}. Stop trying it, or wait for more answers.` };
}
