import type { Benchmark, BenchmarkScore } from "../api/gen/mimir/v1/benchmark_pb";

import { number } from "./report";

// headlineText is a score as the benchmark measures it: a share for
// right answers and the balanced score, the answer's own units for a
// number model's average miss.
export function headlineText(b: Benchmark, headline: number): string {
  if (b.kind === "number") return `off by ${number(headline)}`;
  if (b.metric.startsWith("balanced")) return number(headline, 3);
  return `${(headline * 100).toFixed(1)}%`;
}

// metricHint says in a sentence what the leaderboard is sorted by, and
// which way is better.
export function metricHint(b: Benchmark): string {
  if (b.kind === "number") return "Sorted by how far off each model usually is; less is better.";
  if (b.metric.startsWith("balanced")) return "Sorted by the balanced score, since one answer is rare: a model that never says it would still get most right.";
  return "Sorted by the share of right answers.";
}

// metricTitle is the leaderboard column's heading for the score it
// sorts by.
export function metricTitle(b: Benchmark): string {
  const m = b.metric;
  return m === "" ? "Score" : m.charAt(0).toUpperCase() + m.slice(1);
}

// medal marks the first three places, so the top of a leaderboard reads
// at a glance.
export function medal(rank: number): string {
  return ["🥇", "🥈", "🥉"][rank - 1] ?? "";
}

// behind is how far a score is from the leader's, in the benchmark's own
// terms: "the best" for the leader, "the same" for a tie, and "" for
// anything unfinished.
export function behind(b: Benchmark, scores: BenchmarkScore[], s: BenchmarkScore): string {
  const top = scores.find((x) => x.rank === 1);
  if (!top || s.rank === 0) return "";
  if (s.rank === 1) return "the best";
  const gap = b.lowerIsBetter ? s.headline - top.headline : top.headline - s.headline;
  if (gap <= 0) return "the same score";
  if (b.kind === "number") return `${number(gap)} further off`;
  if (b.metric.startsWith("balanced")) return `${number(gap, 3)} behind`;
  return `${(gap * 100).toFixed(1)} points behind`;
}
