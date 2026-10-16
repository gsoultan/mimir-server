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
