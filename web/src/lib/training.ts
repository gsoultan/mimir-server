import { JobStatus, type Job } from "../api/gen/mimir/v1/training_pb";
import { t } from "../i18n";

export const statusWord: Record<JobStatus, string> = {
  [JobStatus.UNSPECIFIED]: t("Unknown"),
  [JobStatus.QUEUED]: t("Waiting to start"),
  [JobStatus.RUNNING]: t("Learning"),
  [JobStatus.SUCCEEDED]: t("Done"),
  [JobStatus.FAILED]: t("Failed"),
  [JobStatus.CANCELLED]: t("Stopped"),
};

export const statusColor: Record<JobStatus, string> = {
  [JobStatus.UNSPECIFIED]: "gray",
  [JobStatus.QUEUED]: "gray",
  [JobStatus.RUNNING]: "violet",
  [JobStatus.SUCCEEDED]: "teal",
  [JobStatus.FAILED]: "red",
  [JobStatus.CANCELLED]: "yellow",
};

// The same statuses as tones, for status pills.
export const jobTone: Record<JobStatus, "ok" | "warn" | "bad" | "busy" | "idle"> = {
  [JobStatus.UNSPECIFIED]: "idle",
  [JobStatus.QUEUED]: "idle",
  [JobStatus.RUNNING]: "busy",
  [JobStatus.SUCCEEDED]: "ok",
  [JobStatus.FAILED]: "bad",
  [JobStatus.CANCELLED]: "warn",
};

export function active(j: Job) {
  return j.status === JobStatus.QUEUED || j.status === JobStatus.RUNNING;
}

// Progress through the whole run, from 0 to 100.
export function progress(j: Job) {
  const epochs = j.settings?.epochs ?? 1;
  const within = j.stepsPerEpoch > 0 ? j.step / j.stepsPerEpoch : 0;
  return Math.min(100, ((j.epoch + within) / epochs) * 100);
}

// "gets 98 of 100 right". A number model can't be exactly right, so it
// counts answers within a tenth of the real one.
export function score(accuracy: number, number = false) {
  return number ? t("gets {n} of 100 close", { n: Math.round(accuracy * 100) }) : t("gets {n} of 100 right", { n: Math.round(accuracy * 100) });
}

// How often a model keeps learning, as a choice and in words. Looking
// every 15 minutes is only for a model that waits for new rows.
export const repeatChoices = [
  { value: "0", label: t("Off") },
  { value: "900", label: t("Every 15 minutes") },
  { value: "3600", label: t("Every hour") },
  { value: "86400", label: t("Every day") },
  { value: "604800", label: t("Every week") },
];

export function repeatChoicesFor(minNewRows: number | bigint, current?: bigint) {
  const choices = Number(minNewRows) > 0 ? repeatChoices : repeatChoices.filter((c) => c.value !== "900");
  if (current === undefined || choices.some((c) => c.value === String(current))) return choices;
  return [...choices, { value: String(current), label: repeatWord(current) }];
}

export function repeatWord(seconds: bigint) {
  const s = Number(seconds);
  if (s <= 0) return "";
  const known = repeatChoices.find((c) => Number(c.value) === s);
  if (known) return known.label.toLowerCase();
  if (s % 86400 === 0) return t("every {n1} days", { n1: s / 86400 });
  if (s < 3600) return t("every {n1} minutes", { n1: Math.round(s / 60) });
  return t("every {n1} hours", { n1: Math.round(s / 3600) });
}

// "keeps learning every hour, once 500 new rows are in"
export function keepWord(j: Job) {
  const every = repeatWord(j.repeatSeconds);
  return j.minNewRows > 0n ? t("looks {every} and learns once {n2} new rows are in", { every, n2: j.minNewRows.toLocaleString() }) : t("learns {every}", { every });
}
