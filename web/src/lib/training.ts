import { JobStatus, type Job } from "../api/gen/mimir/v1/training_pb";

export const statusWord: Record<JobStatus, string> = {
  [JobStatus.UNSPECIFIED]: "Unknown",
  [JobStatus.QUEUED]: "Waiting to start",
  [JobStatus.RUNNING]: "Learning",
  [JobStatus.SUCCEEDED]: "Done",
  [JobStatus.FAILED]: "Failed",
  [JobStatus.CANCELLED]: "Stopped",
};

export const statusColor: Record<JobStatus, string> = {
  [JobStatus.UNSPECIFIED]: "gray",
  [JobStatus.QUEUED]: "gray",
  [JobStatus.RUNNING]: "blue",
  [JobStatus.SUCCEEDED]: "teal",
  [JobStatus.FAILED]: "red",
  [JobStatus.CANCELLED]: "yellow",
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

// "gets 98 of 100 right"
export function score(accuracy: number) {
  return `gets ${Math.round(accuracy * 100)} of 100 right`;
}

// How often a model keeps learning, as a choice and in words.
export const repeatChoices = [
  { value: "0", label: "Off" },
  { value: "3600", label: "Every hour" },
  { value: "86400", label: "Every day" },
  { value: "604800", label: "Every week" },
];

export function repeatWord(seconds: bigint) {
  const s = Number(seconds);
  if (s <= 0) return "";
  const known = repeatChoices.find((c) => Number(c.value) === s);
  if (known) return known.label.toLowerCase();
  if (s % 86400 === 0) return `every ${s / 86400} days`;
  return `every ${Math.round(s / 3600)} hours`;
}
