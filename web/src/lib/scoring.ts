import type { Architecture } from "../api/gen/mimir/v1/model_pb";
import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import type { Scoring } from "../api/gen/mimir/v1/scoring_pb";
import { t } from "../i18n";

// scores says whether a model of this architecture can answer the rows of
// a table: it reads a row and answers with a label, a number or a ranked
// list, as the server's scorable does.
export function scores(a: Architecture | undefined): boolean {
  if (!a || a.input !== "table" || a.inputShape.length > 0) return false;
  return a.output === "" || a.output === "class" || a.output === "number" || a.output === "ranked";
}

// plansVisits says whether a model can rank customers for a visit plan:
// it reads a row and answers a label (whether they order) or a number
// (days until they order).
export function plansVisits(a: Architecture | undefined): boolean {
  return scores(a) && (a!.output === "" || a!.output === "class" || a!.output === "number");
}

// How often a scoring from a database or storage can run again.
export const repeats = [
  { value: "0", label: t("Just once") },
  { value: "3600", label: t("Every hour") },
  { value: "86400", label: t("Every day") },
  { value: "604800", label: t("Every week") },
];

export const waiting = (s: Scoring) => s.status === JobStatus.QUEUED || s.status === JobStatus.RUNNING;

// progress is how far a scoring is, from 0 to 100.
export function progress(s: Scoring): number {
  if (s.status === JobStatus.SUCCEEDED) return 100;
  const rows = Number(s.rows);
  return rows > 0 ? Math.min(100, (Number(s.done) / rows) * 100) : 0;
}

// statusText says where a scoring is, in words.
export function statusText(s: Scoring): string {
  switch (s.status) {
    case JobStatus.QUEUED: return t("Waiting");
    case JobStatus.RUNNING: return Number(s.rows) > 0 ? t("Scoring: {n1} of {n2} rows", { n1: Number(s.done).toLocaleString(), n2: Number(s.rows).toLocaleString() }) : t("Reading the rows");
    case JobStatus.SUCCEEDED: {
      const failed = Number(s.failed);
      return t("{n1} rows answered{n2}", { n1: Number(s.done).toLocaleString(), n2: failed > 0 ? t(", {n1} couldn't be", { n1: failed.toLocaleString() }) : "" });
    }
    case JobStatus.FAILED: return t("Failed");
    case JobStatus.CANCELLED: return t("Cancelled");
  }
  return "";
}

// resultsURL is where a scoring's results download from.
export const resultsURL = (id: string) => `/api/scorings/${encodeURIComponent(id)}/results`;
