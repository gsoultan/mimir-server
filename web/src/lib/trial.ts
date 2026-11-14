import type { GetModelTrialResponse, ModelTrial } from "../api/gen/mimir/v1/model_pb";
import { t } from "../i18n";

// The shares of questions a version can be tried on, in plain words.
export const shareChoices = [
  { value: "5", label: "5%" },
  { value: "10", label: "10%" },
  { value: "25", label: "25%" },
  { value: "50", label: "50%" },
];

// What a model does with a better version keep-learning makes.
export const newVersionChoices = [
  { value: "0", label: t("Put it live at once") },
  { value: "10", label: t("Try it on 10% of questions first") },
  { value: "25", label: t("Try it on 25% of questions first") },
  { value: "50", label: t("Try it on half the questions first") },
];

// trialHeadline says what a trial does, in a line.
export function trialHeadline(trial: ModelTrial, tried: number) {
  return trial.shadow
    ? t("Version {tried} answers a copy of every question on the side. People still get the live version's answers.", { tried })
    : t("Version {tried} answers {share}% of the questions. The rest get the live version.", { tried, share: trial.share });
}

const per100 = (x: number) => t("{n} of 100", { n: Math.round(x * 100) });

// trialVerdict sums up how the version tried does against the live one,
// and whether it looks ready to go live.
export function trialVerdict(r: GetModelTrialResponse, number: boolean): { tone: "ok" | "warn" | "wait"; text: string } {
  const live = r.live, tried = r.tried;
  if (!live || !tried || !r.trial) return { tone: "wait", text: "" };
  const right = number ? t("close") : t("right");
  if (!r.enoughFeedback) {
    const agree = r.trial.shadow && tried.answers > 0n
      ? t("So far it gives the same answer as version {versionNumber} for {agreeShare} questions. ", { versionNumber: live.versionNumber, agreeShare: per100(r.agreeShare) })
      : "";
    return {
      tone: "wait",
      text: t("{agree}To see which version is {right} more often, your apps need to send back the right answers for at least 20 questions each (see Use from your app).", { agree, right }),
    };
  }
  if (tried.accuracy + 0.02 >= live.accuracy) {
    return { tone: "ok", text: t("Version {versionNumber} is {right} for {accuracy} checked answers, version {versionNumber2} for {accuracy2}. It looks ready to answer every question.", { versionNumber: tried.versionNumber, right, accuracy: per100(tried.accuracy), versionNumber2: live.versionNumber, accuracy2: per100(live.accuracy) }) };
  }
  return { tone: "warn", text: t("Version {versionNumber} is {right} for {accuracy} checked answers, fewer than version {versionNumber2} with {accuracy2}. Stop trying it, or wait for more answers.", { versionNumber: tried.versionNumber, right, accuracy: per100(tried.accuracy), versionNumber2: live.versionNumber, accuracy2: per100(live.accuracy) }) };
}
