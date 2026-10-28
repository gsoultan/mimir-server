import type { Architecture } from "../api/gen/mimir/v1/model_pb";
import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import type { Scoring } from "../api/gen/mimir/v1/scoring_pb";

// scores says whether a model of this architecture can answer the rows of
// a table: it reads a row and answers with a label, a number or a ranked
// list, as the server's scorable does.
export function scores(a: Architecture | undefined): boolean {
  if (!a || a.input !== "table" || a.inputShape.length > 0) return false;
  return a.output === "" || a.output === "class" || a.output === "number" || a.output === "ranked";
}

// How often a scoring from a database or storage can run again.
export const repeats = [
  { value: "0", label: "Just once" },
  { value: "3600", label: "Every hour" },
  { value: "86400", label: "Every day" },
  { value: "604800", label: "Every week" },
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
    case JobStatus.QUEUED: return "Waiting";
    case JobStatus.RUNNING: return Number(s.rows) > 0 ? `Scoring: ${Number(s.done).toLocaleString()} of ${Number(s.rows).toLocaleString()} rows` : "Reading the rows";
    case JobStatus.SUCCEEDED: {
      const failed = Number(s.failed);
      return `${Number(s.done).toLocaleString()} rows answered${failed > 0 ? `, ${failed.toLocaleString()} couldn't be` : ""}`;
    }
    case JobStatus.FAILED: return "Failed";
    case JobStatus.CANCELLED: return "Cancelled";
  }
  return "";
}

// resultsURL is where a scoring's results download from.
export const resultsURL = (id: string) => `/api/scorings/${encodeURIComponent(id)}/results`;
